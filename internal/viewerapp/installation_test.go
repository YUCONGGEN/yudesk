package viewerapp

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func TestDesktopInstallationDefaults(t *testing.T) {
	if got := installedDesktopPath("darwin"); got != "/Applications/YuDesk.app/Contents/MacOS/yudesk" {
		t.Fatal(got)
	}
	if got := installedDesktopPath("linux"); got != "/usr/lib/yudesk/yudesk" {
		t.Fatal(got)
	}
	if got := installedDesktopPath("windows"); got != "" {
		t.Fatal("Windows must retain its verified service installation")
	}
	if got := installedDesktopMarkerPath("darwin"); got != "/Applications/YuDesk.app/Contents/Resources/yudesk-install.json" {
		t.Fatal("Mac install metadata must be a bundle resource, not nested code:", got)
	}
	if got := installedDesktopMarkerPath("linux"); got != "/usr/lib/yudesk/yudesk-install.json" {
		t.Fatal("Linux marker location changed:", got)
	}
	if installedDesktopMarkerPath("windows") != "" {
		t.Fatal("Windows must not use Unix package metadata")
	}
	s := desktopInstallationStatus()
	unix := runtime.GOOS == "darwin" || runtime.GOOS == "linux"
	if s.PackageManaged != unix {
		t.Fatal("platform installation status mismatch")
	}
	if unix && (!s.Supported || s.Path == "" || s.Message == "") {
		t.Fatal("Unix installation setting missing")
	}
	w := newAppWindow("127.0.0.1:4567", "fixture-token")
	if w.useShell != unix {
		t.Fatal("native Unix window is not the default")
	}
}

func TestInstallMarkerCannotPromotePortableCopy(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		path := installedDesktopPath(platform)
		helper := strings.TrimSuffix(path, "yudesk") + "yudesk-window"
		data := fmt.Sprintf(`{"schemaVersion":1,"product":"YuDesk","version":"2.0.0","packageRevision":"1","platform":%q,"architecture":"amd64","installed":true,"executable":%q,"helper":%q}`, platform, path, helper)
		if !validInstallMarker([]byte(data), platform, "amd64", path) {
			t.Fatal("valid marker rejected")
		}
		for _, bad := range []string{strings.Replace(data, "2.0.0", "1.2.0", 1), strings.Replace(data, `"installed":true`, `"installed":false`, 1), strings.Replace(data, "yudesk-window", "other", 1), data + "{}", strings.Repeat(" ", 4097) + data} {
			if validInstallMarker([]byte(bad), platform, "amd64", path) {
				t.Fatal("invalid marker accepted")
			}
		}
		if validInstallMarker([]byte(data), platform, "arm64", path) || validInstallMarker([]byte(data), platform, "amd64", "/tmp/yudesk") {
			t.Fatal("wrong architecture or copied app promoted")
		}
	}
}
