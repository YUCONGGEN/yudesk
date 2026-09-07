//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

const unicodeClipboard = 13 // CF_UNICODETEXT: native UTF-16, never an OEM code page.
const clipboardLimit = 2 << 20

var clipboardMu sync.Mutex
var clipboardAccess = func() error { // replaced only in isolated-window-station native tests
	if interactiveDesktopAvailable() != nil {
		return errors.New("受保护桌面期间暂停剪贴板同步，请解锁后重试")
	}
	return nil
}
var (
	openClipboard            = user32.NewProc("OpenClipboard")
	closeClipboard           = user32.NewProc("CloseClipboard")
	emptyClipboard           = user32.NewProc("EmptyClipboard")
	getClipboardData         = user32.NewProc("GetClipboardData")
	setClipboardData         = user32.NewProc("SetClipboardData")
	clipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	createWindowEx           = user32.NewProc("CreateWindowExW")
	destroyWindow            = user32.NewProc("DestroyWindow")
	globalAlloc              = kernel32.NewProc("GlobalAlloc")
	globalFree               = kernel32.NewProc("GlobalFree")
	globalLock               = kernel32.NewProc("GlobalLock")
	globalUnlock             = kernel32.NewProc("GlobalUnlock")
	globalSize               = kernel32.NewProc("GlobalSize")
	copyClipboardMemory      = kernel32.NewProc("RtlMoveMemory")
)

func withClipboard(fn func() error) error {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := clipboardAccess(); err != nil {
		return err
	}
	// EmptyClipboard needs a non-NULL owner for subsequent SetClipboardData.
	// STATIC is a system class; HWND_MESSAGE creates no visible desktop window.
	class, _ := syscall.UTF16PtrFromString("STATIC")
	window, _, err := createWindowEx.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	if window == 0 {
		return fmt.Errorf("创建剪贴板所有者失败：%v", err)
	}
	defer destroyWindow.Call(window)
	for i := 0; i < 20; i++ {
		if ok, _, _ := openClipboard.Call(window); ok != 0 {
			defer closeClipboard.Call()
			return fn()
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("剪贴板正被其他程序占用，请稍后重试")
}

func clipboardGetPlatform() (value string, err error) {
	err = withClipboard(func() error {
		if available, _, _ := clipboardFormatAvailable.Call(unicodeClipboard); available == 0 {
			return nil
		}
		h, _, e := getClipboardData.Call(unicodeClipboard)
		if h == 0 {
			return fmt.Errorf("读取剪贴板失败：%v", e)
		}
		size, _, _ := globalSize.Call(h)
		if size < 2 || size%2 != 0 || size > clipboardLimit*2+2 {
			return errors.New("剪贴板文本过大或格式无效")
		}
		p, _, e := globalLock.Call(h)
		if p == 0 {
			return fmt.Errorf("锁定剪贴板失败：%v", e)
		}
		defer globalUnlock.Call(h)
		units := make([]uint16, int(size/2))
		// Copy while HGLOBAL is locked. Keep native addresses inside the OS API
		// instead of turning a syscall uintptr into a Go-heap pointer.
		copyClipboardMemory.Call(uintptr(unsafe.Pointer(&units[0])), p, size)
		for i, unit := range units {
			if unit == 0 {
				value = string(utf16.Decode(units[:i]))
				if len(value) > clipboardLimit {
					return errors.New("剪贴板文本过大")
				}
				return nil
			}
		}
		return errors.New("剪贴板文本缺少结束标记")
	})
	return value, err
}

func clipboardSetPlatform(value string) error {
	if len(value) > clipboardLimit || !utf8.ValidString(value) {
		return errors.New("剪贴板文本过大或不是有效 UTF-8")
	}
	text, err := syscall.UTF16FromString(value)
	if err != nil {
		return errors.New("剪贴板文本包含不支持的空字符")
	}
	return withClipboard(func() error {
		h, _, e := globalAlloc.Call(0x0042, uintptr(len(text)*2)) // GMEM_MOVEABLE | GMEM_ZEROINIT
		if h == 0 {
			return fmt.Errorf("分配剪贴板内存失败：%v", e)
		}
		owned := true
		defer func() {
			if owned {
				globalFree.Call(h)
			}
		}()
		p, _, e := globalLock.Call(h)
		if p == 0 {
			return fmt.Errorf("锁定剪贴板内存失败：%v", e)
		}
		copyClipboardMemory.Call(p, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)*2))
		globalUnlock.Call(h)
		if ok, _, e := emptyClipboard.Call(); ok == 0 {
			return fmt.Errorf("更新剪贴板失败：%v", e)
		}
		if ok, _, e := setClipboardData.Call(unicodeClipboard, h); ok == 0 {
			return fmt.Errorf("写入剪贴板失败：%v", e)
		}
		owned = false // Windows owns h after successful SetClipboardData.
		return nil
	})
}
