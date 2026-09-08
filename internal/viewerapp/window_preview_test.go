package viewerapp

import (
	"net/http"
	"os"
	"testing"
	"time"
)

// Opt-in visual check of our own isolated window only. No desktop capture or
// real input injection; the fixture has no remote connection or device identity.
func TestNativeWindowChromePreview(t *testing.T) {
	if os.Getenv("YUDESK_WINDOW_PREVIEW") != "1" {
		t.Skip("opt-in visible window")
	}
	h, err := newViewerHost("127.0.0.1:0", "window-preview")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.window.profile = t.TempDir()
	h.window.candidates = []string{os.Getenv("YUDESK_TEST_BROWSER")}
	l, _ := listenViewerPage("", h)
	attachViewerPage(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/assets/dashboard.css" {
			w.Header().Set("Content-Type", "text/css")
			w.Write([]byte(dashboardCSS))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		dashboardPage.Execute(w, map[string]any{"Token": "window-preview", "Version": "2.0.0"})
	}))
	tray, err := newAppTray(func() { _ = h.window.Show() }, h.cancel)
	if err != nil {
		t.Fatal(err)
	}
	h.tray = tray
	if err := h.window.Show(); err != nil {
		t.Fatal(err)
	}
	t.Log("local-only window and tray preview ready")
	select {
	case <-h.ctx.Done():
	case <-time.After(90 * time.Second):
	}
}
