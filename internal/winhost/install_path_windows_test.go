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

func TestServiceExecutablePathRecovery(t *testing.T) {
	for command, want := range map[string]string{
		`"C:\Program Files\YuDesk\yudesk.exe" -desktop-service`: `C:\Program Files\YuDesk\yudesk.exe`,
		`D:\Apps\YuDesk\yudesk.exe -desktop-service`:            `D:\Apps\YuDesk\yudesk.exe`,
	} {
		got, err := serviceExecutablePath(command)
		if err != nil || got != want {
			t.Errorf("serviceExecutablePath(%q)=(%q,%v), want %q", command, got, err, want)
		}
	}
	for _, command := range []string{
		`C:\Windows\System32\cmd.exe /c anything`,
		`C:\Program Files\YuDesk\yudesk.exe -desktop-service`,
		`"C:\Program Files\YuDesk\other.exe" -desktop-service`,
		`\\server\share\YuDesk\yudesk.exe -desktop-service`,
		`C:\YuDesk\yudesk.exe -desktop-worker`,
	} {
		if _, err := serviceExecutablePath(command); err == nil {
			t.Errorf("accepted unsafe service command %q", command)
		}
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

func TestDesktopExitEndpointAllowsOnlyAuthenticatedLoopback(t *testing.T) {
	for raw, want := range map[string]string{
		"http://127.0.0.1:9348/?access_token=test-token":   "http://127.0.0.1:9348/api/exit?access_token=test-token",
		"http://[::1]:9348/remote?access_token=test-token": "http://[::1]:9348/api/exit?access_token=test-token",
	} {
		got, ok := desktopExitEndpoint(raw)
		if !ok || got != want {
			t.Errorf("desktopExitEndpoint(%q)=(%q,%v), want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{
		"https://127.0.0.1:9348/?access_token=test-token",
		"http://127.0.0.1:9348/",
		"http://www.yucg.cn:8235/?access_token=test-token",
		"http://127.0.0.1/?access_token=test-token",
		"not a URL",
	} {
		if endpoint, ok := desktopExitEndpoint(raw); ok {
			t.Errorf("accepted unsafe desktop endpoint %q as %q", raw, endpoint)
		}
	}
}
