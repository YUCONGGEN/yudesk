//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"github.com/yudesk/yudesk/internal/nativeinit"
	"github.com/yudesk/yudesk/internal/releaseinfo"
	"github.com/yudesk/yudesk/internal/winhost"
	"golang.org/x/sys/windows"
)

//go:embed icon.svg
var setupFiles embed.FS

var setupUser32 = windows.NewLazySystemDLL("user32.dll")

func main() { runSetup() }

func runSetup() {
	packagePath := flag.String("package", "", "YuDesk client executable to package")
	outputPath := flag.String("output", "", "packaged setup output")
	silent := flag.Bool("silent", false, "install and launch without installer UI")
	directory := flag.String("dir", "", "custom installation directory")
	verify := flag.Bool("verify", false, "verify the embedded client payload")
	iconOutput := flag.String("write-icon", "", "write the YuDesk shortcut icon and exit")
	flag.Parse()
	if *packagePath != "" || *outputPath != "" {
		if *packagePath == "" || *outputPath == "" || flag.NArg() != 0 {
			os.Exit(2)
		}
		executable, err := os.Executable()
		if err != nil || packageSetup(executable, *packagePath, *outputPath) != nil {
			os.Exit(1)
		}
		return
	}
	if *iconOutput != "" {
		if err := writeYuIcon(*iconOutput); err != nil {
			os.Exit(1)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		showNativeError("无法读取 YuDesk 安装包路径")
		return
	}
	if *verify {
		file, openErr := os.Open(executable)
		if openErr != nil {
			os.Exit(1)
		}
		descriptor, readErr := readPayloadDescriptor(file)
		if readErr == nil {
			readErr = verifyPayload(file, descriptor)
		}
		file.Close()
		if readErr != nil {
			os.Exit(1)
		}
		return
	}
	setupLog("setup_started", nil)
	defer func() {
		if failure := recover(); failure != nil {
			err := fmt.Errorf("安装程序异常: %v", failure)
			setupLog("setup_panic", err)
			showNativeError("安装程序遇到异常，已安全停止。请重新下载最新版；诊断记录位于本机 YuDesk 安装目录。")
		}
	}()
	if mutexErr := releaseSetupMutex(); mutexErr != nil {
		setupLog("setup_duplicate", mutexErr)
		if !restoreExistingSetupWindow() {
			showNativeError("YuDesk 安装程序已经打开，请在任务栏中查看。")
		}
		return
	}
	if *silent {
		setupLog("silent_install_started", nil)
		path, installErr := installYuDesk(executable, *directory)
		if installErr != nil {
			setupLog("silent_install_failed", installErr)
			os.Exit(1)
		}
		if launchErr := launchYuDesk(path); launchErr != nil {
			setupLog("silent_launch_failed", launchErr)
			os.Exit(1)
		}
		setupLog("silent_install_complete", nil)
		return
	}
	runtime.LockOSThread()
	dataRoot := filepath.Join(os.Getenv("LOCALAPPDATA"), "YuDesk", "Setup")
	if err = nativeinit.Prepare(dataRoot); err != nil {
		setupLog("webview_prepare_failed", err)
		showNativeError("安装界面组件加载失败，请确认系统已安装 Microsoft Edge WebView2。")
		return
	}
	view := webview.NewWithOptions(webview.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(dataRoot, "WebView2"),
		WindowOptions: webview.WindowOptions{
			Title: "安装 YuDesk", Width: 820, Height: 520, Center: true,
		},
	})
	if view == nil {
		setupLog("webview_create_failed", errors.New("nil WebView2 instance"))
		showNativeError("无法打开 YuDesk 安装界面，请重新下载安装包。")
		return
	}
	defer view.Destroy()
	view.SetSize(820, 520, webview.HintFixed)
	hwnd := uintptr(view.Window())
	removeSetupCaption(hwnd)
	var installing atomic.Bool
	view.Bind("closeSetup", func() {
		if !installing.Load() {
			view.Terminate()
		}
	})
	view.Bind("minimizeSetup", func() { setupUser32.NewProc("ShowWindow").Call(hwnd, windows.SW_MINIMIZE) })
	view.Bind("dragSetup", func() {
		setupUser32.NewProc("ReleaseCapture").Call()
		setupUser32.NewProc("SendMessageW").Call(hwnd, 0x00a1, 2, 0)
	})
	view.Bind("beginInstall", func(directory string) {
		if !installing.CompareAndSwap(false, true) {
			return
		}
		go func() {
			setupLog("install_started", nil)
			path, installErr := installYuDesk(executable, directory)
			if installErr != nil {
				setupLog("install_failed", installErr)
				installing.Store(false)
				dispatchInstallStatus(view, "error", installErr.Error())
				return
			}
			dispatchInstallStatus(view, "complete", "正在打开 YuDesk…")
			time.Sleep(220 * time.Millisecond)
			if launchErr := launchYuDesk(path); launchErr != nil {
				setupLog("launch_failed", launchErr)
				installing.Store(false)
				dispatchInstallStatus(view, "error", "程序已安装，但打开失败："+launchErr.Error())
				return
			}
			setupLog("install_complete", nil)
			view.Dispatch(func() { view.Terminate() })
		}()
	})
	defaultPath, pathErr := winhost.ResolveInstallPath(*directory)
	if pathErr != nil {
		setupLog("install_directory_failed", pathErr)
		showNativeError("无法读取安装目录：" + pathErr.Error())
		return
	}
	directoryJSON, _ := json.Marshal(filepath.Dir(defaultPath))
	icon, _ := setupFiles.ReadFile("icon.svg")
	page := strings.ReplaceAll(setupHTML, "{{ICON}}", base64.StdEncoding.EncodeToString(icon))
	page = strings.ReplaceAll(page, "{{VERSION}}", releaseinfo.Version)
	page = strings.ReplaceAll(page, "{{DIRECTORY}}", string(directoryJSON))
	view.SetHtml(page)
	setupLog("installer_window_ready", nil)
	view.Run()
	setupLog("installer_window_closed", nil)
}

