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
		{"linux-amd64", "yudesk.deb", "YuDesk-2.0.0-linux-amd64.deb"},
		{"darwin-amd64", "yudesk.pkg", "YuDesk-2.0.0-darwin-amd64.pkg"},
		{"darwin-arm64", "yudesk.pkg", "YuDesk-2.0.0-darwin-arm64.pkg"},
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

func TestLegacyUnixExecutableDownloadsRedirectToInstallers(t *testing.T) {
	for platform, suffix := range map[string]string{"linux-amd64": ".deb", "darwin-amd64": ".pkg", "darwin-arm64": ".pkg"} {
		w := httptest.NewRecorder()
		serveDownload(w, httptest.NewRequest(http.MethodGet, "/download/"+platform+"/yudesk", nil), t.TempDir())
		if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != "/download/"+platform+"/yudesk"+suffix {
			t.Fatalf("invalid installer redirect: %d %s", w.Code, w.Header().Get("Location"))
		}
	}
}
