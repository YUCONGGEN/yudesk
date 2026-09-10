package relay

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestConferenceHandshakePreservesCoalescedWelcome(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := secureconn.DeviceID(publicKey)
	client, server := net.Pipe()
	defer server.Close()
	serverErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(clientSideServer{Conn: server})
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			serverErr <- readErr
			return
		}
		var helloLine struct {
			Role      string `json:"role"`
			ID        string `json:"id"`
			Room      string `json:"room"`
			Name      string `json:"name"`
			Action    string `json:"action"`
			PublicKey []byte `json:"publicKey"`
		}
		const prefix = "YU_RELAY/1 "
		if len(line) <= len(prefix) || string(line[:len(prefix)]) != prefix || json.Unmarshal(line[len(prefix):], &helloLine) != nil {
			serverErr <- errInvalidConferenceTestHello
			return
		}
		nonce := make([]byte, 32)
		for i := range nonce {
			nonce[i] = byte(i + 1)
		}
		if json.NewEncoder(server).Encode(ControlMessage{Type: "challenge", Nonce: nonce}) != nil {
			serverErr <- net.ErrClosed
			return
		}
		proofLine, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			serverErr <- readErr
			return
		}
		var proof ControlMessage
		if json.Unmarshal(proofLine, &proof) != nil || !ed25519.Verify(publicKey, ConferenceProof(deviceID, "389721091", "join", "Windows联调", nonce), proof.Signature) {
			serverErr <- errInvalidConferenceTestProof
			return
		}
		// One write deliberately puts the final handshake line and first room
		// message in the same packet/read buffer.
		if _, writeErr := server.Write([]byte("OK\n{\"type\":\"welcome\",\"id\":\"peer-1\"}\n")); writeErr != nil {
			serverErr <- writeErr
			return
		}
		ready, readErr := reader.ReadString('\n')
		if readErr != nil || ready != "READY\n" {
			if readErr != nil {
				serverErr <- readErr
			} else {
				serverErr <- errInvalidConferenceTestReady
			}
			return
		}
		serverErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := authenticateConference(ctx, client, deviceID, privateKey, "389721091", "Windows联调", "join")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var message ConferenceMessage
	if err := json.NewDecoder(conn).Decode(&message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "welcome" || message.ID != "peer-1" {
		t.Fatalf("coalesced welcome was lost: %+v", message)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

type clientSideServer struct{ net.Conn }

var (
	errInvalidConferenceTestHello = &conferenceTestError{"invalid hello"}
	errInvalidConferenceTestProof = &conferenceTestError{"invalid proof"}
	errInvalidConferenceTestReady = &conferenceTestError{"invalid ready"}
)

type conferenceTestError struct{ message string }

func (e *conferenceTestError) Error() string { return e.message }
