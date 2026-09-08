//go:build windows

package viewerapp

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowCaption       = 0x00c00000
	windowResize        = 0x00040000
	windowChild         = 0x40000000
	windowPopup         = 0x80000000
	windowFrameChanged  = 0x0020
	windowNoActivate    = 0x0010
	windowNoZOrder      = 0x0004
	windowAsyncPosition = 0x4000
	nativeShow          = 0x8003
	nativeMinimize      = 0x8004
	nativeDispose       = 0x8005
	nativeHide          = 0x8006
)

var nativeGetRect = windowUser32.NewProc("GetWindowRect")
var nativeGetClientRect = windowUser32.NewProc("GetClientRect")
var nativeClientToScreen = windowUser32.NewProc("ClientToScreen")
var nativeSetParent = windowUser32.NewProc("SetParent")
var nativeGetParent = windowUser32.NewProc("GetParent")
var nativeShowWindow = windowUser32.NewProc("ShowWindow")
var nativeSetFocus = windowUser32.NewProc("SetFocus")
var nativeAttachInput = windowUser32.NewProc("AttachThreadInput")
var nativeIsIconic = windowUser32.NewProc("IsIconic")
var nativeIsWindow = windowUser32.NewProc("IsWindow")
var nativeEnumChildren = windowUser32.NewProc("EnumChildWindows")
var nativeGetDPIContext = windowUser32.NewProc("GetWindowDpiAwarenessContext")
var nativeSetDPIContext = windowUser32.NewProc("SetThreadDpiAwarenessContext")
var nativeMonitorFromWindow = windowUser32.NewProc("MonitorFromWindow")
var nativeGetMonitorInfo = windowUser32.NewProc("GetMonitorInfoW")
var nativePostThreadMessage = windowUser32.NewProc("PostThreadMessageW")

type nativeRect struct{ Left, Top, Right, Bottom int32 }
type nativePoint struct{ X, Y int32 }

// Chromium's --app caption is also drawn INSIDE its Win32 client area. Changing
// WS_CAPTION alone cannot remove it. A resizable, captionless window owned by Go
// clips the embedded browser at the actual web renderer bounds. The whole web
// viewport (including YuDesk's own toolbar) remains visible, without fullscreen,
// fixed pixel cropping, injected browser code, or subclassing another process.
type nativeAppWindow struct {
	hwnd         atomic.Uintptr
	threadID     atomic.Uint32
	closing      atomic.Bool
	done         chan struct{}
	browser      uintptr
	browserPID   uint32
	browserStyle uintptr
	disposed     bool // host message thread only
	onClose      func()
	ready        chan error
	deadline     time.Time
}

var nativeWindows sync.Map
var nativeClassOnce sync.Once
var nativeClassErr error
var nativeClassName = syscall.StringToUTF16Ptr("YuDesk.ContentWindow")