func setupLog(event string, failure error) {
	directory := filepath.Join(os.Getenv("LOCALAPPDATA"), "YuDesk", "Setup")
	if directory == filepath.Join("YuDesk", "Setup") || os.MkdirAll(directory, 0700) != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(directory, "install.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	detail := "none"
	if failure != nil {
		detail = strings.NewReplacer("\r", " ", "\n", " ").Replace(failure.Error())
		if len(detail) > 500 {
			detail = detail[:500]
		}
	}
	_, _ = fmt.Fprintf(file, "%s pid=%d event=%s error=%s\n", time.Now().Format(time.RFC3339Nano), os.Getpid(), event, detail)
}

func dispatchInstallStatus(view webview.WebView, state, message string) {
	stateJSON, _ := json.Marshal(state)
	messageJSON, _ := json.Marshal(message)
	view.Dispatch(func() {
		view.Eval("window.yudeskInstallStatus(" + string(stateJSON) + "," + string(messageJSON) + ")")
	})
}

// releaseSetupMutex returns nil only for the first installer process. The
// mutex handle intentionally remains open until process exit.
func releaseSetupMutex() error {
	name, _ := windows.UTF16PtrFromString(`Local\YuDesk.Setup.v2`)
	handle, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return callErr
	}
	if errors.Is(callErr, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(windows.Handle(handle))
		return errors.New("YuDesk 安装程序已经打开")
	}
	return nil
}

func restoreExistingSetupWindow() bool {
	title, _ := windows.UTF16PtrFromString("安装 YuDesk")
	hwnd, _, _ := setupUser32.NewProc("FindWindowW").Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return false
	}
	setupUser32.NewProc("ShowWindow").Call(hwnd, windows.SW_RESTORE)
	setupUser32.NewProc("SetForegroundWindow").Call(hwnd)
	return true
}

func removeSetupCaption(hwnd uintptr) {
	getStyle := setupUser32.NewProc("GetWindowLongW")
	setStyle := setupUser32.NewProc("SetWindowLongW")
	setPosition := setupUser32.NewProc("SetWindowPos")
	index := int32(-16)
	style, _, _ := getStyle.Call(hwnd, uintptr(index))
	setStyle.Call(hwnd, uintptr(index), style&^uintptr(0x00c00000|0x00040000|0x00010000))
	setPosition.Call(hwnd, 0, 0, 0, 0, 0, 0x0027)
}

