//go:build windows

package winhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

var worker atomic.Bool

func IsWorker() bool { return worker.Load() }

func HandleCommand(handler Handler) bool {
	if len(os.Args) < 2 {
		return false
	}
	var err error
	switch os.Args[1] {
	case "-desktop-service":
		err = svc.Run(serviceName, &desktopService{})
	case "-desktop-worker":
		if !processIsSystem(windows.CurrentProcess()) {
			return true
		}
		worker.Store(true)
		err = serveWorker(handler)
	case "-desktop-service-install":
		err = install(false)
	case "-desktop-service-remove":
		err = install(true)
	case "-desktop-service-check":
		var result struct {
			Bytes int            `json:"bytes"`
			Meta  map[string]any `json:"meta"`
			State State          `json:"state"`
		}
		m, e := Call("capture", struct {
			Quality, MaxWidth int
			Raw               bool
		}{65, 640, true})
		err = e
		if err == nil {
			result.Bytes = len(m.Data)
			result.Meta = m.Meta
			result.State = Status()
			err = json.NewEncoder(os.Stdout).Encode(result)
		}
	case "-desktop-wait-parent":
		if len(os.Args) != 3 {
			return true
		}
		pid, e := strconv.ParseUint(os.Args[2], 10, 32)
		if e != nil {
			return true
		}
		h, e := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if e == nil {
			_, _ = windows.WaitForSingleObject(h, 10000)
			windows.CloseHandle(h)
		}
		os.Args = os.Args[:1]
		return false
	default:
		return false
	}
	if err != nil {
		if os.Getenv("YUDESK_SERVICE_DIAGNOSTIC") == "1" {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		showInstallError(err)
	}
	return true
}

func showInstallError(err error) {
	// Services and system workers must not show interactive error dialogs.
	if processIsSystem(windows.CurrentProcess()) {
		return
	}
	title, _ := windows.UTF16PtrFromString("YuDesk 系统服务")
	message, _ := windows.UTF16PtrFromString("操作未完成：" + err.Error())
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
}

func ElevateInstall(remove bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	arg := "-desktop-service-install"
	if remove {
		arg = "-desktop-service-remove"
	}
	params, _ := windows.UTF16PtrFromString(arg)
	return windows.ShellExecute(0, verb, file, params, nil, windows.SW_HIDE)
}

func RestartInstalled() error {
	s := Status()
	if !s.Installed {
		return errors.New("请先安装锁屏服务")
	}
	cmd := exec.Command(s.Path, "-desktop-wait-parent", strconv.Itoa(os.Getpid()))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

func Status() State {
	s := State{Supported: true}
	s.Path, _ = installedPath()
	exe, _ := os.Executable()
	s.TrustedClient = sameInstalledPath(exe)
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		s.Message = "无法查询 Windows 服务"
		return s
	}
	defer windows.CloseServiceHandle(m)
	h, err := windows.OpenService(m, windows.StringToUTF16Ptr(serviceName), windows.SERVICE_QUERY_STATUS)
	if err != nil {
		s.Message = "便携模式：锁屏控制需安装服务"
		return s
	}
	service := &mgr.Service{Name: serviceName, Handle: h}
	defer service.Close()
	s.Installed = true
	status, err := service.Query()
	s.Running = err == nil && status.State == svc.Running
	id, idErr := sessionID(uint32(os.Getpid()))
	if s.Running && idErr == nil && id != 0 {
		name, _ := windows.UTF16PtrFromString(pipeName(id))
		ready, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("WaitNamedPipeW").Call(uintptr(unsafe.Pointer(name)), 1)
		s.Ready = ready != 0
	}
	if !s.Running {
		s.Message = "锁屏服务未运行"
	} else if !s.TrustedClient {
		s.Message = "服务已安装，请切换到安装版"
	} else if idErr != nil || id != windows.WTSGetActiveConsoleSessionId() {
		s.Message = "锁屏服务目前仅支持本机控制台会话，不支持 Windows RDP 会话"
	} else if !s.Ready {
		s.Message = "服务已启动，正在等待桌面辅助进程就绪"
	} else {
		s.Message = "锁屏服务已启用"
	}
	return s
}

func protectDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	resolved, resolveErr := filepath.EvalSymlinks(dir)
	if info.Mode()&os.ModeSymlink != 0 || resolveErr != nil || !strings.EqualFold(filepath.Clean(dir), filepath.Clean(resolved)) {
		return errors.New("安装目录不能是链接")
	}
	sd, err := windows.SecurityDescriptorFromString("O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;GRGX;;;BU)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil)
}

func install(remove bool) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("请允许管理员授权")
	}
	path, err := installedPath()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	service, openErr := m.OpenService(serviceName)
	if openErr == nil {
		defer service.Close()
		config, err := service.Config()
		if err != nil {
			return err
		}
		expected := syscall.EscapeArg(path) + " -desktop-service"
		if !strings.EqualFold(config.BinaryPathName, expected) {
			return errors.New("同名服务的安装路径不同，未修改该服务")
		}
		_, _ = service.Control(svc.Stop)
		until := time.Now().Add(10 * time.Second)
		stopped := false
		for time.Now().Before(until) {
			s, e := service.Query()
			if e != nil {
				return e
			}
			if s.State == svc.Stopped {
				stopped = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !stopped {
			return errors.New("旧服务尚未停止，请稍后重试")
		}
		if remove {
			return service.Delete()
		}
	} else {
		if !errors.Is(openErr, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return openErr
		}
		if remove {
			return nil
		}
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err = protectDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	source, err := os.Executable()
	if err != nil {
		return err
	}
	if !sameInstalledPath(source) {
		if info, e := os.Lstat(path); e == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("安装目标不能是链接")
		}
		in, e := os.Open(source)
		if e != nil {
			return e
		}
		defer in.Close()
		out, e := os.CreateTemp(filepath.Dir(path), ".install-*.exe")
		if e != nil {
			return e
		}
		temp := out.Name()
		defer os.Remove(temp)
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if e = windows.MoveFileEx(windows.StringToUTF16Ptr(temp), windows.StringToUTF16Ptr(path), windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); e != nil {
			return fmt.Errorf("请先退出旧安装版后重试：%w", e)
		}
	}
	if openErr != nil {
		service, err = m.CreateService(serviceName, path, mgr.Config{DisplayName: "YuDesk 锁屏桌面服务", Description: "经管理员授权，为安装版 YuDesk 提供本机锁屏画面与输入。无网络监听，不保存系统密码。", StartType: mgr.StartAutomatic}, "-desktop-service")
		if err != nil {
			return err
		}
		defer service.Close()
	}
	return service.Start()
}

type desktopService struct{}

func (s *desktopService) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return false, 1
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return false, 1
	}
	var process windows.Handle
	current := uint32(0xffffffff)
	var retryAt time.Time
	retryDelay := time.Second
	stopChild := func() {
		if process != 0 {
			_ = windows.TerminateProcess(process, 0)
			_, _ = windows.WaitForSingleObject(process, 3000)
			windows.CloseHandle(process)
			process = 0
		}
	}
	defer stopChild()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case r := <-requests:
			switch r.Cmd {
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				return false, 0
			case svc.Interrogate:
				status <- r.CurrentStatus
			}
		case <-ticker.C:
			id := windows.WTSGetActiveConsoleSessionId()
			if process != 0 {
				exit := uint32(0)
				_ = windows.GetExitCodeProcess(process, &exit)
				if exit != 259 || current != id {
					stopChild()
					if current == id {
						retryAt = time.Now().Add(retryDelay)
						retryDelay = min(30*time.Second, retryDelay*2)
					} else {
						retryAt, retryDelay = time.Time{}, time.Second
					}
				}
			}
			if process == 0 && id != 0xffffffff && id != 0 && !time.Now().Before(retryAt) {
				h, err := startWorker(id, job)
				if err == nil {
					process, current = h, id
				} else {
					retryAt = time.Now().Add(retryDelay)
					retryDelay = min(30*time.Second, retryDelay*2)
				}
			}
		}
	}
}

