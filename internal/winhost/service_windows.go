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
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/yudesk/yudesk/internal/releaseinfo"
	"github.com/yudesk/yudesk/internal/singleinstance"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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
		err = install(false, "")
	case "-desktop-uninstall":
		err = uninstallApplication()
	case "-desktop-uninstall-elevated":
		err = uninstallInstalledApplication()
	case "-desktop-setup-install":
		if len(os.Args) > 3 {
			err = errors.New("安装目录参数无效")
		} else {
			directory := ""
			if len(os.Args) == 3 {
				directory = os.Args[2]
			}
			err = install(false, directory)
		}
		if err != nil {
			os.Exit(1)
		} // caller displays a styled, non-privileged error
	case "-desktop-setup-remove":
		err = install(true, "")
		if err != nil {
			os.Exit(1)
		} // caller displays a styled, non-privileged error
	case "-desktop-service-remove":
		err = install(true, "")
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
	if !setupMu.TryLock() {
		return errors.New("安装操作正在进行，请稍候")
	}
	defer setupMu.Unlock()
	arg := "-desktop-setup-install"
	if remove {
		arg = "-desktop-setup-remove"
	}
	return runElevatedOperation(arg)
}

func runElevatedOperation(args ...string) error {
	if len(args) == 0 {
		return errors.New("缺少安装操作")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	info := shellExecuteInfo{Mask: 0x40 | 0x100 | 0x400, Verb: verb, File: file, Parameters: params, Show: windows.SW_HIDE}
	info.Size = uint32(unsafe.Sizeof(info))
	ok, _, callErr := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW").Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return fmt.Errorf("管理员授权未完成: %w", callErr)
	}
	if info.Process == 0 {
		return errors.New("无法获取安装进程状态")
	}
	defer windows.CloseHandle(info.Process)
	result, err := windows.WaitForSingleObject(info.Process, 180000)
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0 {
		return errors.New("安装仍在等待系统处理，请稍后检查状态")
	}
	var code uint32
	if err = windows.GetExitCodeProcess(info.Process, &code); err != nil {
		return err
	}
	if code != 0 {
		if args[0] == "-desktop-uninstall-elevated" {
			return errors.New("卸载未完成，请先从托盘退出 YuDesk 后重试")
		}
		return errors.New("安装未完成，请退出旧安装版后重试；原有设备和授权保留")
	}
	return nil
}

var setupMu sync.Mutex

// SHELLEXECUTEINFOW: keep the OS UAC prompt, and wait for a real installation
// result rather than reporting ShellExecute's successful dispatch as success.
type shellExecuteInfo struct {
	Size, Mask                        uint32
	Window                            uintptr
	Verb, File, Parameters, Directory *uint16
	Show                              int32
	Instance, IDList                  uintptr
	Class                             *uint16
	ClassKey                          uintptr
	HotKey                            uint32
	Icon                              uintptr
	Process                           windows.Handle
}

