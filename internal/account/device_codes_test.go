package account

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestDeviceCodeStableUniqueAndPreservesLicense(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codes.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var firstID, firstCode string
	for i := 0; i < 25; i++ {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		id := secureconn.DeviceID(pub)
		if err := s.RegisterLicensedDeviceNamed(id, pub, "device"); err != nil {
			t.Fatal(err)
		}
		code, err := s.EnsureDeviceCode(id)
		if err != nil || !ValidDeviceCode(code) || seen[code] {
			t.Fatal("invalid or duplicate code", err)
		}
		seen[code] = true
		again, err := s.EnsureDeviceCode(id)
		if err != nil || again != code {
			t.Fatal("code changed")
		}
		got, err := s.ResolveDeviceCode(code)
		if err != nil || got != id {
			t.Fatal("resolution mismatch")
		}
		if i == 0 {
			firstID, firstCode = id, code
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.ResolveDeviceCode(firstCode)
	if err != nil || got != firstID {
		t.Fatal("code not persisted")
	}
	if err := s.DeleteLicensedDevice(firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveDeviceCode(firstCode); err == nil {
		t.Fatal("deleted code resolves")
	}
	var reserved string
	if err := s.db.QueryRow(`SELECT code FROM device_codes WHERE device_id=?`, firstID).Scan(&reserved); err != nil || reserved != firstCode {
		t.Fatal("deleted code was recycled")
	}
}

func TestCodeBackfillLeavesExistingDeviceDataUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	id := secureconn.DeviceID(pub)
	if err := s.RegisterLicensedDeviceNamed(id, pub, "existing device"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLicensedDevicePIN(id, "123456"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	devices, err := s.ListLicensedDevices(10)
	if err != nil || len(devices) != 1 || devices[0].ID != id || devices[0].Name != "existing device" || devices[0].PairingPIN != "123456" || !ValidDeviceCode(devices[0].Code) {
		t.Fatal("backfill changed device or did not add alias", err)
	}
}
