//go:build windows

package viewerapp

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

//go:embed ui/yudesk.ico
var trayICO []byte

var trayShell = syscall.NewLazyDLL("shell32.dll").NewProc("Shell_NotifyIconW")
var trayRegister = windowUser32.NewProc("RegisterClassExW")
var trayUnregister = windowUser32.NewProc("UnregisterClassW")
var trayCreate = windowUser32.NewProc("CreateWindowExW")
var trayDestroy = windowUser32.NewProc("DestroyWindow")
var trayDefProc = windowUser32.NewProc("DefWindowProcW")
var trayGetMessage = windowUser32.NewProc("GetMessageW")
var trayDispatch = windowUser32.NewProc("DispatchMessageW")
var trayQuit = windowUser32.NewProc("PostQuitMessage")
var trayIconCreate = windowUser32.NewProc("CreateIconFromResourceEx")
var trayIconDestroy = windowUser32.NewProc("DestroyIcon")
var trayRegisterMessage = windowUser32.NewProc("RegisterWindowMessageW")
var trayCreateMenu = windowUser32.NewProc("CreatePopupMenu")
var trayAppendMenu = windowUser32.NewProc("AppendMenuW")
var trayDestroyMenu = windowUser32.NewProc("DestroyMenu")
var trayCursor = windowUser32.NewProc("GetCursorPos")
var trayForeground = windowUser32.NewProc("SetForegroundWindow")
var trayTrackMenu = windowUser32.NewProc("TrackPopupMenu")
var trayModule = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
var traySetTimer = windowUser32.NewProc("SetTimer")
var trayKillTimer = windowUser32.NewProc("KillTimer")
var trayKeyState = windowUser32.NewProc("GetAsyncKeyState")

const trayCallback = 0x8001
const trayVisibility = 0x8002

type trayData struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Timeout             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                [16]byte
	BalloonIcon         uintptr
}
type trayClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type trayMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}
type appTray struct {
	hwnd           atomic.Uintptr
	wanted         atomic.Bool
	showing        atomic.Bool
	done           chan struct{}
	onShow, onExit func()
	data           trayData // owned exclusively by the message thread
	added          bool
	taskbarCreated uint32
	shortcutTimer  bool
	shortcutDown   bool
	closing        bool
}

var trayWindows sync.Map
var trayWindowProc = syscall.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	value, ok := trayWindows.Load(hwnd)
	if ok {
		t := value.(*appTray)
		switch msg {
		case trayVisibility:
			t.updateVisibility()
			return 0
		case 0x113: // WM_TIMER
			if wp == 1 && !t.wanted.Load() {
				// Check only these three key states while hidden. No keyboard
				// hook, keystroke logging, text capture or input suppression.
				ctrl, _, _ := trayKeyState.Call(0x11)
				y, _, _ := trayKeyState.Call(0x59)
				u, _, _ := trayKeyState.Call(0x55)
				down := ctrl&y&u&0x8000 != 0
				if down && !t.shortcutDown {
					t.show()
				}
				t.shortcutDown = down
			}
			return 0
		case trayCallback:
			switch uint32(lp) {
			case 0x203: // WM_LBUTTONDBLCLK
				t.show()
			case 0x205, 0x7B: // WM_RBUTTONUP / WM_CONTEXTMENU
				t.menu()
			}
			return 0
		case 0x10: // WM_CLOSE
			t.closing = true
			trayKillTimer.Call(hwnd, 1)
			t.wanted.Store(false)
			t.updateVisibility()
			trayDestroy.Call(hwnd)
			return 0
		case 0x02: // WM_DESTROY
			trayQuit.Call(0)
			return 0
		}
		if msg == t.taskbarCreated {
			t.added = false
			t.updateVisibility() // Explorer restart must not lose a visible tray icon.
			return 0
		}
	}
	r, _, _ := trayDefProc.Call(hwnd, uintptr(msg), wp, lp)
	return r
})

