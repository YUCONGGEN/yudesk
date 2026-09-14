package viewerapp

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestDashboardVisualPreview is an opt-in, local-only visual QA server. It has
// no agent, remote-control connection, device identity, or system permissions.
func TestDashboardVisualPreview(t *testing.T) {
	if os.Getenv("YUDESK_UI_PREVIEW") != "1" {
		t.Skip("opt-in visual preview")
	}
	mux := http.NewServeMux()
	registerViewerAssets(mux)
	mux.HandleFunc("/assets/dashboard.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(append([]byte(dashboardCSS+"\n"), productTheme...))
	})
	mux.HandleFunc("/assets/conference.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write([]byte(conferenceCSS))
	})
	mux.HandleFunc("/assets/portmap.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write([]byte(portMapCSS))
	})
	mux.HandleFunc("/assets/dashboard.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write([]byte(dashboardJS))
	})
	mux.HandleFunc("/api/local/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "DESKTOP-YUDESK", "code": "883002949", "pin": "607593",
			"receiving": true, "files": true, "fileDirectory": "D:\\YuDesk 接收文件",
			"activeUntil": "永久授权", "online": true, "connected": false, "active": true,
		})
	})
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"devices":[]}`))
	})
	mux.HandleFunc("/api/ui/watch", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := page.Execute(w, map[string]any{"Token": "preview", "Control": true, "Audio": true, "AudioRequested": true, "AudioReason": "系统声音可用", "FPS": 30, "Quality": 72}); err != nil {
				t.Error(err)
			}
			return
		}
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := dashboardPage.Execute(w, map[string]any{"Token": "preview", "Version": "2.0.0", "Build": "visual-preview", "InstallPrompt": true}); err != nil {
			t.Error(err)
		}
	})
	server := &http.Server{Addr: ":9399", Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	t.Cleanup(func() { _ = server.Close() })
	t.Log("YuDesk visual preview: http://127.0.0.1:9399/")
	time.Sleep(10 * time.Minute)
}
