//go:build windows

package winhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateCustomInstallDirectory(t *testing.T) {
	for _, invalid := range []string{"", `YuDesk`, `C:\`, `\\server\share\YuDesk`, `//server/share/YuDesk`, `C:\Apps\yudesk.exe`} {
		if _, err := validateInstallDirectory(invalid); err == nil {
			t.Errorf("accepted unsafe install directory %q", invalid)
		}
	}
	got, err := validateInstallDirectory(`C:\Apps\YuDesk\.`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `C:\Apps\YuDesk` {
		t.Fatalf("unexpected normalized directory %q", got)
	}
	path, err := ResolveInstallPath(`D:\Programs\YuDesk`)
	if err != nil {
		t.Fatal(err)
	}
	if path != `D:\Programs\YuDesk\yudesk.exe` {
		t.Fatalf("unexpected install path %q", path)
	}
}

func TestCustomInstallDirectoryMustBeDedicated(t *testing.T) {
	directory := t.TempDir()
	previous := filepath.Join(t.TempDir(), "yudesk.exe")
	if err := installDirectoryAvailable(directory, previous); err != nil {
		t.Fatalf("empty custom directory rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "personal.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installDirectoryAvailable(directory, previous); err == nil {
		t.Fatal("non-empty unrelated custom directory accepted")
	}
	if err := installDirectoryAvailable(directory, filepath.Join(directory, "yudesk.exe")); err != nil {
		t.Fatalf("registered YuDesk directory rejected during repair: %v", err)
	}
}

func TestWindowsUninstallMetadataHelpers(t *testing.T) {
	command := uninstallCommand(`C:\Program Files\YuDesk\yudesk.exe`)
	if !strings.Contains(command, `"C:\Program Files\YuDesk\yudesk.exe"`) || !strings.HasSuffix(command, " -desktop-uninstall") {
		t.Fatalf("unsafe uninstall command %q", command)
	}
	for size, want := range map[int64]uint32{0: 1, 1: 1, 1024: 1, 1025: 2} {
		if got := estimatedInstallSize(size); got != want {
			t.Errorf("estimatedInstallSize(%d)=%d, want %d", size, got, want)
		}
	}
}
