package identity

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedPINMigrationKeepsPrivateIdentity(t *testing.T) {
	dir := t.TempDir()
	first, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.PIN) != 6 {
		t.Fatal("default PIN not six digits")
	}
	if err := os.WriteFile(filepath.Join(dir, "pairing-pin"), []byte("12345678"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.PIN) != 6 || first.ID != second.ID || !bytes.Equal(first.PrivateKey, second.PrivateKey) {
		t.Fatal("PIN migration changed identity")
	}
	third, err := Load(dir, "")
	if err != nil || third.PIN != second.PIN {
		t.Fatal("PIN was not persisted")
	}
}

func TestPINRotationPersistsAndPreservesIdentity(t *testing.T) {
	dir := t.TempDir()
	initial, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		pin, err := RotatePIN(dir, initial.PIN)
		if err != nil || pin == initial.PIN || len(pin) != 6 {
			t.Fatal("rotation failed", err)
		}
		loaded, err := Load(dir, "")
		if err != nil || loaded.PIN != pin || loaded.ID != initial.ID || !bytes.Equal(loaded.PrivateKey, initial.PrivateKey) {
			t.Fatal("identity was changed", err)
		}
		initial = loaded
	}
	if _, err := RotatePIN(filepath.Join(dir, "missing"), initial.PIN); err == nil {
		t.Fatal("failed write accepted")
	}
	loaded, err := Load(dir, "")
	if err != nil || loaded.PIN != initial.PIN {
		t.Fatal("failed rotation lost PIN", err)
	}
}
