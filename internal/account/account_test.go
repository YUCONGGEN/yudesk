package account

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestRegisterLogin(t *testing.T) {
	s, err := Open(t.TempDir() + "/accounts.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Register("alice", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login("alice", "wrong password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	token, err := s.Login("alice", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Valid(token) {
		t.Fatal("session token rejected")
	}
	if err := s.Logout(token); err != nil {
		t.Fatal(err)
	}
	if s.Valid(token) {
		t.Fatal("revoked session token accepted")
	}
}

func TestActivationAndDeviceOwnership(t *testing.T) {
	s, err := Open(t.TempDir() + "/accounts.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Register("alice", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	key, err := s.GenerateActivationKey(30*24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	expiry, err := s.RedeemActivationKey("alice", key)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasActiveLicense("alice") || expiry.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatal("license was not activated")
	}
	if _, err := s.RedeemActivationKey("alice", key); err == nil {
		t.Fatal("activation key was reused")
	}
	unused, err := s.GenerateActivationKey(24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeActivationKey(unused); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemActivationKey("alice", unused); err == nil {
		t.Fatal("revoked activation key was accepted")
	}
	keys, err := s.ListActivationKeys(10)
	if err != nil || len(keys) != 2 {
		t.Fatalf("list activation keys: count=%d err=%v", len(keys), err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := secureconn.DeviceID(publicKey)
	if err := s.BindDevice("alice", id, publicKey, "office"); err != nil {
		t.Fatal(err)
	}
	if !s.OwnsDevice("alice", id) {
		t.Fatal("bound device is not owned")
	}
}

func TestMigratesLegacyAccountTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE accounts (username TEXT PRIMARY KEY,password_hash BLOB NOT NULL,salt BLOB NOT NULL,created_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Register("migrated", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if s.HasActiveLicense("migrated") {
		t.Fatal("new account unexpectedly activated")
	}
}

func TestStandaloneDeviceActivationAndGrant(t *testing.T) {
	s, err := Open(t.TempDir() + "/devices.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := secureconn.DeviceID(publicKey)
	if err := s.RegisterLicensedDeviceNamed(deviceID, publicKey, "测试电脑"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLicensedDevicePIN(deviceID, "260831"); err != nil {
		t.Fatal(err)
	}
	if s.HasActiveDeviceLicense(deviceID) {
		t.Fatal("new device unexpectedly licensed")
	}
	key, err := s.GenerateActivationKey(24*time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	expires, err := s.RedeemDeviceActivationKey(deviceID, key)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasActiveDeviceLicense(deviceID) || expires.Before(time.Now().Add(23*time.Hour)) {
		t.Fatal("device activation did not set the license")
	}
	if _, err := s.RedeemDeviceActivationKey(deviceID, key); err == nil {
		t.Fatal("device activation key was reused")
	}
	extended, err := s.GrantDeviceLicense(deviceID, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !extended.After(expires.Add(47 * time.Hour)) {
		t.Fatal("direct grant did not extend the current license")
	}
	devices, err := s.ListLicensedDevices(10)
	if err != nil || len(devices) != 1 || devices[0].ID != deviceID || devices[0].Name != "测试电脑" || devices[0].PairingPIN != "260831" {
		t.Fatalf("unexpected device list: %+v err=%v", devices, err)
	}
	if err := s.RenameLicensedDevice(deviceID, "前台主机"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeLicensedDevice(deviceID); err != nil {
		t.Fatal(err)
	}
	if s.HasActiveDeviceLicense(deviceID) {
		t.Fatal("revoked device remained active")
	}
	if _, err := s.GrantDeviceLicense(deviceID, time.Hour); err != nil {
		t.Fatalf("admin grant did not restore a revoked device: %v", err)
	}
	if !s.HasActiveDeviceLicense(deviceID) {
		t.Fatal("restored device is not active")
	}
	if enabled, err := s.DeviceAutoActivation(); err != nil || enabled {
		t.Fatalf("automatic activation should default to disabled: enabled=%v err=%v", enabled, err)
	}
	if err := s.SetDeviceAutoActivation(true); err != nil {
		t.Fatal(err)
	}
	if enabled, err := s.DeviceAutoActivation(); err != nil || !enabled {
		t.Fatalf("automatic activation setting was not persisted: enabled=%v err=%v", enabled, err)
	}
	autoPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	autoDeviceID := secureconn.DeviceID(autoPublicKey)
	if err := s.RegisterLicensedDevice(autoDeviceID, autoPublicKey); err != nil {
		t.Fatal(err)
	}
	granted, err := s.EnsurePermanentDeviceLicense(autoDeviceID)
	if err != nil || !granted {
		t.Fatalf("device did not receive a permanent license: granted=%v err=%v", granted, err)
	}
	permanentExpiry, err := s.DeviceLicenseExpiry(autoDeviceID)
	if err != nil || !IsPermanentDeviceLicense(permanentExpiry) {
		t.Fatalf("unexpected permanent license expiry: %v err=%v", permanentExpiry, err)
	}
	if err := s.RevokeLicensedDevice(autoDeviceID); err != nil {
		t.Fatal(err)
	}
	if granted, err := s.EnsurePermanentDeviceLicense(autoDeviceID); err != nil || granted {
		t.Fatalf("automatic activation bypassed a disabled device: granted=%v err=%v", granted, err)
	}
	if err := s.UnrevokeLicensedDevice(autoDeviceID); err != nil {
		t.Fatal(err)
	}
	if !s.HasActiveDeviceLicense(autoDeviceID) {
		t.Fatal("unrevoked permanent device did not become active")
	}
	if err := s.SetDeviceAutoActivation(false); err != nil {
		t.Fatal(err)
	}
	unused, err := s.GenerateActivationKey(time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := s.ListActivationKeys(10)
	if err != nil {
		t.Fatal(err)
	}
	wantedFingerprint := ""
	for _, candidate := range keys {
		if candidate.RedeemedBy == "" && candidate.RevokedAt.IsZero() {
			wantedFingerprint = candidate.Fingerprint
			break
		}
	}
	if wantedFingerprint == "" {
		t.Fatal("unused activation key fingerprint was not listed")
	}
	if err := s.RevokeActivationKeyByFingerprint(wantedFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemDeviceActivationKey(deviceID, unused); err == nil {
		t.Fatal("activation key revoked by fingerprint was accepted")
	}
	s.Audit(AuditEntry{Action: "admin_test", DeviceID: deviceID})
	audits, err := s.RecentAuditAll(10)
	if err != nil || len(audits) != 1 || audits[0].Action != "admin_test" {
		t.Fatalf("unexpected audit list: %+v err=%v", audits, err)
	}
	if err := s.DeleteLicensedDevice(deviceID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLicensedDevice(autoDeviceID); err != nil {
		t.Fatal(err)
	}
	devices, err = s.ListLicensedDevices(10)
	if err != nil || len(devices) != 0 {
		t.Fatalf("device was not deleted: %+v err=%v", devices, err)
	}
}
