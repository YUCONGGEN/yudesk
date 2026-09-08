//go:build windows

package viewerapp

import (
	"errors"
	"golang.org/x/sys/windows"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

func configureBrowserProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}

func newBrowserGuard(cmd *exec.Cmd) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	err = windows.AssignProcessToJobObject(job, process)
	windows.CloseHandle(process)
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { windows.TerminateJobObject(job, 0); windows.CloseHandle(job) }) }, nil
}

var windowUser32 = syscall.NewLazyDLL("user32.dll")
var enumAppWindows = windowUser32.NewProc("EnumWindows")
var appWindowPID = windowUser32.NewProc("GetWindowThreadProcessId")
var appWindowClass = windowUser32.NewProc("GetClassNameW")
var appWindowVisible = windowUser32.NewProc("IsWindowVisible")
var getAppWindowStyle = windowUser32.NewProc("GetWindowLongW")
var setAppWindowStyle = windowUser32.NewProc("SetWindowLongW")
var setAppWindowPos = windowUser32.NewProc("SetWindowPos")
var postAppWindowMessage = windowUser32.NewProc("PostMessageW")

type appWindowSearch struct {
	pid  uint32
	hwnd uintptr
}

var appWindowSearchMu sync.Mutex
var activeAppWindowSearch *appWindowSearch

// One callback for the process lifetime. syscall.NewCallback slots cannot be
// freed, so creating one per minimize/show/drag would eventually exhaust them.
var findAppWindow = syscall.NewCallback(func(hwnd, param uintptr) uintptr {
	search := activeAppWindowSearch
	if search == nil {
		return 0
	}
	var pid uint32
	appWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != search.pid {
		return 1
	}
	var name [128]uint16
	appWindowClass.Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	visible, _, _ := appWindowVisible.Call(hwnd)
	if visible != 0 && syscall.UTF16ToString(name[:]) == "Chrome_WidgetWin_1" {
		search.hwnd = hwnd
		return 0
	}
	return 1
})

// Find only our private browser process, never a title-matched user window.
func (b *appWindow) nativeHandle() uintptr {
	if b.browserPID == 0 {
		return 0
	}
	search := appWindowSearch{pid: b.browserPID}
	appWindowSearchMu.Lock()
	defer appWindowSearchMu.Unlock()
	activeAppWindowSearch = &search
	defer func() { activeAppWindowSearch = nil }()
	enumAppWindows.Call(findAppWindow, 0)
	return search.hwnd
}

func (b *appWindow) removeNativeCaption() {
	hwnd := b.nativeHandle()
	if hwnd == 0 {
		return
	}
	index := int32(-16) // GWL_STYLE
	style, _, _ := getAppWindowStyle.Call(hwnd, uintptr(index))
	setAppWindowStyle.Call(hwnd, uintptr(index), style&^0x00C00000) // WS_CAPTION
	setAppWindowPos.Call(hwnd, 0, 0, 0, 0, 0, 0x0037)               // FRAMECHANGED, NOMOVE, NOSIZE, NOZORDER, NOACTIVATE
}

func (b *appWindow) Drag() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	hwnd := b.nativeHandle()
	if hwnd == 0 {
		return errors.New("未找到 YuDesk 窗口")
	}
	ok, _, err := postAppWindowMessage.Call(hwnd, 0x0112, 0xF012, 0) // WM_SYSCOMMAND, SC_MOVE | HTCAPTION
	if ok == 0 {
		return err
	}
	return nil
}
