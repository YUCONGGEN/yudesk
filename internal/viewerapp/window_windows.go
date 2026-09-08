//go:build windows

package viewerapp

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func startBrowserProcess(executable string, args []string) (uint32, chan struct{}, func() error, error) {
	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return 0, nil, nil, err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{executable}, args...)))
	if err != nil {
		return 0, nil, nil, err
	}
	// The GUI renderer must not inherit the caller's console or pipe handles.
	// Restrict inheritance to NUL, including when YuDesk is started from a
	// terminal, service helper, or test runner with redirected standard streams.
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	null, err := windows.CreateFile(windows.StringToUTF16Ptr("NUL"), windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &security, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, nil, nil, err
	}
	defer windows.CloseHandle(null)
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, nil, nil, err
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&null), unsafe.Sizeof(null)); err != nil {
		return 0, nil, nil, err
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Flags: windows.STARTF_USESHOWWINDOW | windows.STARTF_USESTDHANDLES, ShowWindow: windows.SW_HIDE, StdInput: null, StdOutput: null, StdErr: null}, ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	var process windows.ProcessInformation
	if err := windows.CreateProcess(application, command, nil, nil, true, windows.CREATE_NO_WINDOW|windows.CREATE_SUSPENDED|windows.EXTENDED_STARTUPINFO_PRESENT, nil, nil, &startup.StartupInfo, &process); err != nil {
		return 0, nil, nil, err
	}
	// Keep the actual primary thread handle from CreateProcess. Enumerating
	// threads after exec.Start can select a suspended loader worker instead,
	// leaving the primary thread suspended forever on a later launch.
	guard, err := newBrowserGuard(process.ProcessId)
	if err == nil {
		_, err = windows.ResumeThread(process.Thread)
	}
	windows.CloseHandle(process.Thread)
	if err != nil {
		if guard != nil {
			_ = guard()
		} else {
			_ = windows.TerminateProcess(process.Process, 1)
		}
		windows.WaitForSingleObject(process.Process, 3000)
		windows.CloseHandle(process.Process)
		return 0, nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		windows.WaitForSingleObject(process.Process, windows.INFINITE)
		windows.CloseHandle(process.Process)
		close(done)
	}()
	return process.ProcessId, done, func() error {
		err := guard()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			err = errors.Join(err, errors.New("YuDesk 浏览器进程退出仍在等待系统清理"))
		}
		return err
	}, nil
}

func newBrowserGuard(pid uint32) (func() error, error) {
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
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
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
	var closeErr error
	return func() error {
		once.Do(func() {
			if err := windows.TerminateJobObject(job, 0); err != nil {
				closeErr = fmt.Errorf("结束 YuDesk 浏览器进程组: %w", err)
			}
			// Job termination is asynchronous. Do not reopen this profile until
			// the owned process tree has released its handles.
			deadline := time.Now().Add(3 * time.Second)
			for {
				var accounting struct {
					UserTime, KernelTime, PeriodUserTime, PeriodKernelTime           int64
					PageFaults, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
				}
				if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
					closeErr = errors.Join(closeErr, fmt.Errorf("查询 YuDesk 浏览器清理状态: %w", err))
					break
				}
				if accounting.ActiveProcesses == 0 {
					break
				}
				if time.Now().After(deadline) {
					closeErr = errors.Join(closeErr, fmt.Errorf("YuDesk 浏览器进程清理超时: %d", accounting.ActiveProcesses))
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			closeErr = errors.Join(closeErr, windows.CloseHandle(job))
		})
		return closeErr
	}, nil
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
	owner, _, _ := windowUser32.NewProc("GetWindow").Call(hwnd, 4) // GW_OWNER
	if owner == 0 && syscall.UTF16ToString(name[:]) == "Chrome_WidgetWin_1" {
		search.hwnd = hwnd
		return 0
	}
	return 1
})

// Find only our private browser process, never a title-matched user window.
func (b *appWindow) nativeHandle() uintptr {
	if b.native != nil {
		return b.native.ownedHandle()
	}
	return b.browserHandle()
}

func (b *appWindow) browserHandle() uintptr {
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
