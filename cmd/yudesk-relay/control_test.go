package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestManagementAuthenticatesDeviceAndDeliversStop(t *testing.T) {
	store, err := account.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b := &broker{accounts: store, deviceLicenses: true, devices: map[string]waiting{}, active: map[string]activeSession{}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	handled := make(chan struct{}, 2)
	go func() {
		for i := 0; i < 2; i++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { b.handle(c); handled <- struct{}{} }()
		}
	}()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	_, wrongKey, _ := ed25519.GenerateKey(rand.Reader)
	id := secureconn.DeviceID(pub)
	hello := relay.Hello{ID: id, PublicKey: pub, Name: "local test device", PIN: "260831"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = relay.WatchDevice(ctx, ln.Addr().String(), relay.DialOptions{}, hello, wrongKey, func(relay.ControlMessage) { t.Error("unauthenticated status delivered") })
	if relay.RejectionCode(err) != "DENIED" {
		t.Fatalf("forged identity accepted: %v", err)
	}
	<-handled
	devices, _ := store.ListLicensedDevices(10)
	if len(devices) != 0 {
		t.Fatal("forged identity registered a device")
	}
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- relay.WatchDevice(ctx, ln.Addr().String(), relay.DialOptions{}, hello, key, func(m relay.ControlMessage) {
			select {
			case ready <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("management did not become ready")
	}
	devices, err = store.ListLicensedDevices(10)
	if err != nil || len(devices) != 1 || devices[0].PairingPIN != "260831" {
		t.Fatalf("authenticated pairing PIN was not stored: devices=%+v err=%v", devices, err)
	}
	if !b.terminateDevice("device:"+id, id, "test admin disconnect") {
		t.Fatal("management connection was not found")
	}
	select {
	case err := <-done:
		if relay.RejectionCode(err) != "STOP" {
			t.Fatalf("stop not delivered: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stop did not reach device")
	}
	<-handled
	b.Lock()
	defer b.Unlock()
	if b.controls[id] != nil {
		t.Fatal("closed control connection remained online")
	}
}
