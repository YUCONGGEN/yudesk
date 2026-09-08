package viewerapp

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLifecycleJournalPrivateBoundedAndConcurrent(t *testing.T) {
	dir := t.TempDir()
	j, err := openLifecycleJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	j.limit = 1024
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 12 {
				j.record("session_disconnected", io.EOF)
			}
		}()
	}
	wg.Wait()
	j.record("secret-pin-123456", errors.New("access_token=secret-url; remote body; password"))
	j.Close()
	j.Close()
	j.record("app_stopped", nil)
	files, err := os.ReadDir(filepath.Join(dir, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected bounded current/previous journals, got %d", len(files))
	}
	var combined string
	for _, entry := range files {
		data, err := os.ReadFile(filepath.Join(dir, "logs", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 1024 {
			t.Fatalf("unbounded journal: %d", len(data))
		}
		combined += string(data)
	}
	for _, secret := range []string{"123456", "access_token", "password", "remote body"} {
		if strings.Contains(combined, secret) {
			t.Fatal("journal leaked caller data")
		}
	}
	if !strings.Contains(combined, "event=unknown_event error=other_error") || !strings.Contains(combined, "error=eof") {
		t.Fatal("missing classified events")
	}
}

func TestLifecycleJournalUnavailableIsOptional(t *testing.T) {
	var disabled *lifecycleJournal
	disabled.record("app_started", nil)
	disabled.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logs"), []byte("block directory"), 0600); err != nil {
		t.Fatal(err)
	}
	j, err := openLifecycleJournal(dir)
	if err == nil || j != nil {
		t.Fatal("expected an unavailable journal")
	}
	j.record("app_stopped", err)
	j.Close()
}
