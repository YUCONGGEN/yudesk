package account

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestLicensedDeviceHeartbeatMonotonicAndAdminChangesDoNotTouchPresence(t *testing.T) {
	s, err := Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key, _, _ := ed25519.GenerateKey(rand.Reader)
	id := secureconn.DeviceID(key)
	if err := s.RegisterLicensedDevice(id, key); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Unix()
	if _, err := s.db.Exec(`UPDATE licensed_devices SET last_seen=? WHERE device_id=?`, old, id); err != nil {
		t.Fatal(err)
	}
	activation, err := s.GenerateActivationKey(time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemDeviceActivationKey(id, activation); err != nil {
		t.Fatal(err)
	}
	check := func(want int64) {
		t.Helper()
		devices, err := s.ListLicensedDevices(10)
		if err != nil || len(devices) != 1 || devices[0].LastSeen.Unix() != want {
			t.Fatalf("unexpected last seen: %+v, %v", devices, err)
		}
	}
	check(old)
	seen := time.Now().Truncate(time.Second)
	if err := s.TouchLicensedDevice(id, seen); err != nil {
		t.Fatal(err)
	}
	check(seen.Unix())
	if err := s.TouchLicensedDevice(id, seen.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	check(seen.Unix())
	if err := s.DeleteLicensedDevice(id); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchLicensedDevice(id, seen.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	devices, err := s.ListLicensedDevices(10)
	if err != nil || len(devices) != 0 {
		t.Fatal("late heartbeat recreated removed device", err)
	}
}