func installYuDesk(packagePath, directory string) (string, error) {
	localRoot := filepath.Join(os.Getenv("LOCALAPPDATA"), "YuDesk")
	if localRoot == "YuDesk" {
		return "", errors.New("无法确定当前用户的应用目录")
	}
	workDir := filepath.Join(localRoot, "Setup")
	payloadPath := filepath.Join(workDir, "yudesk-"+releaseinfo.Version+".exe")
	payloadHash, err := extractPayload(packagePath, payloadPath)
	if err != nil {
		return "", err
	}
	defer os.Remove(payloadPath)
	installed, err := winhost.ResolveInstallPath(directory)
	if err != nil {
		return "", err
	}
	if !fileMatchesHash(installed, payloadHash) || !installedServiceReady(installed) || !winhost.UninstallRegistered(installed) {
		if err = runElevated(payloadPath, "-desktop-setup-install", filepath.Dir(installed)); err != nil {
			return "", err
		}
	}
	if !fileMatchesHash(installed, payloadHash) {
		return "", errors.New("安装后的 YuDesk 程序校验失败，请重试")
	}
	if !winhost.UninstallRegistered(installed) {
		return "", errors.New("YuDesk 未能登记到 Windows 程序列表，请重试")
	}
	iconPath := filepath.Join(localRoot, "YuDesk.ico")
	if err = writeYuIcon(iconPath); err != nil {
		return "", fmt.Errorf("程序已安装，但写入桌面图标失败: %w", err)
	}
	if err = createShortcuts(installed, iconPath); err != nil {
		return "", fmt.Errorf("程序已安装，但创建快捷方式失败: %w", err)
	}
	return installed, nil
}

func runElevated(path string, args ...string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(path)
	parameters, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	info := setupShellExecuteInfo{Mask: 0x40 | 0x100 | 0x400, Verb: verb, File: file, Parameters: parameters, Show: windows.SW_HIDE}
	info.Size = uint32(unsafe.Sizeof(info))
	ok, _, callErr := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW").Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return fmt.Errorf("管理员授权未完成: %w", callErr)
	}
	if info.Process == 0 {
		return errors.New("无法取得安装进程状态")
	}
	defer windows.CloseHandle(info.Process)
	result, err := windows.WaitForSingleObject(info.Process, 180000)
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0 {
		return errors.New("安装仍在等待系统处理，请稍后重试")
	}
	var code uint32
	if err = windows.GetExitCodeProcess(info.Process, &code); err != nil {
		return err
	}
	if code != 0 {
		return errors.New("安装没有完成；请关闭旧版 YuDesk 后再试")
	}
	return nil
}

type setupShellExecuteInfo struct {
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

func installedServiceReady(path string) bool {
	if path == "" {
		return false
	}
	cmd := exec.Command(path, "-desktop-service-check")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(15 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return false
	}
}

func fileMatchesHash(path string, want [sha256.Size]byte) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return false
	}
	return equalBytes(hash.Sum(nil), want[:])
}

func createShortcuts(installed, icon string) error {
	programs := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs")
	if err := os.MkdirAll(programs, 0755); err != nil {
		return err
	}
	desktop := filepath.Join(os.Getenv("USERPROFILE"), "Desktop")
	script := `$ErrorActionPreference='Stop';$shell=New-Object -ComObject WScript.Shell;$desktop=[Environment]::GetFolderPath('Desktop');foreach($link in @((Join-Path $env:YUDESK_PROGRAMS 'YuDesk.lnk'),(Join-Path $desktop 'YuDesk.lnk'))){$shortcut=$shell.CreateShortcut($link);$shortcut.TargetPath=$env:YUDESK_EXE;$shortcut.WorkingDirectory=$env:YUDESK_DIR;$shortcut.IconLocation=$env:YUDESK_ICON;$shortcut.Description='YuDesk 低延迟远程桌面';$shortcut.Save()}`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), "YUDESK_PROGRAMS="+programs, "YUDESK_EXE="+installed, "YUDESK_DIR="+filepath.Dir(installed), "YUDESK_ICON="+icon)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(programs, "YuDesk.lnk")); err != nil {
		return err
	}
	// Some managed desktops expose no physical Desktop folder. The Start menu
	// shortcut remains authoritative, so only require a desktop link when the
	// known user directory itself exists.
	if _, err := os.Stat(desktop); err == nil {
		if _, linkErr := os.Stat(filepath.Join(desktop, "YuDesk.lnk")); linkErr != nil {
			// The PowerShell known folder may be redirected; it already verified
			// creation there, so do not mistake redirection for installation failure.
			return nil
		}
	}
	return nil
}

