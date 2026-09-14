package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/turn/v5"
)

func TestConferenceTURNAuthenticationAndEncryptedPacketRelay(t *testing.T) {
	const roomCode = "123456789"
	const participantID = "00112233445566778899aabb"
	participant := &conferenceParticipant{id: participantID, done: make(chan struct{})}
	b := &broker{meetings: map[string]meetingRoom{
		roomCode: {expires: time.Now().Add(time.Hour), conference: &conferenceRoom{participants: map[string]*conferenceParticipant{participantID: participant}, hostID: participantID}},
	}}
	secretFile := filepath.Join(t.TempDir(), "turn.secret")
	if err := os.WriteFile(secretFile, []byte(strings.Repeat("conference-secret-", 3)), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := startConferenceTURN(b, conferenceTURNConfig{
		Listen: "127.0.0.1:0", PublicAddress: "127.0.0.1:8253", SecretFile: secretFile, Realm: "yudesk-test",
		RelayMinPort: 39200, RelayMaxPort: 39263, AllowedPeerCIDRs: []string{"127.0.0.0/8"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	policy := m.policy(roomCode, participantID, time.Now().Add(time.Hour), "stun:127.0.0.1:8253", 3000)
	if !policy.RelayEnabled || policy.DirectTimeoutMS != 3000 || len(policy.ICEServers) != 2 {
		t.Fatalf("invalid policy: %+v", policy)
	}
	credential := policy.ICEServers[1]
	if credential.Username == "" || credential.Credential == "" || bytes.Contains([]byte(credential.Credential), m.secret) {
		t.Fatal("unsafe or missing TURN credential")
	}
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	client, err := turn.NewClient(&turn.ClientConfig{TURNServerAddr: m.address, STUNServerAddr: m.address, Conn: udp, Username: credential.Username, Password: credential.Credential, Realm: m.config.Realm, RTO: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.Listen(); err != nil {
		t.Fatal(err)
	}
	allocation, err := client.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer allocation.Close()
	peer, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	payload := []byte("opaque-dtls-srtp-packet")
	if _, err = allocation.WriteTo(payload, peer.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	buffer := make([]byte, 128)
	n, source, err := peer.ReadFrom(buffer)
	if err != nil || !bytes.Equal(buffer[:n], payload) {
		t.Fatalf("TURN outbound relay failed: %v", err)
	}
	if _, err = peer.WriteTo([]byte("reply"), source); err != nil {
		t.Fatal(err)
	}
	_ = allocation.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = allocation.ReadFrom(buffer)
	if err != nil || string(buffer[:n]) != "reply" {
		t.Fatalf("TURN inbound relay failed: %v", err)
	}
	tcp, err := net.DialTimeout("tcp4", m.address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tcpTransport := turn.NewSTUNConn(tcp)
	defer tcpTransport.Close()
	tcpClient, err := turn.NewClient(&turn.ClientConfig{TURNServerAddr: m.address, STUNServerAddr: m.address, Conn: tcpTransport, Username: credential.Username, Password: credential.Credential, Realm: m.config.Realm, RTO: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer tcpClient.Close()
	if err = tcpClient.Listen(); err != nil {
		t.Fatal(err)
	}
	tcpAllocation, err := tcpClient.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	defer tcpAllocation.Close()
	if _, err = tcpAllocation.WriteTo([]byte("turn-over-tcp"), peer.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = peer.ReadFrom(buffer)
	if err != nil || string(buffer[:n]) != "turn-over-tcp" {
		t.Fatalf("TURN TCP transport relay failed: %v", err)
	}
	close(participant.done)
	m.prune()
	if m.alive(credential.Username) || len(m.snapshot()) != 0 {
		t.Fatal("leaving the meeting retained TURN authorization")
	}
}

func TestConferenceTURNCredentialAndPeerPolicyBounds(t *testing.T) {
	m := &conferenceTURN{config: conferenceTURNConfig{Realm: "yudesk"}}
	for _, ip := range []string{"0.0.0.0", "127.0.0.1", "10.1.2.3", "192.168.1.1", "169.254.169.254", "100.64.1.2", "224.0.0.1", "::1", "fc00::1"} {
		if m.permission(nil, net.ParseIP(ip)) {
			t.Fatalf("private peer allowed: %s", ip)
		}
	}
	if !m.permission(nil, net.ParseIP("223.166.147.252")) {
		t.Fatal("public peer denied")
	}
	for _, username := range []string{"bad", "0:123456789:00112233445566778899aabb", "9999999999:123456789:00112233445566778899aabb"} {
		if _, _, ok := m.identity(username); ok {
			t.Fatalf("invalid credential accepted: %s", username)
		}
	}
}

func TestRemoteTURNPolicyIsBoundToActiveSession(t *testing.T) {
	const deviceID = "00112233445566778899AABB"
	const sessionID = "00112233445566778899aabbccddeeff"
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	b := &broker{active: map[string]activeSession{deviceID: {agent: left, viewer: right, owner: "device:" + deviceID, id: sessionID}}}
	m := &conferenceTURN{owner: b, config: conferenceTURNConfig{PublicAddress: "relay.example:8254", Realm: "yudesk"}, secret: []byte(strings.Repeat("remote-secret-", 3))}
	policy := m.remotePolicy(deviceID, sessionID, "viewer", "stun:relay.example:8233", 3700)
	if !policy.RelayEnabled || policy.DirectTimeoutMS != 3700 || len(policy.ICEServers) != 3 {
		t.Fatalf("invalid remote policy: %+v", policy)
	}
	credential := policy.ICEServers[len(policy.ICEServers)-1]
	if len(credential.URLs) != 1 || credential.URLs[0] != "turn:relay.example:8254?transport=udp" || !m.alive(credential.Username) {
		t.Fatalf("remote TURN credential not active: %+v", credential)
	}
	b.Lock()
	delete(b.active, deviceID)
	b.Unlock()
	if m.alive(credential.Username) {
		t.Fatal("ended remote session retained TURN authorization")
	}
}
