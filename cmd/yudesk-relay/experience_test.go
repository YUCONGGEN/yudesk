package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativeViewerExperience(t *testing.T) {
	f := newLifecycleFixture(t)
	if runtime.GOOS != "windows" && os.Getenv("YUDESK_TEST_DESKTOP") != "1" {
		t.Skip("requires a native desktop")
	}
	dir, id := f.prepareAgent()
	a := f.agent(dir)
	f.waitOnline(id.ID)
	f.admin("grant", id.ID)
	f.waitPairing(id.ID)
	viewerDir := filepath.Join(f.root, "viewer")
	if err := os.MkdirAll(viewerDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(viewerDir, "stream-options.json"), []byte(`{"fps":30,"quality":60,"mode":"fixed","maxWidth":960,"maxMbps":0,"saveIdle":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	v := f.connectViewer(viewerDir, id)
	base := f.page(filepath.Join(viewerDir, "viewer-session.url"))
	u, _ := url.Parse(base)
	client := &http.Client{Timeout: 5 * time.Second}
	var stats struct {
		ProbeOK bool    `json:"probeOK"`
		RTT     float64 `json:"rttMs"`
		FPS     float64 `json:"fps"`
		RX      uint64  `json:"receivedBytes"`
		Width   int
		Quality int `json:"quality"`
	}
	u.Path = "/api/stats"
	eventually(t, "measured RTT, FPS, bandwidth and resized desktop", func() bool {
		r, err := client.Get(u.String())
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return json.NewDecoder(r.Body).Decode(&stats) == nil && stats.ProbeOK && stats.RX > 0 && stats.FPS > 0 && stats.Width == 960 && stats.Quality == 60
	})
	t.Logf("low-latency target=30, actual=%.1f FPS, RTT=%.1f ms, received=%d bytes", stats.FPS, stats.RTT, stats.RX)
	if script := os.Getenv("YUDESK_BROWSER_TEST_SCRIPT"); script != "" {
		result, err := exec.Command("node", script, base).CombinedOutput()
		if err != nil {
			t.Fatalf("browser regression: %v %s", err, result)
		}
		t.Logf("browser regression: %s", result)
	}
	if stats.FPS < 12 || stats.FPS > 36 {
		t.Fatalf("fixed stream overshot target: %.1f", stats.FPS)
	}
	u.Path = "/api/stream/options"
	for i := 0; i < 4; i++ {
		payload := []byte(`{"fps":15,"quality":65,"mode":"fixed","maxWidth":1280,"maxMbps":8,"saveIdle":false}`)
		r, err := client.Post(u.String(), "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatal("options rejected")
		}
	}
	u.Path = "/api/stats"
	eventually(t, "stream survived repeated live option changes", func() bool {
		r, err := client.Get(u.String())
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return json.NewDecoder(r.Body).Decode(&stats) == nil && stats.Width == 1280 && stats.Quality == 65
	})
	data, err := os.ReadFile(filepath.Join(viewerDir, "connection-history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), id.ID) || strings.Contains(string(data), id.PIN) {
		t.Fatalf("invalid history: %s", data)
	}
	f.post(base, "/api/exit", nil)
	v.exited(t)
	v = f.viewer(viewerDir)
	launcher := f.page(filepath.Join(viewerDir, "viewer-launcher.url"))
	r, err := client.Get(launcher)
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(html), "再次连接") || !strings.Contains(string(html), id.ID) {
		t.Fatal("history missing after restart")
	}
	f.post(launcher, "/exit", nil)
	v.exited(t)
	f.admin("disconnect", id.ID)
	a.exited(t)
}