func startWorker(id uint32, job windows.Handle) (windows.Handle, error) {
	if !processIsSystem(windows.CurrentProcess()) {
		return 0, errors.New("service identity is not LocalSystem")
	}
	var source, primary windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &source); err != nil {
		return 0, err
	}
	defer source.Close()
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeTcbPrivilege"), &luid); err != nil {
		return 0, err
	}
	privileges := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}}}
	if err := windows.AdjustTokenPrivileges(source, false, &privileges, 0, nil, nil); err != nil {
		return 0, err
	}
	if err := windows.DuplicateTokenEx(source, windows.TOKEN_ALL_ACCESS, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return 0, err
	}
	defer primary.Close()
	if err := windows.SetTokenInformation(primary, windows.TokenSessionId, (*byte)(unsafe.Pointer(&id)), 4); err != nil {
		return 0, err
	}
	path, err := installedPath()
	if err != nil {
		return 0, err
	}
	app, _ := windows.UTF16PtrFromString(path)
	command, _ := windows.UTF16PtrFromString(syscall.EscapeArg(path) + " -desktop-worker")
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: desktop, Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	// Ordinary clients may verify the exact helper image. QUERY_LIMITED only:
	// no process-memory access, handles, termination, token or code injection.
	processSecurity, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x1000;;;IU)")
	if err != nil {
		return 0, err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: processSecurity}
	var process windows.ProcessInformation
	if err = windows.CreateProcessAsUser(primary, app, command, &attributes, nil, false, windows.CREATE_NO_WINDOW|windows.CREATE_SUSPENDED, nil, nil, &startup, &process); err != nil {
		return 0, err
	}
	if err = windows.AssignProcessToJobObject(job, process.Process); err != nil {
		windows.TerminateProcess(process.Process, 1)
		windows.CloseHandle(process.Thread)
		windows.CloseHandle(process.Process)
		return 0, err
	}
	if _, err = windows.ResumeThread(process.Thread); err != nil {
		windows.TerminateProcess(process.Process, 1)
		windows.CloseHandle(process.Thread)
		windows.CloseHandle(process.Process)
		return 0, err
	}
	windows.CloseHandle(process.Thread)
	return process.Process, nil
}