func RestartInstalled() error {
	s := Status()
	if !s.Installed {
		return errors.New("请先安装锁屏服务")
	}
	if s.UpdateRequired {
		return errors.New("安装目录中还是其他版本，请先更新安装版，不能切换回旧程序")
	}
	if !s.Running {
		return errors.New("安装服务未运行，请先修复安装版")
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
		s.Message = "尚未安装，请授权启用安装版；也可暂用便携模式"
		return s
	}
	service := &mgr.Service{Name: serviceName, Handle: h}
	defer service.Close()
	s.Installed = true
	if !s.TrustedClient {
		s.UpdateRequired = !installedBinaryMatches(exe, s.Path)
	}
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
	} else if s.UpdateRequired {
		s.Message = "安装目录中是其他版本，请更新并切换到当前安装版"
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

const uninstallRegistryPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\YuDesk`

func uninstallCommand(path string) string {
	return syscall.EscapeArg(path) + " -desktop-uninstall"
}

func estimatedInstallSize(size int64) uint32 {
	if size <= 0 {
		return 1
	}
	kib := (size + 1023) / 1024
	if kib > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(kib)
}

func registerUninstall(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallRegistryPath, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return fmt.Errorf("无法登记到 Windows 程序列表: %w", err)
	}
	defer key.Close()
	values := map[string]string{
		"DisplayName":          "YuDesk",
		"DisplayVersion":       releaseinfo.Version,
		"Publisher":            "YuDesk",
		"InstallLocation":      filepath.Dir(path),
		"DisplayIcon":          path + ",0",
		"UninstallString":      uninstallCommand(path),
		"QuietUninstallString": uninstallCommand(path),
		"URLInfoAbout":         "http://www.yucg.cn:8235/",
		"InstallDate":          time.Now().Format("20060102"),
	}
	for name, value := range values {
		if err = key.SetStringValue(name, value); err != nil {
			return fmt.Errorf("无法写入 Windows 程序信息 %s: %w", name, err)
		}
	}
	for name, value := range map[string]uint32{"NoModify": 1, "NoRepair": 1, "EstimatedSize": estimatedInstallSize(info.Size())} {
		if err = key.SetDWordValue(name, value); err != nil {
			return fmt.Errorf("无法写入 Windows 程序信息 %s: %w", name, err)
		}
	}
	return nil
}

// UninstallRegistered lets the outer installer repair an older installation
// whose executable/service are current but which is absent from Programs and
// Features. Reading HKLM does not require elevation.
func UninstallRegistered(path string) bool {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallRegistryPath, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return false
	}
	defer key.Close()
	name, _, nameErr := key.GetStringValue("DisplayName")
	version, _, versionErr := key.GetStringValue("DisplayVersion")
	command, _, commandErr := key.GetStringValue("UninstallString")
	directory, _, directoryErr := key.GetStringValue("InstallLocation")
	return nameErr == nil && versionErr == nil && commandErr == nil && directoryErr == nil && name == "YuDesk" && version == releaseinfo.Version && command == uninstallCommand(path) && strings.EqualFold(filepath.Clean(directory), filepath.Dir(path))
}

func uninstallApplication() error {
	if windows.GetCurrentProcessToken().IsElevated() {
		if err := uninstallInstalledApplication(); err != nil {
			return err
		}
		removeUserIntegration()
		return nil
	}
	if err := runElevatedOperation("-desktop-uninstall-elevated"); err != nil {
		return err
	}
	removeUserIntegration()
	return nil
}

func uninstallInstalledApplication() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("请允许管理员授权")
	}
	source, err := os.Executable()
	if err != nil {
		return err
	}
	if !sameInstalledPath(source) {
		return errors.New("请从 Windows 程序列表卸载安装版 YuDesk")
	}
	path, err := installedPath()
	if err != nil {
		return err
	}
	if err = install(true, ""); err != nil {
		return err
	}
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	// This executable is the uninstaller itself, so Windows cannot remove it
	// before process exit. Queue only the exact registered executable. A custom
	// directory can contain files owned by the user and is never removed here.
	if err = windows.MoveFileEx(path16, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT); err != nil {
		return fmt.Errorf("无法安排删除安装程序: %w", err)
	}
	if err = registry.DeleteKey(registry.LOCAL_MACHINE, uninstallRegistryPath); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("无法删除 Windows 程序登记: %w", err)
	}
	return nil
}

func removeUserIntegration() {
	paths := []string{
		filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "YuDesk.lnk"),
		filepath.Join(os.Getenv("USERPROFILE"), "Desktop", "YuDesk.lnk"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "YuDesk", "YuDesk.ico"),
	}
	for _, path := range paths {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func installDirectoryAvailable(directory, previousPath string) error {
	info, err := os.Stat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("安装目录指向的不是文件夹")
	}
	if strings.EqualFold(filepath.Clean(directory), filepath.Clean(filepath.Dir(previousPath))) {
		return nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("自定义安装目录必须是空目录或新的 YuDesk 目录")
	}
	return nil
}

func desktopGUIRunning() (bool, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return false, err
	}
	lock, acquired, err := singleinstance.Acquire(filepath.Join(root, "yudesk", "viewer.lock"))
	if err != nil {
		return false, err
	}
	if acquired {
		return false, lock.Close()
	}
	return true, nil
}

func install(remove bool, requestedDirectory string) (resultErr error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("请允许管理员授权")
	}
	previousPath, err := installedPath()
	if err != nil {
		return err
	}
	path := previousPath
	if !remove {
		path, err = ResolveInstallPath(requestedDirectory)
		if err != nil {
			return err
		}
		if !strings.EqualFold(path, previousPath) {
			running, runningErr := desktopGUIRunning()
			if runningErr != nil {
				return fmt.Errorf("无法确认 YuDesk 是否已退出: %w", runningErr)
			}
			if running {
				return errors.New("更换安装目录前，请先从 YuDesk 托盘菜单选择“退出”")
			}
		}
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	service, openErr := m.OpenService(serviceName)
	relocated := false
	if openErr == nil {
		defer service.Close()
		previous, _ := service.Query()
		defer func() {
			// Copy failures (for example an old GUI still holding the executable)
			// must not leave a previously healthy installation silently stopped.
			if resultErr != nil && !remove && previous.State == svc.Running {
				_ = service.Start()
			}
		}()
		config, err := service.Config()
		if err != nil {
			return err
		}
		expectedPrevious := syscall.EscapeArg(previousPath) + " -desktop-service"
		expected := syscall.EscapeArg(path) + " -desktop-service"
		if !strings.EqualFold(config.BinaryPathName, expected) && !strings.EqualFold(config.BinaryPathName, expectedPrevious) {
			return errors.New("同名服务的安装路径不同，未修改该服务")
		}
		relocated = !strings.EqualFold(config.BinaryPathName, expected)
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
	if err = installDirectoryAvailable(filepath.Dir(path), previousPath); err != nil {
		return err
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
	} else if relocated {
		config, configErr := service.Config()
		if configErr != nil {
			return configErr
		}
		config.BinaryPathName = syscall.EscapeArg(path) + " -desktop-service"
		if err = service.UpdateConfig(config); err != nil {
			return fmt.Errorf("无法更新系统服务安装目录: %w", err)
		}
	}
	// Register before service start: the LocalSystem worker authenticates its
	// executable against this administrator-owned location during startup.
	if err = registerUninstall(path); err != nil {
		return err
	}
	if err := service.Start(); err != nil {
		return err
	}
	if err := waitForInstalledService(service.Query, 10*time.Second); err != nil {
		return err
	}
	// If this was an explicit directory migration, remove only the old exact
	// executable when Windows permits it. A running old GUI can keep the file
	// open; leaving that inert, no-longer-trusted copy is safer than scheduling a
	// surprising deletion at a later reboot.
	if relocated && !strings.EqualFold(previousPath, path) {
		_ = os.Remove(previousPath)
	}
	return nil
}

func waitForInstalledService(query func() (svc.Status, error), timeout time.Duration) error {
	until := time.Now().Add(timeout)
	for {
		status, err := query()
		if err != nil {
			return err
		}
		if status.State == svc.Running {
			return nil
		}
		if status.State == svc.Stopped {
			return errors.New("安装服务启动失败，请修复安装后重试")
		}
		if !time.Now().Before(until) {
			return errors.New("安装服务尚未就绪，请稍后检查状态")
		}
		time.Sleep(min(50*time.Millisecond, time.Until(until)))
	}
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
