//go:build windows

package desktop

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"github.com/yudesk/yudesk/internal/winhost"
)

var (
	openInputDesktop         = user32.NewProc("OpenInputDesktop")
	closeDesktop             = user32.NewProc("CloseDesktop")
	getThreadDesktop         = user32.NewProc("GetThreadDesktop")
	getUserObjectInformation = user32.NewProc("GetUserObjectInformationW")
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	getCurrentThreadID       = kernel32.NewProc("GetCurrentThreadId")
	setThreadDesktop         = user32.NewProc("SetThreadDesktop")
)

const protectedDesktopMessage = "被控电脑已锁屏或切换到受保护桌面，当前便携模式无权操作。请在被控电脑解锁或关闭系统授权提示，画面将自动恢复。"

func enterCaptureDesktop() (func(), error) {
	if !winhost.IsWorker() {
		return func() {}, interactiveDesktopAvailable()
	}
	h, _, err := openInputDesktop.Call(0, 0, 0x01ff)
	if h == 0 {
		return nil, err
	}
	id, _, _ := getCurrentThreadID.Call()
	previous, _, _ := getThreadDesktop.Call(id)
	if ok, _, err := setThreadDesktop.Call(h); ok == 0 {
		closeDesktop.Call(h)
		return nil, err
	}
	return func() {
		if ok, _, _ := setThreadDesktop.Call(previous); ok == 0 {
			os.Exit(1)
		}
		closeDesktop.Call(h)
	}, nil
}

// Called on a locked OS thread. Never switch desktops, elevate, or send input
// into a different desktop implicitly; that requires an explicitly installed
// privileged host. Detect before GDI, which can otherwise return a stale frame.
func interactiveDesktopAvailable() error {
	h, _, _ := openInputDesktop.Call(0, 0, 0x0001) // DESKTOP_READOBJECTS
	if h == 0 {
		return errors.New(protectedDesktopMessage)
	}
	defer closeDesktop.Call(h)
	active, ok := desktopObjectName(h)
	if !ok {
		return errors.New("暂时无法确认当前桌面，正在等待桌面恢复")
	}
	id, _, _ := getCurrentThreadID.Call()
	current, _, _ := getThreadDesktop.Call(id)
	name, ok := desktopObjectName(current)
	if !ok || !strings.EqualFold(name, active) || !strings.EqualFold(active, "Default") {
		return errors.New(protectedDesktopMessage)
	}
	return nil
}

func desktopObjectName(h uintptr) (string, bool) {
	var buffer [256]uint16
	var needed uint32
	ok, _, _ := getUserObjectInformation.Call(h, 2, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)*2), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		return "", false
	}
	return syscall.UTF16ToString(buffer[:]), true
}
