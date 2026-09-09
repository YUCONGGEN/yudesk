package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"path/filepath"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
	"github.com/yudesk/yudesk/internal/security"
)

func TestTemporaryMeetingDirectoryAuthenticatesOpensResolvesAndCloses(t *testing.T) {
	store, err := account.Open(filepath.Join(t.TempDir(), "meeting.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := secureconn.DeviceID(publicKey)
	if err := store.RegisterLicensedDevice(deviceID, publicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantDeviceLicense(deviceID, time.Hour); err != nil {
		t.Fatal(err)
	}
	deviceCode, err := store.EnsureDeviceCode(deviceID)
	if err != nil {
		t.Fatal(err)
	}
	control := &deviceControl{}
	control.lastSeen.Store(time.Now().Unix())
	control.pin.Store("")
	b := &broker{accounts: store, deviceLicenses: true, devices: map[string]waiting{}, active: map[string]activeSession{}, controls: map[string]*deviceControl{deviceID: control}}

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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	room, err := relay.OpenMeeting(ctx, listener.Addr().String(), options, deviceID, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if !room.Active || !relay.IsMeetingCode(room.Code) || room.Code == deviceCode || !room.ExpiresAt.After(time.Now()) {
		t.Fatalf("invalid meeting room: %+v deviceCode=%s", room, deviceCode)
	}
	resolved, err := relay.ResolveMeeting(ctx, listener.Addr().String(), options, room.Code)
	if err != nil || resolved.ID != deviceID || resolved.Code != room.Code {
		t.Fatalf("meeting resolution failed: %+v %v", resolved, err)
	}
	if err := relay.CloseMeeting(ctx, listener.Addr().String(), options, deviceID, privateKey); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.ResolveMeeting(ctx, listener.Addr().String(), options, room.Code); err == nil {
		t.Fatal("closed meeting remained resolvable")
	}

	// A copied public key is insufficient: the signer must possess the device's
	// private identity key.
	_, forgedKey, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := relay.OpenMeeting(ctx, listener.Addr().String(), options, deviceID, forgedKey); err == nil {
		t.Fatal("forged meeting host was accepted")
	}
}
