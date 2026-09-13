package viewerapp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenBrowserRetriesOnlyTransientWakeFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ui/show" || r.Method != http.MethodPost {
			t.Errorf("unexpected wake request %s %s", r.Method, r.URL.Path)
		}
		if calls.Add(1) < 3 {
			http.Error(w, "renderer rebuilding", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := openBrowser(server.URL+"/?access_token=test", false); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("transient wake attempts=%d, want 3", got)
	}

	calls.Store(0)
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer denied.Close()
	if err := openBrowser(denied.URL+"/?access_token=test", false); err == nil || !strings.Contains(err.Error(), fmt.Sprint(http.StatusForbidden)) {
		t.Fatalf("permanent wake failure not returned: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("permanent failure retried %d times", got)
	}
}

func TestColdStartUsesFreshRendererProfile(t *testing.T) {
	root := t.TempDir()
	first, err := freshRendererProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := freshRendererProfile(root)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || filepath.Dir(first) != root || filepath.Dir(second) != root {
		t.Fatalf("renderer profiles must be distinct children: first=%q second=%q", first, second)
	}
	if !strings.HasPrefix(filepath.Base(first), "renderer-") || !strings.HasPrefix(filepath.Base(second), "renderer-") {
		t.Fatalf("unexpected renderer profile names: %q %q", first, second)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("stale renderer profile survived the next cold start: %v", err)
	}
	discardRendererProfile(root, second)
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatalf("renderer profile survived normal close cleanup: %v", err)
	}
}

func TestDiscardRendererProfileRejectsUnrelatedDirectories(t *testing.T) {
	root := t.TempDir()
	unrelated := filepath.Join(root, "settings")
	if err := os.Mkdir(unrelated, 0700); err != nil {
		t.Fatal(err)
	}
	discardRendererProfile(root, unrelated)
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated application data was removed: %v", err)
	}
}

func TestBrowserProcessArgsSuppressUncleanRestartUI(t *testing.T) {
	args := browserProcessArgs("http://127.0.0.1/app", filepath.Join(t.TempDir(), "profile"), false)
	for _, want := range []string{"--disable-session-crashed-bubble", "--hide-crash-restore-bubble"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing cold-start guard %q in %q", want, args)
		}
	}
}

func TestNativeManagedWindowHideReopenAndClose(t *testing.T) {
	browser := os.Getenv("YUDESK_TEST_BROWSER")
	if browser == "" {
		t.Skip("opt-in isolated native Chromium, no visible windows or input")
	}
	h, err := newViewerHost("127.0.0.1:0", "window-test-token")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := os.MkdirTemp("", "yudesk-headless-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	h.window.profile = profile
	t.Cleanup(func() {
		if err := h.window.Close(); err != nil {
			t.Errorf("browser cleanup: %v", err)
		}
		h.Close()
		if err := os.RemoveAll(profile); err != nil {
			t.Logf("temporary browser cache cleanup deferred: %v", err)
		}
	})
	h.window.candidates = []string{browser}
	h.window.headless = true
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
