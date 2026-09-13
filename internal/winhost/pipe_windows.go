//go:build windows

package winhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/yudesk/yudesk/internal/protocol"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const serviceName = "YuDeskDesktop"
const serviceRegistryPath = `SYSTEM\CurrentControlSet\Services\` + serviceName

func defaultInstallDirectory() (string, error) {
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "YuDesk"), nil
}

func validateInstallDirectory(directory string) (string, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" || !filepath.IsAbs(directory) || strings.HasPrefix(directory, `\\`) || strings.HasPrefix(directory, `//`) {
		return "", errors.New("请选择本机磁盘上的完整安装目录")
	}
	directory = filepath.Clean(directory)
	volume := filepath.VolumeName(directory)
	if volume == "" || strings.Trim(directory[len(volume):], `\/`) == "" {
		return "", errors.New("不能直接安装到磁盘根目录")
	}
	if strings.EqualFold(filepath.Base(directory), "yudesk.exe") {
		return "", errors.New("安装目录不能是程序文件名")
	}
	return directory, nil
}

func registeredInstallDirectory() (string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallRegistryPath, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer key.Close()
	directory, _, err := key.GetStringValue("InstallLocation")
	if err != nil {
		return "", err
	}
	return validateInstallDirectory(directory)
}

func serviceExecutablePath(command string) (string, error) {
	command = strings.TrimSpace(command)
	const argument = " -desktop-service"
	if len(command) <= len(argument) || !strings.EqualFold(command[len(command)-len(argument):], argument) {
		return "", errors.New("Windows 服务命令无效")
	}
	path := strings.TrimSpace(command[:len(command)-len(argument)])
	quoted := len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"'
	if quoted {
		path = path[1 : len(path)-1]
	}
	if strings.ContainsAny(path, "\"\r\n") || (!quoted && strings.ContainsAny(path, " \t")) || !filepath.IsAbs(path) || !strings.EqualFold(filepath.Base(path), "yudesk.exe") {
		return "", errors.New("Windows 服务程序路径无效")
	}
	directory, err := validateInstallDirectory(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "yudesk.exe"), nil
}

// A failed/older installer can leave the uninstall key incomplete while the
// service still has the authoritative administrator-owned image path. Recover
// that path so repair installation remains possible instead of failing before
// the user can choose an installation directory.
func serviceInstallDirectory() (string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, serviceRegistryPath, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", err
	}
	defer key.Close()
	command, _, err := key.GetStringValue("ImagePath")
	if err != nil {
		return "", err
	}
	path, err := serviceExecutablePath(command)
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

// ResolveInstallPath resolves an explicit installer choice, or the trusted
// machine-wide location from a previous installation. The registry value is
// written only by the elevated installer and is also used by the service when
// authenticating its desktop worker and GUI peer.
func ResolveInstallPath(directory string) (string, error) {
	if strings.TrimSpace(directory) == "" {
		registered, err := registeredInstallDirectory()
		if err == nil {
			return filepath.Join(registered, "yudesk.exe"), nil
		}
		serviceDirectory, serviceErr := serviceInstallDirectory()
		if serviceErr == nil {
			return filepath.Join(serviceDirectory, "yudesk.exe"), nil
		}
		var defaultErr error
		directory, defaultErr = defaultInstallDirectory()
		if defaultErr != nil {
			return "", errors.Join(fmt.Errorf("无法读取已登记的安装目录: %w", err), defaultErr)
		}
	}
	validated, err := validateInstallDirectory(directory)
	if err != nil {
		return "", err
	}
	return filepath.Join(validated, "yudesk.exe"), nil
}