var nativeWindowProc = syscall.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	if value, ok := nativeWindows.Load(hwnd); ok {
		n := value.(*nativeAppWindow)
		switch msg {
		case 0x0024: // WM_GETMINMAXINFO: maximize to the work area, keeping taskbar.
			var monitor struct {
				Size         uint32
				Bounds, Work nativeRect
				Flags        uint32
			}
			monitor.Size = uint32(unsafe.Sizeof(monitor))
			hmon, _, _ := nativeMonitorFromWindow.Call(hwnd, 2)
			if ok, _, _ := nativeGetMonitorInfo.Call(hmon, uintptr(unsafe.Pointer(&monitor))); ok != 0 {
				var info struct{ Reserved, MaxSize, MaxPosition, MinTrackSize, MaxTrackSize nativePoint }
				// LPARAM belongs to USER32, not Go. Copy through the checked OS
				// boundary instead of manufacturing a Go pointer from an integer.
				if windows.ReadProcessMemory(windows.CurrentProcess(), lp, (*byte)(unsafe.Pointer(&info)), unsafe.Sizeof(info), nil) == nil {
					info.MaxPosition = nativePoint{monitor.Work.Left - monitor.Bounds.Left, monitor.Work.Top - monitor.Bounds.Top}
					info.MaxSize = nativePoint{monitor.Work.Right - monitor.Work.Left, monitor.Work.Bottom - monitor.Work.Top}
					if windows.WriteProcessMemory(windows.CurrentProcess(), lp, (*byte)(unsafe.Pointer(&info)), unsafe.Sizeof(info), nil) == nil {
						return 0
					}
				}
			}
		case 0x0007: // WM_SETFOCUS: keyboard focus stays in the embedded browser.
			if !n.closing.Load() && n.ownsBrowser() {
				nativeSetFocus.Call(n.browser)
			}
			return 0
		case 0x0005, 0x0113: // WM_SIZE, WM_TIMER: also catches renderer replacement/navigation
			if n.closing.Load() {
				n.dispose()
				return 0
			}
			if err := n.layout(); err != nil && n.ready != nil && time.Now().After(n.deadline) {
				n.complete(fmt.Errorf("YuDesk 网页窗口布局失败: %w", err))
			} else if err == nil && n.ready != nil {
				ready := n.ready
				n.ready = nil // ShowWindow can reenter WM_SIZE on this thread.
				n.show()
				ready <- nil
			}
			return 0
		case nativeShow:
			_ = n.layout()
			n.show()
			return 0
		case nativeMinimize:
			nativeShowWindow.Call(hwnd, 6) // SW_MINIMIZE, only our host
			return 0
		case nativeHide:
			nativeShowWindow.Call(hwnd, 0) // SW_HIDE: preserve renderer and UI state
			return 0
		case 0x02e0: // WM_DPICHANGED: retain the suggested bounds on the new monitor
			var r nativeRect
			if windows.ReadProcessMemory(windows.CurrentProcess(), lp, (*byte)(unsafe.Pointer(&r)), unsafe.Sizeof(r), nil) == nil {
				setAppWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), windowNoZOrder|windowNoActivate)
				_ = n.layout()
				return 0
			}
		case 0x0010: // WM_CLOSE (Alt+F4/taskbar), same exit semantics as the web X
			if n.onClose != nil {
				go n.onClose()
			}
			fallthrough
		case nativeDispose:
			n.dispose()
			return 0
		case 0x0002: // WM_DESTROY
			n.disposed = true
			n.closing.Store(true)
			n.hwnd.CompareAndSwap(hwnd, 0)
			trayKillTimer.Call(hwnd, 1)
			n.complete(errors.New("YuDesk 窗口已关闭"))
			trayQuit.Call(0)
			return 0
		}
	}
	r, _, _ := trayDefProc.Call(hwnd, uintptr(msg), wp, lp)
	return r
})

func (n *nativeAppWindow) complete(err error) {
	if n.ready != nil {
		n.ready <- err
		n.ready = nil
	}
}

func (n *nativeAppWindow) ownsBrowser() bool {
	var pid uint32
	appWindowPID.Call(n.browser, uintptr(unsafe.Pointer(&pid)))
	return n.browserPID != 0 && pid == n.browserPID
}

func (n *nativeAppWindow) ownedHandle() uintptr {
	hwnd := n.hwnd.Load()
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	thread, _, _ := appWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != windows.GetCurrentProcessId() || uint32(thread) != n.threadID.Load() {
		return 0
	}
	if owner, ok := nativeWindows.Load(hwnd); !ok || owner != n {
		return 0
	}
	return hwnd
}

// Runs only on the host message thread, never destroys a browser/user HWND.
func (n *nativeAppWindow) dispose() {
	if n.disposed {
		return
	}
	n.disposed = true
	n.closing.Store(true)
	hwnd := n.hwnd.Load()
	nativeShowWindow.Call(hwnd, 0)
	// Hide before detaching so Chromium's caption cannot flash during teardown.
	if parent, _, _ := nativeGetParent.Call(n.browser); parent == hwnd && n.ownsBrowser() {
		browserThread, _, _ := appWindowPID.Call(n.browser, 0)
		nativeSetFocus.Call(0)
		nativeShowWindow.Call(n.browser, 0)
		// SetParent does not restore WS_CHILD/WS_POPUP. Leaving Chromium as a
		// desktop child keeps cross-thread window/input relationships alive
		// during shutdown. Restore its original top-level style while hidden.
		index := int32(-16)
		setAppWindowStyle.Call(n.browser, uintptr(index), n.browserStyle&^uintptr(0x10000000))
		nativeSetParent.Call(n.browser, 0)
		setAppWindowPos.Call(n.browser, 0, 0, 0, 0, 0, windowFrameChanged|windowNoActivate|windowNoZOrder|3|windowAsyncPosition)
		if browserThread != 0 {
			nativeAttachInput.Call(uintptr(n.threadID.Load()), browserThread, 0)
		}
	}
	trayDestroy.Call(hwnd)
}

