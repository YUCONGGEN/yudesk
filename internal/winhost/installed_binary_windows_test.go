//go:build windows

package winhost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows/svc"
)

func TestInstalledBinaryVersionComparison(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "new.exe"), filepath.Join(dir, "installed.exe")
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(a, "new-build")
	write(b, "old-build")
	if installedBinaryMatches(a, b) {
		t.Fatal("allowed switching back to an old build")
	}
	write(b, "new-build")
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(b, now, now); err != nil {
		t.Fatal(err)
	}
	if !installedBinaryMatches(a, b) || !installedBinaryMatches(a, b) {
		t.Fatal("matching build/cache")
	}
	if installedBinaryMatches(a, dir) || installedBinaryMatches(a, filepath.Join(dir, "missing")) {
		t.Fatal("missing/nonregular target accepted")
	}
	// Atomic replacement with unchanged length/time must invalidate file-ID cache.
	info, _ := os.Stat(b)
	replacement := filepath.Join(dir, "replacement.exe")
	write(replacement, "old-build")
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, b); err != nil {
		t.Fatal(err)
	}
	if installedBinaryMatches(a, b) {
		t.Fatal("cached replaced target")
	}
}

func TestInstalledServiceMustActuallyReachRunning(t *testing.T) {
	calls := 0
	err := waitForInstalledService(func() (svc.Status, error) {
		calls++
		state := svc.StartPending
		if calls == 2 {
			state = svc.Running
		}
		return svc.Status{State: state}, nil
	}, time.Second)
	if err != nil || calls != 2 {
		t.Fatal("did not await running", err, calls)
	}
	if err := waitForInstalledService(func() (svc.Status, error) { return svc.Status{State: svc.Stopped}, nil }, time.Second); err == nil {
		t.Fatal("accepted failed service")
	}
	if err := waitForInstalledService(func() (svc.Status, error) { return svc.Status{State: svc.StartPending}, nil }, 0); err == nil {
		t.Fatal("accepted incomplete service")
	}
}

func TestShellExecuteInfoLayout(t *testing.T) {
	var info shellExecuteInfo
	want := uintptr(112)
	offset := uintptr(104)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		want = 60
		offset = 56
	}
	if unsafe.Sizeof(info) != want || unsafe.Offsetof(info.Process) != offset {
		t.Fatalf("SHELLEXECUTEINFOW layout: size=%d process=%d", unsafe.Sizeof(info), unsafe.Offsetof(info.Process))
	}
}
