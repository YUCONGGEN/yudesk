package viewerapp

import (
	"net/http"
	"os"
	"testing"
	"time"
)

func TestNativeManagedWindowHideReopenAndClose(t *testing.T) {
	browser := os.Getenv("YUDESK_TEST_BROWSER")
	if browser == "" {
		t.Skip("opt-in isolated native Chromium, no visible windows or input")
	}
	h, err := newViewerHost("127.0.0.1:0", "window-test-token")
	if err != nil {
		t.Fatal(err)
	}
	h.window.profile = t.TempDir()
	h.window.candidates = []string{browser}
	h.window.headless = true
	defer h.Close()
	waitAttached := func() {
		t.Helper()
		until := time.Now().Add(4 * time.Second)
		for !h.tracker.Attached() && time.Now().Before(until) {
			time.Sleep(20 * time.Millisecond)
		}
		if !h.tracker.Attached() {
			t.Fatal("window did not attach lifecycle watcher")
		}
	}
	l, _ := listenViewerPage("", h)
	attachViewerPage(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<title>YuDesk test</title><script>fetch('/api/ui/watch?access_token=window-test-token')</script>`))
	}))
	if err := h.window.Show(); err != nil {
		t.Fatal(err)
	}
	waitAttached()
	getTarget := func() string {
		t.Helper()
		c, err := h.window.connection()
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		id, err := h.window.target(c)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := getTarget()
	if err := h.window.Show(); err != nil {
		t.Fatal(err)
	}
	if got := getTarget(); got != id {
		t.Fatal("duplicate launch created another window")
	}
	h.tracker.Suspend()
	if err := h.window.Hide(); err != nil {
		t.Fatal(err)
	}
	if c, err := h.window.connection(); err == nil {
		c.Close()
		t.Fatal("hidden renderer remained open")
	}
	if h.ctx.Err() != nil {
		t.Fatal("hide killed app")
	}
	if err := h.window.Show(); err != nil {
		t.Fatal(err)
	}
	waitAttached()
	c, err := h.window.connection()
	if err != nil {
		t.Fatal(err)
	}
	id = getTarget()
	if err := windowCommand(c, "Target.closeTarget", map[string]any{"targetId": id}, nil); err != nil {
		t.Fatal(err)
	}
	c.Close()
	select {
	case <-h.ctx.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("home X did not exit app")
	}
}