func installedPath() (string, error) {
	return ResolveInstallPath("")
}
func sameInstalledPath(path string) bool {
	want, err := installedPath()
	if err != nil {
		return false
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	trusted, err := filepath.EvalSymlinks(want)
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(actual), filepath.Clean(trusted))
}
func sessionID(pid uint32) (uint32, error) {
	var id uint32
	err := windows.ProcessIdToSessionId(pid, &id)
	return id, err
}
func pipeName(id uint32) string { return fmt.Sprintf(`\\.\pipe\YuDesk-Desktop-v1-%d`, id) }
func processIsSystem(handle windows.Handle) bool {
	var token windows.Token
	if windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token) != nil {
		return false
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	return err == nil && user.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}
func verifyPeer(c net.Conn, server bool, expectedSession uint32) error {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("invalid local pipe")
	}
	var pid uint32
	var err error
	if server {
		err = windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &pid)
	} else {
		err = windows.GetNamedPipeClientProcessId(windows.Handle(f.Fd()), &pid)
	}
	if err != nil {
		return err
	}
	var id uint32
	sessionAPI := "GetNamedPipeClientSessionId"
	if server {
		sessionAPI = "GetNamedPipeServerSessionId"
	}
	okSession, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc(sessionAPI).Call(f.Fd(), uintptr(unsafe.Pointer(&id)))
	if okSession == 0 {
		return fmt.Errorf("query pipe Windows session: %w", err)
	}
	if id != expectedSession {
		return fmt.Errorf("wrong Windows session: got %d, want %d", id, expectedSession)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var path [32768]uint16
	size := uint32(len(path))
	if windows.QueryFullProcessImageName(h, 0, &path[0], &size) != nil || !sameInstalledPath(windows.UTF16ToString(path[:size])) {
		return errors.New("only the protected installed YuDesk client is allowed")
	}
	if server {
		// Pipe ownership can be queried by the ordinary client; opening a
		// LocalSystem process token would itself require elevated permissions.
		sd, e := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if e != nil {
			return e
		}
		owner, _, e := sd.Owner()
		if e != nil || !owner.IsWellKnown(windows.WinLocalSystemSid) {
			return errors.New("desktop service identity mismatch")
		}
	}
	return nil
}

func Call(method string, params any) (protocol.Message, error) {
	if method != "capture" && method != "input" {
		return protocol.Message{}, errors.New("unsupported desktop operation")
	}
	exe, _ := os.Executable()
	if !sameInstalledPath(exe) {
		return protocol.Message{}, errors.New("锁屏服务需要使用安装版 YuDesk，请在设置中点击“切换到安装版”")
	}
	id, err := sessionID(uint32(os.Getpid()))
	if err != nil {
		return protocol.Message{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := winio.DialPipeContext(ctx, pipeName(id))
	if err != nil {
		return protocol.Message{}, errors.New("锁屏服务未就绪，请检查服务是否已安装并运行")
	}
	defer c.Close()
	if err := verifyPeer(c, true, id); err != nil {
		return protocol.Message{}, err
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	p := protocol.NewConn(c)
	raw, err := json.Marshal(params)
	if err != nil {
		return protocol.Message{}, err
	}
	if err = p.WriteMessage(protocol.Message{Kind: "request", ID: "1", Method: method, Params: raw}); err != nil {
		return protocol.Message{}, err
	}
	m, err := p.ReadMessage()
	if err == nil && (m.Kind != "response" || m.ID != "1") {
		err = errors.New("invalid desktop service response")
	}
	if err == nil && m.Error != "" {
		err = errors.New(m.Error)
	}
	return m, err
}

func serveWorker(handler Handler) error {
	if !processIsSystem(windows.CurrentProcess()) {
		return errors.New("desktop worker requires LocalSystem")
	}
	exe, _ := os.Executable()
	if !sameInstalledPath(exe) {
		return errors.New("worker is not installed")
	}
	id, err := sessionID(uint32(os.Getpid()))
	if err != nil || id == 0 {
		return errors.New("worker must run in the interactive session")
	}
	l, err := winio.ListenPipe(pipeName(id), &winio.PipeConfig{SecurityDescriptor: "O:SYG:SYD:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GRGW;;;IU)", InputBufferSize: 65536, OutputBufferSize: 65536})
	if err != nil {
		return err
	}
	defer l.Close()
	// One capture and a separate input worker. Input cannot queue behind a large
	// frame; all sessions and operations have bounded concurrency and deadlines.
	connections := make(chan struct{}, 8)
	capture := make(chan struct{}, 1)
	input := make(chan struct{}, 1)
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		select {
		case connections <- struct{}{}:
		default:
			c.Close()
			continue
		}
		go func() {
			defer func() { c.Close(); <-connections }()
			_ = c.SetDeadline(time.Now().Add(6 * time.Second))
			if verifyPeer(c, false, id) != nil {
				return
			}
			p := protocol.NewConn(c)
			m, err := p.ReadMessage()
			if err != nil || m.Kind != "request" || len(m.Params) > 128<<10 || len(m.Data) != 0 {
				return
			}
			gate := input
			if m.Method == "capture" {
				gate = capture
			} else if m.Method != "input" {
				return
			}
			select {
			case gate <- struct{}{}:
				defer func() { <-gate }()
			default:
				_ = p.WriteMessage(protocol.Failure(m.ID, "桌面操作繁忙，请重试"))
				return
			}
			data, meta, err := handler(m.Method, m.Params)
			if err != nil {
				_ = p.WriteMessage(protocol.Failure(m.ID, err.Error()))
				return
			}
			_ = p.WriteMessage(protocol.Response(m.ID, data, meta))
		}()
	}
}