func newAppTray(show, exit func()) (*appTray, error) {
	t := &appTray{done: make(chan struct{}), onShow: show, onExit: exit}
	t.wanted.Store(true)
	ready := make(chan error, 1)
	go t.run(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return t, nil
}

func (t *appTray) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.done)
	instance, _, _ := trayModule.Call(0)
	name, _ := syscall.UTF16PtrFromString("YuDesk.NotificationWindow")
	class := trayClass{Proc: trayWindowProc, Instance: instance, Name: name}
	class.Size = uint32(unsafe.Sizeof(class))
	atom, _, err := trayRegister.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		ready <- err
		return
	}
	defer trayUnregister.Call(uintptr(unsafe.Pointer(name)), instance)
	// Invisible top-level window, not HWND_MESSAGE: receives TaskbarCreated.
	hwnd, _, err := trayCreate.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if hwnd == 0 {
		ready <- err
		return
	}
	t.hwnd.Store(hwnd)
	trayWindows.Store(hwnd, t)
	defer trayWindows.Delete(hwnd)
	defer t.hwnd.Store(0)
	icon, err := makeTrayIcon()
	if err != nil {
		trayDestroy.Call(hwnd)
		ready <- err
		return
	}
	defer trayIconDestroy.Call(icon)
	t.data = trayData{Window: hwnd, ID: 1, Flags: 7, Callback: trayCallback, Icon: icon}
	t.data.Size = uint32(unsafe.Sizeof(t.data))
	copy(t.data.Tip[:], syscall.StringToUTF16("YuDesk · 双击显示，右键退出"))
	restart, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	id, _, _ := trayRegisterMessage.Call(uintptr(unsafe.Pointer(restart)))
	t.taskbarCreated = uint32(id)
	t.updateVisibility()
	if !t.added {
		trayDestroy.Call(hwnd)
		ready <- errors.New("无法添加 YuDesk 托盘图标")
		return
	}
	ready <- nil
	var msg trayMessage
	for {
		status, _, _ := trayGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(status) <= 0 {
			break
		}
		trayDispatch.Call(uintptr(unsafe.Pointer(&msg)))
	}
	t.closing = true
	t.wanted.Store(false)
	t.updateVisibility()
}

func makeTrayIcon() (uintptr, error) {
	if len(trayICO) < 6 {
		return 0, errors.New("missing Yu icon")
	}
	n := int(binary.LittleEndian.Uint16(trayICO[4:6]))
	for i := 0; i < n; i++ {
		entry := 6 + 16*i
		if entry+16 > len(trayICO) {
			break
		}
		if trayICO[entry] != 32 {
			continue
		}
		size := uint64(binary.LittleEndian.Uint32(trayICO[entry+8:]))
		offset := uint64(binary.LittleEndian.Uint32(trayICO[entry+12:]))
		if size == 0 || offset+size > uint64(len(trayICO)) {
			break
		}
		icon, _, err := trayIconCreate.Call(uintptr(unsafe.Pointer(&trayICO[offset])), uintptr(size), 1, 0x30000, 32, 32, 0)
		if icon == 0 {
			return 0, err
		}
		return icon, nil
	}
	return 0, errors.New("invalid Yu icon")
}

func (t *appTray) updateVisibility() {
	if !t.closing && !t.wanted.Load() && !t.shortcutTimer {
		timer, _, _ := traySetTimer.Call(t.hwnd.Load(), 1, 30, 0)
		t.shortcutTimer = timer != 0
		t.shortcutDown = false
	} else if (t.closing || t.wanted.Load()) && t.shortcutTimer {
		trayKillTimer.Call(t.hwnd.Load(), 1)
		t.shortcutTimer = false
		t.shortcutDown = false
	}
	if !t.closing && t.wanted.Load() {
		if !t.added {
			ok, _, _ := trayShell.Call(0, uintptr(unsafe.Pointer(&t.data)))
			t.added = ok != 0
		}
	} else if t.added {
		trayShell.Call(2, uintptr(unsafe.Pointer(&t.data)))
		t.added = false
	}
}
func (t *appTray) Visible(visible bool) {
	if t == nil {
		return
	}
	t.wanted.Store(visible)
	if hwnd := t.hwnd.Load(); hwnd != 0 {
		postAppWindowMessage.Call(hwnd, trayVisibility, 0, 0)
	}
}
func (t *appTray) Close() {
	if t == nil {
		return
	}
	if hwnd := t.hwnd.Load(); hwnd != 0 {
		postAppWindowMessage.Call(hwnd, 0x10, 0, 0)
	}
	<-t.done
}
func (t *appTray) show() {
	if !t.showing.CompareAndSwap(false, true) {
		return
	}
	go func() { defer t.showing.Store(false); t.onShow() }()
}
func (t *appTray) menu() {
	menu, _, _ := trayCreateMenu.Call()
	if menu == 0 {
		return
	}
	defer trayDestroyMenu.Call(menu)
	show, _ := syscall.UTF16PtrFromString("显示 YuDesk")
	exit, _ := syscall.UTF16PtrFromString("退出")
	trayAppendMenu.Call(menu, 0, 1, uintptr(unsafe.Pointer(show)))
	trayAppendMenu.Call(menu, 0, 2, uintptr(unsafe.Pointer(exit)))
	var point struct{ X, Y int32 }
	trayCursor.Call(uintptr(unsafe.Pointer(&point)))
	hwnd := t.hwnd.Load()
	trayForeground.Call(hwnd)
	command, _, _ := trayTrackMenu.Call(menu, 0x102, uintptr(point.X), uintptr(point.Y), 0, hwnd, 0) // RETURNCMD | RIGHTBUTTON
	postAppWindowMessage.Call(hwnd, 0, 0, 0)
	if command == 1 {
		t.show()
	} else if command == 2 {
		go t.onExit()
	}
}