func launchYuDesk(path string) error {
	if !strings.EqualFold(filepath.Base(path), "yudesk.exe") {
		return errors.New("安装路径不正确")
	}
	cmd := exec.Command(path)
	cmd.Dir = filepath.Dir(path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.NewTimer(20 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if yuDeskWindowVisible() {
			return nil
		}
		select {
		case err := <-done:
			if yuDeskWindowVisible() {
				return nil
			}
			if err == nil {
				return errors.New("YuDesk 启动后提前退出，请查看安装诊断记录")
			}
			return fmt.Errorf("YuDesk 启动后提前退出: %w", err)
		case <-ticker.C:
		case <-deadline.C:
			return errors.New("YuDesk 已安装，但主界面启动超时；请从桌面图标重新打开")
		}
	}
}

func yuDeskWindowVisible() bool {
	title, _ := windows.UTF16PtrFromString("YuDesk")
	hwnd, _, _ := setupUser32.NewProc("FindWindowW").Call(0, uintptr(unsafe.Pointer(title)))
	if hwnd == 0 {
		return false
	}
	visible, _, _ := setupUser32.NewProc("IsWindowVisible").Call(hwnd)
	return visible != 0
}

func showNativeError(message string) {
	title, _ := windows.UTF16PtrFromString("YuDesk 安装")
	text, _ := windows.UTF16PtrFromString(message)
	setupUser32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}

func writeYuIcon(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	const scale, size = 4, 256
	large := image.NewRGBA(image.Rect(0, 0, size*scale, size*scale))
	for y := 0; y < size*scale; y++ {
		for x := 0; x < size*scale; x++ {
			nx, ny := float64(x)/(float64(size*scale)/64), float64(y)/(float64(size*scale)/64)
			if !insideRoundedSquare(nx, ny, 15) {
				continue
			}
			t := float64(y) / float64(size*scale-1)
			large.SetRGBA(x, y, color.RGBA{R: uint8(39*(1-t) + 18*t), G: uint8(141*(1-t) + 100*t), B: uint8(255*(1-t) + 232*t), A: 255})
		}
	}
	segments := [][4]float64{{12, 18, 22, 33}, {32, 18, 22, 33}, {22, 33, 22, 48}, {36, 29, 36, 41}, {51, 41, 51, 29}}
	previousX, previousY := 36.0, 41.0
	for i := 1; i <= 24; i++ {
		t := float64(i) / 24
		x, y := cubic(36, 41, 36, 46, 39, 48, 43, 48, t)
		segments = append(segments, [4]float64{previousX, previousY, x, y})
		previousX, previousY = x, y
	}
	for i := 1; i <= 24; i++ {
		t := float64(i) / 24
		x, y := cubic(43, 48, 47, 48, 51, 44, 51, 41, t)
		segments = append(segments, [4]float64{previousX, previousY, x, y})
		previousX, previousY = x, y
	}
	for y := 0; y < size*scale; y++ {
		for x := 0; x < size*scale; x++ {
			nx, ny := float64(x)/(float64(size*scale)/64), float64(y)/(float64(size*scale)/64)
			for _, segment := range segments {
				if pointSegmentDistance(nx, ny, segment) <= 2.75 {
					large.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
					break
				}
			}
		}
	}
	small := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a uint32
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					pixel := large.RGBAAt(x*scale+sx, y*scale+sy)
					r += uint32(pixel.R)
					g += uint32(pixel.G)
					b += uint32(pixel.B)
					a += uint32(pixel.A)
				}
			}
			const samples = scale * scale
			small.SetRGBA(x, y, color.RGBA{uint8(r / samples), uint8(g / samples), uint8(b / samples), uint8(a / samples)})
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".yudesk-icon-*.ico")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	var pngData bytes.Buffer
	if err = png.Encode(&pngData, small); err != nil {
		temporary.Close()
		return err
	}
	data := pngData.Bytes()
	header := []byte{0, 0, 1, 0, 1, 0, 0, 0, 0, 0, 1, 0, 32, 0}
	header = append(header, uint32LE(uint32(len(data)))...)
	header = append(header, uint32LE(22)...)
	if _, err = temporary.Write(header); err == nil {
		_, err = temporary.Write(data)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(temporaryPath, path)
}

