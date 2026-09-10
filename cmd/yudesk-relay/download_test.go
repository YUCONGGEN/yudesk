package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type delayedDownloadWriter struct {
	http.ResponseWriter
	once sync.Once
}

func (w *delayedDownloadWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *delayedDownloadWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { time.Sleep(100 * time.Millisecond) })
	return w.ResponseWriter.Write(p)
}

func TestDownloadSurvivesShortAPIWriteTimeout(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "windows-amd64"), 0700); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("download-payload"), 4096)
	if err := os.WriteFile(filepath.Join(root, "windows-amd64", "yudesk.exe"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveDownload(&delayedDownloadWriter{ResponseWriter: w}, r, root)
	}))
	server.Config.WriteTimeout = 25 * time.Millisecond
	server.Start()
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	for _, partial := range []bool{false, true} {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/download/windows-amd64/yudesk.exe", nil)
		if err != nil {
			t.Fatal(err)
		}
		want, status := payload, http.StatusOK
		if partial {
			req.Header.Set("Range", "bytes=10-999")
			want, status = payload[10:1000], http.StatusPartialContent
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("download interrupted by API timeout (partial=%v): %v", partial, err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != status || !bytes.Equal(body, want) {
			t.Fatalf("partial=%v status=%d bytes=%d err=%v", partial, response.StatusCode, len(body), err)
		}
		if response.Header.Get("Accept-Ranges") != "bytes" || response.Header.Get("ETag") == "" {
			t.Fatalf("partial=%v missing resume validators: %v", partial, response.Header)
		}
		if got := response.Header.Get("Cache-Control"); got == "" || strings.Contains(got, "no-store") || !strings.Contains(got, "no-transform") {
			t.Fatalf("partial=%v unsafe download cache policy %q", partial, got)
		}
	}
}

type deadlineDownloadWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *deadlineDownloadWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestOnlyAvailableDownloadsReceiveBoundedWriteDeadline(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "windows-amd64"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "windows-amd64", "yudesk.exe"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		extended     bool
	}{
		{http.MethodGet, "/download/windows-amd64/yudesk.exe", true},
		{http.MethodHead, "/download/windows-amd64/yudesk.exe", true},
		{http.MethodPost, "/download/windows-amd64/yudesk.exe", false},
		{http.MethodGet, "/download/linux-amd64/yudesk", false},
		{http.MethodGet, "/download/../../yudesk.db", false},
	} {
		w := &deadlineDownloadWriter{ResponseRecorder: httptest.NewRecorder()}
		before := time.Now()
		serveDownload(w, httptest.NewRequest(tc.method, tc.path, nil), root)
		if tc.extended {
			if w.deadline.Before(before.Add(9*time.Minute)) || w.deadline.After(time.Now().Add(10*time.Minute)) {
				t.Fatalf("%s %s: invalid bounded deadline %v", tc.method, tc.path, w.deadline)
			}
		} else if !w.deadline.IsZero() {
			t.Fatalf("%s %s unexpectedly extended deadline", tc.method, tc.path)
		}
	}
}