func (n *nativeAppWindow) show() {
	command := uintptr(5) // SW_SHOW preserves maximized/snap bounds
	if iconic, _, _ := nativeIsIconic.Call(n.hwnd.Load()); iconic != 0 {
		command = 9 // SW_RESTORE
	}
	nativeShowWindow.Call(n.hwnd.Load(), command)
	trayForeground.Call(n.hwnd.Load())
}

func (n *nativeAppWindow) run() {
	runtime.LockOSThread()
	// Retire this dedicated GUI thread when its pump exits, allowing Windows to
	// release the thread's input/message-queue state instead of pooling it.
	defer close(n.done)
	n.threadID.Store(windows.GetCurrentThreadId())
	defer n.threadID.Store(0)
	if n.closing.Load() || !n.ownsBrowser() {
		n.complete(errors.New("YuDesk 网页窗口已关闭"))
		return
	}
	// SetParent requires matching DPI awareness. Match the owned Chromium HWND,
	// and keep all host geometry on this same OS thread in physical pixels.
	if nativeGetDPIContext.Find() == nil && nativeSetDPIContext.Find() == nil {
		context, _, _ := nativeGetDPIContext.Call(n.browser)
		previous, _, _ := nativeSetDPIContext.Call(context)
		defer nativeSetDPIContext.Call(previous)
	}
	instance, _, _ := trayModule.Call(0)
	nativeClassOnce.Do(func() {
		class := trayClass{Proc: nativeWindowProc, Instance: instance, Name: nativeClassName}
		class.Size = uint32(unsafe.Sizeof(class))
		if atom, _, err := trayRegister.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
			nativeClassErr = err
		}
	})
	if nativeClassErr != nil {
		n.complete(nativeClassErr)
		return
	}
	var rect nativeRect
	if ok, _, err := nativeGetRect.Call(n.browser, uintptr(unsafe.Pointer(&rect))); ok == 0 {
		n.complete(err)
		return
	}
	// No WS_CAPTION or WS_SYSMENU: there is no native title/button area. Keep
	// WS_THICKFRAME and min/max capabilities for resize, snap and taskbar restore.
	style := uintptr(windowPopup | windowResize | 0x02000000 | 0x00030000)
	title := syscall.StringToUTF16Ptr("YuDesk")
	hwnd, _, err := trayCreate.Call(0x00040000, uintptr(unsafe.Pointer(nativeClassName)), uintptr(unsafe.Pointer(title)), style,
		uintptr(rect.Left), uintptr(rect.Top), uintptr(rect.Right-rect.Left), uintptr(rect.Bottom-rect.Top), 0, 0, instance, 0)
	if hwnd == 0 {
		n.complete(err)
		return
	}
	n.hwnd.Store(hwnd)
	nativeWindows.Store(hwnd, n)
	defer nativeWindows.Delete(hwnd)
	defer n.hwnd.Store(0)
	defer n.dispose()
	icon, iconErr := makeTrayIcon()
	if iconErr == nil {
		defer trayIconDestroy.Call(icon)
		windowUser32.NewProc("SendMessageW").Call(hwnd, 0x80, 0, icon) // WM_SETICON
		windowUser32.NewProc("SendMessageW").Call(hwnd, 0x80, 1, icon)
	}
	nativeShowWindow.Call(n.browser, 0)
	index := int32(-16)
	n.browserStyle, _, _ = getAppWindowStyle.Call(n.browser, uintptr(index))
	if err = embedBrowserStyle(n.browser); err != nil {
		n.complete(err)
		return
	}
	_, _, parentErr := nativeSetParent.Call(n.browser, hwnd)
	if parent, _, _ := nativeGetParent.Call(n.browser); parent != hwnd {
		n.complete(fmt.Errorf("无法嵌入 YuDesk 网页窗口: %v", parentErr))
		return
	}
	nativeShowWindow.Call(n.browser, 5)
	if timer, _, err := traySetTimer.Call(hwnd, 1, 100, 0); timer == 0 {
		n.complete(err)
		return
	}
	var msg trayMessage
	for {
		status, _, _ := trayGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(status) <= 0 {
			break
		}
		if msg.Window == 0 && msg.Message == nativeDispose {
			n.dispose()
			continue
		}
		windowUser32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		trayDispatch.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func embedBrowserStyle(hwnd uintptr) error {
	index := int32(-16) // GWL_STYLE
	style, _, _ := getAppWindowStyle.Call(hwnd, uintptr(index))
	want := (style &^ (windowPopup | windowCaption | windowResize | 0x000b0000)) | windowChild
	if style != want {
		setAppWindowStyle.Call(hwnd, uintptr(index), want)
		if got, _, err := getAppWindowStyle.Call(hwnd, uintptr(index)); got != want {
			return fmt.Errorf("无法设置 YuDesk 子窗口样式: %v", err)
		}
		setAppWindowPos.Call(hwnd, 0, 0, 0, 0, 0, windowFrameChanged|windowNoActivate|windowNoZOrder|3|windowAsyncPosition)
	}
	return nil
}

var rendererSearchMu sync.Mutex
var rendererSearchHandle uintptr
var findRendererWindow = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
	var name [128]uint16
	appWindowClass.Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	if syscall.UTF16ToString(name[:]) == "Chrome_RenderWidgetHostHWND" {
		index := int32(-16)
		style, _, _ := getAppWindowStyle.Call(hwnd, uintptr(index))
		if style&0x10000000 == 0 { // Ignore old, hidden renderers after navigation.
			return 1
		}
		var rect nativeRect
		nativeGetRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
		if rect.Right > rect.Left && rect.Bottom > rect.Top {
			rendererSearchHandle = hwnd
			return 0
		}
	}
	return 1
})

