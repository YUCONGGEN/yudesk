package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestAdminTimeExplicitBeijingAndConfirmedHeartbeat(t *testing.T) {
	instant := time.Date(2026, 9, 7, 23, 15, 13, 0, time.UTC)
	for _, zone := range []*time.Location{time.UTC, time.FixedZone("PDT", -7*3600), time.FixedZone("JST", 9*3600)} {
		if got := formatAdminTime(instant.In(zone)); got != "2026-09-08 07:15:13" {
			t.Fatal(got)
		}
	}
	if formatAdminTime(time.Time{}) != "-" {
		t.Fatal("zero time displayed")
	}
	control := &deviceControl{}
	control.lastSeen.Store(instant.Unix())
	if !adminLastSeen(instant.Add(-time.Hour), control).Equal(instant) {
		t.Fatal("live heartbeat ignored")
	}
	if !adminLastSeen(instant.Add(time.Second), control).Equal(instant.Add(time.Second)) {
		t.Fatal("timestamp regressed")
	}
}

func TestControlHeartbeatsAdvanceAndPersistOnDisconnect(t *testing.T) {
	s, err := account.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	id := secureconn.DeviceID(public)
	b := &broker{accounts: s, deviceLicenses: true, devices: map[string]waiting{}, active: map[string]activeSession{}}
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		b.handleControl(server, bufio.NewReader(server), relay.Hello{ID: id, PublicKey: public, Name: "心跳测试", PIN: "123456"})
	}()
	defer func() {
		client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("control reader leaked")
		}
	}()
	_ = client.SetDeadline(time.Now().Add(6 * time.Second))
	decoder, encoder := json.NewDecoder(client), json.NewEncoder(client)
	var challenge relay.ControlMessage
	if err := decoder.Decode(&challenge); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(relay.ControlMessage{Type: "proof", Signature: ed25519.Sign(key, relay.ControlProof(id, challenge.Nonce))}); err != nil {
		t.Fatal(err)
	}
	var observed int64
	for index := range 3 {
		var status relay.ControlMessage
		if err := decoder.Decode(&status); err != nil {
			t.Fatal(err)
		}
		if status.Type != "status" {
			t.Fatal(status.Type)
		}
		if index == 2 && status.PIN != "654321" {
			t.Fatal("server did not acknowledge rotated PIN")
		}
		if err := encoder.Encode(relay.ControlMessage{Type: "pong", PIN: "654321"}); err != nil {
			t.Fatal(err)
		}
		observed = time.Now().Unix()
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reader not stopped")
	}
	devices, err := s.ListLicensedDevices(10)
	if err != nil || len(devices) != 1 || devices[0].LastSeen.Unix() < observed-1 {
		t.Fatalf("heartbeat not persisted: %v", err)
	}
	last := devices[0].LastSeen
	if devices[0].PairingPIN != "654321" {
		t.Fatal("server retained stale PIN")
	}
	w := httptest.NewRecorder()
	serveAdminPage(w, httptest.NewRequest("GET", "/admin", nil), b, nil, "", "csrf-test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), formatAdminTime(last)) || !strings.Contains(w.Body.String(), "北京时间（UTC+8）") {
		t.Fatal("admin timestamp missing")
	}
	for _, want := range []string{`class="server-snow"`, "prefers-reduced-motion:reduce", "backdrop-filter:blur", "admin-snow"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("admin visual theme missing %q", want)
		}
	}
	// Reading the admin page cannot make an offline device appear seen now.
	again, _ := s.ListLicensedDevices(10)
	if !again[0].LastSeen.Equal(last) {
		t.Fatal("admin refresh changed last seen")
	}
}
