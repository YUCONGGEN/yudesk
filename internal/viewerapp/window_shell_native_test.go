//go:build linux || darwin

package viewerapp

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Explicit opt-in, isolated virtual display only. Place the production native
// helper beside this Go test binary; no real YuDesk data or OS input is used.
func TestNativeUnixShellLifecycle(t *testing.T) {
	if os.Getenv("YUDESK_TEST_VIRTUAL_DISPLAY") != "1" {
		t.Skip("requires an isolated virtual display and packaged native helper")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(exe), "yudesk-window")); err != nil {
		t.Fatal(err)
	}
	h, err := newViewerHost("127.0.0.1:0", "native-shell-fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	l, _ := listenViewerPage("", h)
	attachViewerPage(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>YuDesk isolated native test</title><body><h1 data-window-drag>YuDesk</h1><p>Local fixture only</p><script>%s;fetch('/api/ui/watch?access_token=native-shell-fixture');</script></body>`, compatJS)
	}))
	if err = h.showWindow(); err != nil {
		t.Fatal(err)
	}
	first := h.window.shell
	if first == nil || !h.window.managed.Load() {
		t.Fatal("native shell not managed")
	}
	for range 3 {
		if err = h.hideWindow(); err != nil {
			t.Fatal(err)
		}
		if err = h.showWindow(); err != nil {
			t.Fatal(err)
		}
		if err = h.window.Minimize(); err != nil {
			t.Fatal(err)
		}
		if err = h.showWindow(); err != nil {
			t.Fatal(err)
		}
		if h.window.shell != first || h.ctx.Err() != nil {
			t.Fatal("window action replaced renderer or ended app")
		}
	}
	_ = first.cmd.Process.Kill()
	select {
	case <-first.done:
	case <-time.After(4 * time.Second):
		t.Fatal("crashed helper not reaped")
	}
	if h.ctx.Err() != nil {
		t.Fatal("helper crash ended application")
	}
	if err = h.showWindow(); err != nil {
		t.Fatal(err)
	}
	if h.window.shell == first {
		t.Fatal("failed helper was not replaced")
	}
	last := h.window.shell
	h.Close()
	select {
	case <-last.done:
	case <-time.After(4 * time.Second):
		t.Fatal("app exit left native helper")
	}
}