func rendererWindow(browser uintptr) uintptr {
	rendererSearchMu.Lock()
	defer rendererSearchMu.Unlock()
	rendererSearchHandle = 0
	nativeEnumChildren.Call(browser, findRendererWindow, 0)
	return rendererSearchHandle
}

func (n *nativeAppWindow) layout() error {
	if n.ready == nil {
		if visible, _, _ := appWindowVisible.Call(n.hwnd.Load()); visible == 0 {
			return nil
		}
	}
	if iconic, _, _ := nativeIsIconic.Call(n.hwnd.Load()); iconic != 0 {
		return nil
	}
	if !n.ownsBrowser() {
		return errors.New("网页窗口已关闭")
	}
	if err := embedBrowserStyle(n.browser); err != nil {
		return err
	}
	renderer := rendererWindow(n.browser)
	if renderer == 0 {
		return errors.New("等待网页内容窗口")
	}
	var browser, content, client nativeRect
	var origin nativePoint
	nativeGetRect.Call(n.browser, uintptr(unsafe.Pointer(&browser)))
	nativeGetRect.Call(renderer, uintptr(unsafe.Pointer(&content)))
	nativeGetClientRect.Call(n.hwnd.Load(), uintptr(unsafe.Pointer(&client)))
	nativeClientToScreen.Call(n.hwnd.Load(), uintptr(unsafe.Pointer(&origin)))
	left, top := content.Left-browser.Left, content.Top-browser.Top
	right, bottom := browser.Right-content.Right, browser.Bottom-content.Bottom
	if left < 0 || top < 0 || right < 0 || bottom < 0 || client.Right <= 0 || client.Bottom <= 0 {
		return errors.New("等待网页内容布局")
	}
	// Re-measure every time: DPI, browser versions, minimize/restore and page
	// navigation can all change the browser chrome or replace the renderer HWND.
	if !nativeContentAligned(content, client, origin) {
		setAppWindowPos.Call(n.browser, 0, uintptr(-left), uintptr(-top), uintptr(client.Right+left+right), uintptr(client.Bottom+top+bottom), windowNoZOrder|windowNoActivate|windowAsyncPosition)
		return errors.New("等待网页内容对齐")
	}
	return nil
}

