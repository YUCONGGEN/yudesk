package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionedDownloadAndAndroidPreviewLabel(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "release.json"), []byte(`{"version":"2.0.0","publishedAt":"2026-09-07T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ platform, binary, name string }{
		{"windows-amd64", "yudesk.exe", "YuDesk-2.0.0-windows-amd64.exe"},
		{"android", "yudesk.apk", "YuDesk-2.0.0-android-preview.apk"},
	} {
		if err := os.Mkdir(filepath.Join(root, tc.platform), 0700); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/download/"+tc.platform+"/"+tc.binary, nil)
		missing := httptest.NewRecorder()
		serveDownload(missing, request, root)
		if missing.Code != http.StatusNotFound {
			t.Fatalf("missing artifact: %d", missing.Code)
		}
		if err := os.WriteFile(filepath.Join(root, tc.platform, tc.binary), []byte("fixture artifact"), 0600); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		serveDownload(response, request, root)
		if response.Code != http.StatusOK || response.Header().Get("Content-Disposition") != `attachment; filename="`+tc.name+`"` {
			t.Fatalf("invalid versioned download: %d %v", response.Code, response.Header())
		}
		if tc.platform == "android" && response.Header().Get("Content-Type") != "application/vnd.android.package-archive" {
			t.Fatal("incorrect APK media type")
		}
	}
}
