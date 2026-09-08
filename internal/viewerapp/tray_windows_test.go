//go:build windows

package viewerapp

import (
	"os"
	"testing"
	"time"
	"unsafe"
)

func TestNativeTrayVisibilityAndCleanup(t *testing.T) {
	if os.Getenv("YUDESK_NATIVE_TRAY") != "1" {
		t.Skip("opt-in native tray, no real keyboard/mouse input")
	}
	tray, err := newAppTray(func() {}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer tray.Close()
	probe := trayData{Window: tray.hwnd.Load(), ID: 1}
	probe.Size = uint32(unsafe.Sizeof(probe))
	visible := func() bool {
		// Query registration with a no-field modification. GetRect can fail for
		// registered icons while Explorer's overflow flyout has no layout.
		status, _, _ := trayShell.Call(1, uintptr(unsafe.Pointer(&probe)))
		return status != 0
	}
	wait := func(want bool) {
		t.Helper()
		until := time.Now().Add(2 * time.Second)
		for time.Now().Before(until) {
			if visible() == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("tray visibility expected %v", want)
	}
	wait(true)
	tray.Visible(false)
	wait(false)
	tray.Visible(true)
	wait(true)
	tray.Close()
	wait(false)
	if tray.hwnd.Load() != 0 {
		t.Fatal("notification window not destroyed")
	}
}