func uint32LE(value uint32) []byte {
	return []byte{byte(value), byte(value >> 8), byte(value >> 16), byte(value >> 24)}
}
func insideRoundedSquare(x, y, radius float64) bool {
	if x < 0 || y < 0 || x >= 64 || y >= 64 {
		return false
	}
	cx, cy := math.Max(radius, math.Min(64-radius, x)), math.Max(radius, math.Min(64-radius, y))
	return math.Hypot(x-cx, y-cy) <= radius
}
func pointSegmentDistance(x, y float64, segment [4]float64) float64 {
	dx, dy := segment[2]-segment[0], segment[3]-segment[1]
	length := dx*dx + dy*dy
	if length == 0 {
		return math.Hypot(x-segment[0], y-segment[1])
	}
	t := math.Max(0, math.Min(1, ((x-segment[0])*dx+(y-segment[1])*dy)/length))
	return math.Hypot(x-(segment[0]+t*dx), y-(segment[1]+t*dy))
}
func cubic(a, b, c, d, e, f, g, h, t float64) (float64, float64) {
	u := 1 - t
	return u*u*u*a + 3*u*u*t*c + 3*u*t*t*e + t*t*t*g, u*u*u*b + 3*u*u*t*d + 3*u*t*t*f + t*t*t*h
}

const setupHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><style>
*{box-sizing:border-box}html,body{height:100%;margin:0;overflow:hidden;font-family:"Microsoft YaHei UI","Segoe UI",sans-serif;color:#15243a}body{background:radial-gradient(circle at 10% 0,#eef6ff 0,transparent 38%),radial-gradient(circle at 95% 80%,#e9f2ff 0,transparent 42%),#fbfdff;user-select:none}.bar{height:44px;display:flex;align-items:center;padding:0 8px 0 18px;-webkit-app-region:drag}.bar .title{font-size:13px;color:#78879a;letter-spacing:.02em}.bar .space{flex:1}.bar button{width:44px;height:34px;border:0;border-radius:9px;background:transparent;color:#63738a;font-size:22px;cursor:pointer;-webkit-app-region:no-drag}.bar button:hover{background:#eaf1fa}.bar .close:hover{background:#ef5962;color:white}.content{height:calc(100% - 44px);padding:30px 58px 26px;display:grid;grid-template-columns:1.12fr .88fr;gap:38px}.hero{display:flex;flex-direction:column;justify-content:center}.brand{display:flex;align-items:center;gap:17px;margin-bottom:22px}.brand img{width:66px;height:66px;filter:drop-shadow(0 12px 22px #1769e82b)}.brand strong{font-size:37px;letter-spacing:-1.5px}.version{font-size:13px;color:#7e8da1;margin-left:3px}.hero h1{font-size:29px;line-height:1.28;margin:0 0 13px;letter-spacing:-.5px}.hero p{font-size:14px;line-height:1.8;color:#6f7f94;margin:0;max-width:430px}.features{display:grid;gap:12px;margin-top:22px}.feature{display:flex;align-items:center;gap:10px;font-size:13px;color:#4c6078}.tick{width:21px;height:21px;border-radius:50%;display:grid;place-items:center;background:#e8f6ef;color:#159466;font-weight:bold}.panel{align-self:center;background:#fff;border:1px solid #e1e9f3;border-radius:24px;padding:25px 25px 22px;box-shadow:0 24px 65px #42658f18;text-align:center}.panel h2{font-size:21px;margin:0 0 7px}.panel p{font-size:12px;color:#7a899b;line-height:1.65;margin:0 0 14px}.options-toggle{display:flex;align-items:center;justify-content:center;gap:6px;width:100%;height:30px;border:0;background:transparent;color:#5f7390;font-size:12px;cursor:pointer}.options-toggle:hover{color:#176fe8}.chevron{transition:transform .2s}.options-toggle[aria-expanded="true"] .chevron{transform:rotate(180deg)}.custom{display:none;text-align:left;margin:0 0 13px}.custom.open{display:block}.custom label{display:block;margin:0 0 6px;color:#687a91;font-size:11px}.custom input{width:100%;height:37px;border:1px solid #dce6f2;border-radius:9px;background:#f8fbff;padding:0 11px;color:#354861;font:12px "Segoe UI","Microsoft YaHei UI",sans-serif;outline:none;user-select:text}.custom input:focus{border-color:#68a7f8;box-shadow:0 0 0 3px #237cf512}.custom input:disabled{opacity:.65}.install{width:100%;height:46px;border:0;border-radius:13px;background:linear-gradient(135deg,#237cf5,#1264e8);color:white;font-size:16px;font-weight:600;cursor:pointer;box-shadow:0 10px 24px #1769e833}.install:hover{filter:brightness(1.04)}.install:disabled{cursor:wait;opacity:.72}.status{min-height:38px;padding-top:11px;color:#67788e;font-size:11px;line-height:1.45}.hint{font-size:10px;color:#a0acba;border-top:1px solid #edf1f6;padding-top:11px}.spinner{display:none;width:16px;height:16px;border:2px solid #ffffff66;border-top-color:#fff;border-radius:50%;animation:spin .75s linear infinite;margin-right:8px;vertical-align:-3px}@keyframes spin{to{transform:rotate(360deg)}}
</style></head><body><header class="bar" onpointerdown="if(event.target===this||event.target.classList.contains('title')||event.target.classList.contains('space'))dragSetup()"><span class="title">YuDesk 安装程序</span><span class="space"></span><button onclick="minimizeSetup()" aria-label="最小化">−</button><button class="close" onclick="closeSetup()" aria-label="关闭">×</button></header><main class="content"><section class="hero"><div class="brand"><img src="data:image/svg+xml;base64,{{ICON}}"><div><strong>YuDesk</strong><div class="version">版本 {{VERSION}} · Windows 10 / 11</div></div></div><h1>低延迟连接，安装后立即使用。</h1><p>控制端与被控端合并为一个程序。设备码、连接记录和授权数据会继续保存在当前用户目录。</p><div class="features"><div class="feature"><span class="tick">✓</span>自动创建桌面与开始菜单图标</div><div class="feature"><span class="tick">✓</span>安装系统服务，支持 Windows 锁屏桌面</div><div class="feature"><span class="tick">✓</span>一次安装完成，不重复打开旧窗口</div></div></section><section class="panel"><h2>准备安装</h2><p>默认安装到 Windows 程序目录，仅弹出一次管理员授权。</p><button class="options-toggle" id="optionsToggle" type="button" aria-expanded="false"><span class="chevron">⌄</span>自定义安装目录</button><div class="custom" id="custom"><label for="directory">安装位置（请选择空目录或 YuDesk 专用目录）</label><input id="directory" autocomplete="off" spellcheck="false"></div><button class="install" id="install"><span class="spinner" id="spinner"></span><span id="label">立即安装</span></button><div class="status" id="status" role="status">安装完成后自动打开 YuDesk。</div><div class="hint">安装不会删除已有设备码或连接记录</div></section></main><script>
const button=document.getElementById('install'),label=document.getElementById('label'),spinner=document.getElementById('spinner'),status=document.getElementById('status'),directory=document.getElementById('directory'),custom=document.getElementById('custom'),toggle=document.getElementById('optionsToggle');directory.value={{DIRECTORY}};toggle.onclick=()=>{const open=!custom.classList.contains('open');custom.classList.toggle('open',open);toggle.setAttribute('aria-expanded',String(open));if(open)directory.focus()};button.onclick=()=>{if(button.disabled)return;const target=directory.value.trim();if(!target){custom.classList.add('open');toggle.setAttribute('aria-expanded','true');status.textContent='请输入完整安装目录';directory.focus();return}button.disabled=true;directory.disabled=true;toggle.disabled=true;spinner.style.display='inline-block';label.textContent='正在安装';status.textContent='请完成 Windows 管理员授权，安装窗口会保持打开。';beginInstall(target)};window.yudeskInstallStatus=(state,message)=>{status.textContent=message;if(state==='complete'){label.textContent='安装完成';return}spinner.style.display='none';button.disabled=false;directory.disabled=false;toggle.disabled=false;label.textContent='重新安装'};
</script></body></html>`