func nativeContentAligned(content, client nativeRect, origin nativePoint) bool {
	// Chromium rounds DIP sizes independently at fractional scale factors. Its
	// right/bottom edges can differ by one physical pixel (e.g. 125%); requiring
	// exact equality makes resize oscillate forever. Never relax the top/left:
	// those must align exactly to exclude browser chrome and keep our toolbar.
	dw, dh := content.Right-content.Left-client.Right, content.Bottom-content.Top-client.Bottom
	return content.Left == origin.X && content.Top == origin.Y && dw >= -1 && dw <= 1 && dh >= -1 && dh <= 1
}

func (b *appWindow) showNativeWindow() error {
	if b.native != nil {
		if hwnd := b.native.ownedHandle(); hwnd != 0 && !b.native.closing.Load() {
			ok, _, err := postAppWindowMessage.Call(hwnd, nativeShow, 0, 0)
			if ok == 0 {
				return err
			}
			return nil
		}
		if err := b.closeNativeWindow(); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	var browser uintptr
	for browser == 0 && time.Now().Before(deadline) {
		browser = b.browserHandle()
		if browser == 0 {
			time.Sleep(40 * time.Millisecond)
		}
	}
	if browser == 0 {
		return errors.New("未找到 YuDesk 专用浏览器窗口")
	}
	ready := make(chan error, 1)
	n := &nativeAppWindow{browser: browser, browserPID: b.browserPID, done: make(chan struct{}), onClose: b.onClose, ready: ready, deadline: time.Now().Add(5 * time.Second)}
	b.native = n
	go n.run()
	select {
	case err := <-ready:
		return err
	case <-time.After(7 * time.Second):
		return errors.New("YuDesk 原生窗口启动超时")
	}
}

func (b *appWindow) closeNativeWindow() error {
	if b.native == nil {
		return nil
	}
	n := b.native
	n.closing.Store(true) // the timer also disposes if message delivery fails
	if err := waitNativeClose(n.done, 2*time.Second, n.postClose); err != nil {
		return err // retain ownership so cleanup can retry after terminating the private browser job
	}
	b.native = nil
	return nil
}

func (n *nativeAppWindow) postClose() error {
	if hwnd := n.ownedHandle(); hwnd != 0 {
		if ok, _, _ := postAppWindowMessage.Call(hwnd, nativeDispose, 0, 0); ok != 0 {
			return nil
		}
	}
	// An invalid/stale HWND must never receive a command. Signal our own thread
	// as a fallback; it owns the only DestroyWindow call.
	if thread := n.threadID.Load(); thread != 0 {
		if ok, _, err := nativePostThreadMessage.Call(uintptr(thread), nativeDispose, 0, 0); ok == 0 {
			return err
		}
		return nil
	}
	return errors.New("YuDesk 原生窗口线程已结束")
}

func waitNativeClose(done <-chan struct{}, timeout time.Duration, post func() error) error {
	select {
	case <-done:
		return nil
	default:
	}
	err := post()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil // destruction can race message delivery
	case <-timer.C:
		return errors.Join(errors.New("YuDesk 原生窗口关闭超时"), err)
	}
}

func (b *appWindow) minimizeNativeWindow() (bool, error) {
	if b.native == nil {
		return false, nil
	}
	hwnd := b.native.ownedHandle()
	if hwnd == 0 || b.native.closing.Load() {
		return true, errors.New("YuDesk 窗口已关闭")
	}
	ok, _, err := postAppWindowMessage.Call(hwnd, nativeMinimize, 0, 0)
	if ok == 0 {
		return true, err
	}
	return true, nil
}

func (b *appWindow) hideNativeWindow() (bool, error) {
	if b.native == nil {
		return false, nil
	}
	hwnd := b.native.ownedHandle()
	if hwnd == 0 || b.native.closing.Load() {
		return true, errors.New("YuDesk 窗口已关闭")
	}
	ok, _, err := postAppWindowMessage.Call(hwnd, nativeHide, 0, 0)
	if ok == 0 {
		return true, err
	}
	return true, nil
}
