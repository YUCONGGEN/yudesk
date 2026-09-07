//go:build windows

package desktop

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// All clipboard mutations take place in a private window station and desktop.
// Never read, overwrite, or try to reconstruct the user's real clipboard.
func TestUnicodeClipboardInPrivateWindowStation(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	getStation := user32.NewProc("GetProcessWindowStation")
	setStation := user32.NewProc("SetProcessWindowStation")
	createStation := user32.NewProc("CreateWindowStationW")
	closeStation := user32.NewProc("CloseWindowStation")
	previous, _, _ := getStation.Call()
	station, _, err := createStation.Call(0, 0, 0x000F037F, 0)
	if station == 0 {
		t.Fatalf("cannot create isolated clipboard station: %v", err)
	}
	defer closeStation.Call(station)
	if ok, _, err := setStation.Call(station); ok == 0 {
		t.Fatal(err)
	}
	defer setStation.Call(previous)
	name, _ := syscall.UTF16PtrFromString("YuDeskClipboardTest")
	desk, _, err := user32.NewProc("CreateDesktopW").Call(uintptr(unsafe.Pointer(name)), 0, 0, 0, 0x10000000, 0)
	if desk == 0 {
		t.Fatal(err)
	}
	defer closeDesktop.Call(desk)
	id, _, _ := getCurrentThreadID.Call()
	old, _, _ := getThreadDesktop.Call(id)
	if ok, _, err := setThreadDesktop.Call(desk); ok == 0 {
		t.Fatal(err)
	}
	defer setThreadDesktop.Call(old)
	previousCheck := clipboardAccess
	clipboardAccess = func() error { return nil }
	defer func() { clipboardAccess = previousCheck }()
	for _, want := range []string{"中文剪贴板：你好，世界！", "简体 / 繁體 / 日本語 / 한국어 / 😀𠀀", "  前后空格  ", "第一行\r\n第二行\n末尾\r\n", "", "a\tb"} {
		if err := clipboardSetPlatform(want); err != nil {
			t.Fatal(err)
		}
		got, err := clipboardGetPlatform()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("Unicode clipboard differs: got %q want %q", got, want)
		}
	}
	if clipboardSetPlatform("a\x00b") == nil {
		t.Fatal("embedded NUL accepted")
	}
}
