package releaseinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishedDateIsExplicitAndBeijing(t *testing.T) {
	root := t.TempDir()
	if _, err := Read(root); err == nil {
		t.Fatal("missing date invented")
	}
	if err := os.WriteFile(filepath.Join(root, "release.json"), []byte(`{"version":"2.0.0","publishedAt":"2026-09-07T23:42:23Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Read(root)
	if err != nil || m.Version != "2.0.0" || m.Date() != "2026-09-08 07:42" {
		t.Fatalf("%+v %v", m, err)
	}
	for _, data := range []string{`{"version":"bad\r\nname","publishedAt":"2026-09-07T00:00:00Z"}`, `{"version":"2.0.0"}`, `{"version":"2.0.0","publishedAt":"2099-01-01T00:00:00Z"}`, `{} {}`} {
		os.WriteFile(filepath.Join(root, "release.json"), []byte(data), 0600)
		if _, err := Read(root); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	if Filename("windows-amd64", ".exe", "2.0.0") != "YuDesk-2.0.0-windows-amd64.exe" {
		t.Fatal("versioned filename")
	}
}
