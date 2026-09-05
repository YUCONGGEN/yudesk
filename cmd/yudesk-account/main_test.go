package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPinnedHTTPSClient(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	sum := sha256.Sum256(server.Certificate().Raw)
	fingerprint := strings.ToUpper(hex.EncodeToString(sum[:]))
	client, err := newHTTPClient(server.URL, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doJSON(client, server.URL, http.MethodGet, "/healthz", "", nil); err != nil {
		t.Fatalf("correct certificate pin was rejected: %v", err)
	}

	wrong := strings.Repeat("0", sha256.Size*2)
	client, err = newHTTPClient(server.URL, wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doJSON(client, server.URL, http.MethodGet, "/healthz", "", nil); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("wrong certificate pin was not rejected: %v", err)
	}
}

func TestPinnedHTTPSClientValidation(t *testing.T) {
	if _, err := newHTTPClient("http://example.com", strings.Repeat("0", sha256.Size*2)); err == nil {
		t.Fatal("fingerprint was accepted for plain HTTP")
	}
	if _, err := newHTTPClient("https://example.com", "not-a-fingerprint"); err == nil {
		t.Fatal("malformed fingerprint was accepted")
	}
}
