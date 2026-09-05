package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func signedManifest(t *testing.T, privateKey ed25519.PrivateKey, file File) []byte {
	t.Helper()
	manifest := Manifest{Version: "1.2.3", PublishedAt: time.Unix(1, 0).UTC(), Files: []File{file}}
	payload, err := SigningPayload(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestVerifyAndSelect(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("release"))
	data := signedManifest(t, privateKey, File{Component: "yudesk-agent", OS: "windows", Arch: "amd64", URL: "https://example.com/agent.exe", SHA256: hex.EncodeToString(hash[:])})
	manifest, err := Verify(data, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Select(manifest, "yudesk-agent", "windows", "amd64"); err != nil {
		t.Fatal(err)
	}
	data[len(data)-2] ^= 1
	if _, err := Verify(data, publicKey); err == nil {
		t.Fatal("tampered manifest was accepted")
	}
}

func TestInstallChecksHashAndKeepsBackup(t *testing.T) {
	release := []byte("new release")
	hash := sha256.Sum256(release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(release) }))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "yudesk-agent")
	if err := os.WriteFile(destination, []byte("old release"), 0755); err != nil {
		t.Fatal(err)
	}
	backup, err := Install(context.Background(), File{URL: server.URL, SHA256: hex.EncodeToString(hash[:])}, destination, true)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(destination)
	old, _ := os.ReadFile(backup)
	if string(got) != string(release) || string(old) != "old release" {
		t.Fatalf("unexpected installed or backup data: %q / %q", got, old)
	}
}
