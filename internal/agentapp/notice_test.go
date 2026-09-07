package agentapp

import (
	"testing"
	"time"
)

func TestHeadlessTerminalNoticeNeverOpensBrowser(t *testing.T) {
	opened := 0
	a := &agent{quit: make(chan struct{}), uiURL: "http://127.0.0.1:1/", allowBrowserUI: false, openNoticeBrowser: func(string) error { opened++; return nil }}
	start := time.Now()
	a.terminateWithNotice("test disconnect", "test")
	a.terminateWithNotice("test expiry", "test")
	if opened != 0 {
		t.Fatalf("headless notice opened %d browsers", opened)
	}
	if !a.quitting() {
		t.Fatal("headless agent did not exit")
	}
	if time.Since(start) > time.Second {
		t.Fatal("headless exit waited for a browser")
	}
}
