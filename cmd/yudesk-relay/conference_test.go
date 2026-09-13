package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
	"github.com/yudesk/yudesk/internal/security"
)

func TestConferenceSignalingJoinsTransfersHostAndEnds(t *testing.T) {
	store, err := account.Open(filepath.Join(t.TempDir(), "conference.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hostPublic, hostPrivate, _ := ed25519.GenerateKey(rand.Reader)
	hostDevice := secureconn.DeviceID(hostPublic)
	if err := store.RegisterLicensedDevice(hostDevice, hostPublic); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantDeviceLicense(hostDevice, time.Hour); err != nil {
		t.Fatal(err)
	}
	control := &deviceControl{}
	control.lastSeen.Store(time.Now().Unix())
	b := &broker{accounts: store, deviceLicenses: true, devices: map[string]waiting{}, active: map[string]activeSession{}, controls: map[string]*deviceControl{hostDevice: control}}
	certificate, fingerprint, err := security.LoadOrCreateServerConfig(filepath.Join(t.TempDir(), "cert"), filepath.Join(t.TempDir(), "key"), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go b.handle(connection)
		}
	}()
	options := relay.DialOptions{TLS: true, Fingerprint: fingerprint}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	meeting, err := relay.OpenMeeting(ctx, listener.Addr().String(), options, hostDevice, hostPrivate)
	if err != nil {
		t.Fatal(err)
	}

	host, err := relay.DialConference(ctx, listener.Addr().String(), options, hostDevice, hostPrivate, meeting.Code, "主持人", true)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	hostDecoder, hostEncoder := json.NewDecoder(host), json.NewEncoder(host)
	hostWelcome := readConferenceMessage(t, host, hostDecoder)
	if hostWelcome.Type != "welcome" || hostWelcome.ID == "" || hostWelcome.Host != hostWelcome.ID || len(hostWelcome.Peers) != 0 {
		t.Fatalf("unexpected host welcome: %+v", hostWelcome)
	}

	guestPublic, guestPrivate, _ := ed25519.GenerateKey(rand.Reader)
	guestDevice := secureconn.DeviceID(guestPublic)
	if err := store.RegisterLicensedDevice(guestDevice, guestPublic); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantDeviceLicense(guestDevice, time.Hour); err != nil {
		t.Fatal(err)
	}
	guest, err := relay.DialConference(ctx, listener.Addr().String(), options, guestDevice, guestPrivate, meeting.Code, "参会者", false)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()
	guestDecoder, guestEncoder := json.NewDecoder(guest), json.NewEncoder(guest)
	joined := readConferenceMessage(t, host, hostDecoder)
	guestWelcome := readConferenceMessage(t, guest, guestDecoder)
	if joined.Type != "peer-joined" || joined.Name != "参会者" || guestWelcome.Type != "welcome" || len(guestWelcome.Peers) != 1 || !guestWelcome.Peers[0].Host {
		t.Fatalf("unexpected join state: joined=%+v welcome=%+v", joined, guestWelcome)
	}

	latePublic, latePrivate, _ := ed25519.GenerateKey(rand.Reader)
	lateDevice := secureconn.DeviceID(latePublic)
	if err := store.RegisterLicensedDevice(lateDevice, latePublic); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantDeviceLicense(lateDevice, time.Hour); err != nil {
		t.Fatal(err)
	}
	late, err := relay.DialConference(ctx, listener.Addr().String(), options, lateDevice, latePrivate, meeting.Code, "短暂参会者", false)
	if err != nil {
		t.Fatal(err)
	}
	lateDecoder := json.NewDecoder(late)
	lateJoinedHost := readConferenceMessage(t, host, hostDecoder)
	lateJoinedGuest := readConferenceMessage(t, guest, guestDecoder)
	lateWelcome := readConferenceMessage(t, late, lateDecoder)
	if lateJoinedHost.Type != "peer-joined" || lateJoinedGuest.Type != "peer-joined" || lateWelcome.Type != "welcome" {
		t.Fatalf("unexpected temporary participant join: host=%+v guest=%+v welcome=%+v", lateJoinedHost, lateJoinedGuest, lateWelcome)
	}
	_ = late.Close()
	if left := readConferenceMessage(t, host, hostDecoder); left.Type != "peer-left" || left.ID != lateWelcome.ID {
		t.Fatalf("temporary participant did not leave: %+v", left)
	}
	if left := readConferenceMessage(t, guest, guestDecoder); left.Type != "peer-left" || left.ID != lateWelcome.ID {
		t.Fatalf("temporary participant did not leave for guest: %+v", left)
	}
	// ICE generated immediately before the peer-left notification may arrive
	// afterwards. It must be ignored without dropping the remaining sender.
	if err := hostEncoder.Encode(relay.ConferenceMessage{Type: "signal", To: lateWelcome.ID, Signal: "candidate", Candidate: `{"candidate":"candidate:1 1 UDP 1 127.0.0.1 9 typ host","sdpMid":"0","sdpMLineIndex":0}`}); err != nil {
		t.Fatal(err)
	}
	if err := hostEncoder.Encode(relay.ConferenceMessage{Type: "ping"}); err != nil {
		t.Fatal(err)
	}
	if pong := readConferenceMessage(t, host, hostDecoder); pong.Type != "pong" {
		t.Fatalf("stale peer signal disconnected sender: %+v", pong)
	}

	removedPublic, removedPrivate, _ := ed25519.GenerateKey(rand.Reader)
	removedDevice := secureconn.DeviceID(removedPublic)
	if err := store.RegisterLicensedDevice(removedDevice, removedPublic); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantDeviceLicense(removedDevice, time.Hour); err != nil {
		t.Fatal(err)
	}
	removed, err := relay.DialConference(ctx, listener.Addr().String(), options, removedDevice, removedPrivate, meeting.Code, "待移出成员", false)
	if err != nil {
		t.Fatal(err)
	}
	removedDecoder := json.NewDecoder(removed)
	removedJoinedHost := readConferenceMessage(t, host, hostDecoder)
	removedJoinedGuest := readConferenceMessage(t, guest, guestDecoder)
	removedWelcome := readConferenceMessage(t, removed, removedDecoder)
	if removedJoinedHost.Type != "peer-joined" || removedJoinedHost.JoinedAt == 0 || removedJoinedGuest.Type != "peer-joined" || removedWelcome.JoinedAt == 0 {
		t.Fatalf("removed participant did not join with metadata: host=%+v guest=%+v welcome=%+v", removedJoinedHost, removedJoinedGuest, removedWelcome)
	}
	if err := hostEncoder.Encode(relay.ConferenceMessage{Type: "kick", To: removedWelcome.ID}); err != nil {
		t.Fatal(err)
	}
	if kicked := readConferenceMessage(t, removed, removedDecoder); kicked.Type != "removed" || kicked.Message == "" {
		t.Fatalf("participant did not receive removal reason: %+v", kicked)
	}
	if left := readConferenceMessage(t, host, hostDecoder); left.Type != "peer-left" || left.ID != removedWelcome.ID {
		t.Fatalf("host did not see removed participant leave: %+v", left)
	}
	if left := readConferenceMessage(t, guest, guestDecoder); left.Type != "peer-left" || left.ID != removedWelcome.ID {
		t.Fatalf("guest did not see removed participant leave: %+v", left)
	}
	_ = removed.Close()
	if _, err := relay.DialConference(ctx, listener.Addr().String(), options, removedDevice, removedPrivate, meeting.Code, "尝试重新加入", false); relay.RejectionCode(err) != "DENIED" {
		t.Fatalf("removed device rejoined the same meeting: %v", err)
	}

	if err := guestEncoder.Encode(relay.ConferenceMessage{Type: "signal", To: hostWelcome.ID, Signal: "offer", SDP: "v=0\r\n"}); err != nil {
		t.Fatal(err)
	}
	offer := readConferenceMessage(t, host, hostDecoder)
	if offer.Type != "signal" || offer.From != guestWelcome.ID || offer.Signal != "offer" || offer.SDP != "v=0\r\n" {
		t.Fatalf("signal was not routed: %+v", offer)
	}
	if err := hostEncoder.Encode(relay.ConferenceMessage{Type: "state", Microphone: true, Camera: true, Screen: true}); err != nil {
		t.Fatal(err)
	}
	selfState := readConferenceMessage(t, host, hostDecoder)
	guestState := readConferenceMessage(t, guest, guestDecoder)
	if selfState.Type != "state" || guestState.Type != "state" || guestState.ID != hostWelcome.ID || !guestState.Microphone || !guestState.Camera || !guestState.Screen {
		t.Fatalf("state was not broadcast: self=%+v guest=%+v", selfState, guestState)
	}
	if err := guestEncoder.Encode(relay.ConferenceMessage{Type: "state", Microphone: true, Screen: true, Recording: true}); err != nil {
		t.Fatal(err)
	}
	guestSelfState := readConferenceMessage(t, guest, guestDecoder)
	hostGuestState := readConferenceMessage(t, host, hostDecoder)
	if guestSelfState.Type != "state" || hostGuestState.ID != guestWelcome.ID || !hostGuestState.Microphone || hostGuestState.Screen || hostGuestState.Recording {
		t.Fatalf("participant asserted host-only state: self=%+v host=%+v", guestSelfState, hostGuestState)
	}
	if err := hostEncoder.Encode(relay.ConferenceMessage{Type: "transfer", To: guestWelcome.ID}); err != nil {
		t.Fatal(err)
	}
	hostReset := readConferenceMessage(t, host, hostDecoder)
	guestReset := readConferenceMessage(t, guest, guestDecoder)
	if hostReset.Type != "state" || guestReset.Type != "state" || hostReset.ID != hostWelcome.ID || hostReset.Screen || hostReset.Recording {
		t.Fatalf("old host media privileges were not reset: host=%+v guest=%+v", hostReset, guestReset)
	}
	hostChanged := readConferenceMessage(t, host, hostDecoder)
	guestChanged := readConferenceMessage(t, guest, guestDecoder)
	if hostChanged.Type != "host" || guestChanged.Type != "host" || hostChanged.Host != guestWelcome.ID || guestChanged.Host != guestWelcome.ID {
		t.Fatalf("host was not transferred: host=%+v guest=%+v", hostChanged, guestChanged)
	}
	if err := guestEncoder.Encode(relay.ConferenceMessage{Type: "end"}); err != nil {
		t.Fatal(err)
	}
	if ended := readConferenceMessage(t, guest, guestDecoder); ended.Type != "ended" {
		t.Fatalf("new host did not end meeting: %+v", ended)
	}
	eventually(t, "ended conference is removed from directory", func() bool {
		checkCtx, checkCancel := context.WithTimeout(context.Background(), time.Second)
		defer checkCancel()
		_, resolveErr := relay.ResolveMeeting(checkCtx, listener.Addr().String(), options, meeting.Code)
		return resolveErr != nil
	})
}

func TestConferenceRejectsForgedHostAndJoinBeforeHost(t *testing.T) {
	store, err := account.Open(filepath.Join(t.TempDir(), "conference-auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hostPublic, hostPrivate, _ := ed25519.GenerateKey(rand.Reader)
	hostDevice := secureconn.DeviceID(hostPublic)
	if err := store.RegisterLicensedDevice(hostDevice, hostPublic); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantDeviceLicense(hostDevice, time.Hour); err != nil {
		t.Fatal(err)
	}
	control := &deviceControl{}
	control.lastSeen.Store(time.Now().Unix())
	b := &broker{accounts: store, deviceLicenses: true, devices: map[string]waiting{}, active: map[string]activeSession{}, controls: map[string]*deviceControl{hostDevice: control}}
	certificate, fingerprint, _ := security.LoadOrCreateServerConfig(filepath.Join(t.TempDir(), "cert"), filepath.Join(t.TempDir(), "key"), "localhost")
	listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go b.handle(connection)
		}
	}()
	options := relay.DialOptions{TLS: true, Fingerprint: fingerprint}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	meeting, err := relay.OpenMeeting(ctx, listener.Addr().String(), options, hostDevice, hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	guestPublic, guestPrivate, _ := ed25519.GenerateKey(rand.Reader)
	guestDevice := secureconn.DeviceID(guestPublic)
	if _, err := relay.DialConference(ctx, listener.Addr().String(), options, guestDevice, guestPrivate, meeting.Code, "提前入会", false); relay.RejectionCode(err) != "BUSY" {
		t.Fatalf("guest joined before host: %v", err)
	}
	if _, err := relay.DialConference(ctx, listener.Addr().String(), options, guestDevice, guestPrivate, meeting.Code, "伪造主持人", true); relay.RejectionCode(err) != "DENIED" {
		t.Fatalf("forged host was accepted: %v", err)
	}
}

func TestConferenceHasNoStaticParticipantLimit(t *testing.T) {
	store, err := account.Open(filepath.Join(t.TempDir(), "conference-unlimited.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hostPublic, hostPrivate, _ := ed25519.GenerateKey(rand.Reader)
	hostDevice := secureconn.DeviceID(hostPublic)
	if err := store.RegisterLicensedDevice(hostDevice, hostPublic); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantDeviceLicense(hostDevice, time.Hour); err != nil {
		t.Fatal(err)
	}
	control := &deviceControl{}
	control.lastSeen.Store(time.Now().Unix())
	b := &broker{accounts: store, deviceLicenses: true, devices: map[string]waiting{}, active: map[string]activeSession{}, controls: map[string]*deviceControl{hostDevice: control}}
	certificate, fingerprint, err := security.LoadOrCreateServerConfig(filepath.Join(t.TempDir(), "cert"), filepath.Join(t.TempDir(), "key"), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", certificate)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go b.handle(connection)
		}
	}()
	options := relay.DialOptions{TLS: true, Fingerprint: fingerprint}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	meeting, err := relay.OpenMeeting(ctx, listener.Addr().String(), options, hostDevice, hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	host, err := relay.DialConference(ctx, listener.Addr().String(), options, hostDevice, hostPrivate, meeting.Code, "主持人", true)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if welcome := readConferenceMessage(t, host, json.NewDecoder(host)); welcome.Type != "welcome" {
		t.Fatalf("unexpected host welcome: %+v", welcome)
	}

	// Nine guests plus the host crosses the former eight-participant product
	// cap. The signaling room now admits every authenticated, licensed device;
	// practical media capacity is governed by endpoint and network resources.
	guests := make([]net.Conn, 0, 9)
	defer func() {
		for _, guest := range guests {
			_ = guest.Close()
		}
	}()
	for index := 0; index < 9; index++ {
		publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
		deviceID := secureconn.DeviceID(publicKey)
		if err := store.RegisterLicensedDevice(deviceID, publicKey); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GrantDeviceLicense(deviceID, time.Hour); err != nil {
			t.Fatal(err)
		}
		guest, dialErr := relay.DialConference(ctx, listener.Addr().String(), options, deviceID, privateKey, meeting.Code, "参会者", false)
		if dialErr != nil {
			t.Fatalf("guest %d was rejected: %v", index+1, dialErr)
		}
		guests = append(guests, guest)
		welcome := readConferenceMessage(t, guest, json.NewDecoder(guest))
		if welcome.Type != "welcome" || len(welcome.Peers) != index+1 {
			t.Fatalf("guest %d received unexpected welcome: %+v", index+1, welcome)
		}
	}
}

func readConferenceMessage(t *testing.T, conn net.Conn, decoder *json.Decoder) relay.ConferenceMessage {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var message relay.ConferenceMessage
	if err := decoder.Decode(&message); err != nil {
		t.Fatal(err)
	}
	return message
}
