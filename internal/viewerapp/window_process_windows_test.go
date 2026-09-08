//go:build windows

package viewerapp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsBrowserGuardWithConsoleProcess(t *testing.T) {
	if os.Getenv("YUDESK_TEST_BROWSER") == "" {
		t.Skip("opt-in owned process cleanup")
	}
	pid, done, stop, err := startBrowserProcess(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), []string{"/c", "exit", "0"})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("console process %d did not exit", pid)
	}
}

func TestNativeBrowserJobCleanup(t *testing.T) {
	browser := os.Getenv("YUDESK_TEST_BROWSER")
	if browser == "" {
		t.Skip("opt-in isolated Chromium process tree")
	}
	profile, err := os.MkdirTemp("", "yudesk-job-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(profile); err != nil {
			t.Log(err)
		}
	})
	pid, done, stop, err := startBrowserProcess(browser, []string{"--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-background-mode", "--user-data-dir=" + profile, "about:blank"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stop() })
	time.Sleep(time.Second)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("browser %d termination still pending without any window embedding", pid)
	}
}
