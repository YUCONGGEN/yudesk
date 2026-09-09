//go:build windows

package viewerapp

import "unsafe"

// Separate policy from the Win32 boundary for deterministic race/ownership
// regressions without injecting input into the user's desktop.
type windowDragOps struct {
	valid   func() bool
	pressed func() bool
	cursor  func() (nativePoint, bool)
	capture func() uintptr
	owned   func(uintptr) bool
	cancel  func(uintptr)
	release func()
	start   func(nativePoint)
}

func beginWindowDrag(ops windowDragOps) bool {
	if !ops.valid() || !ops.pressed() {
		return false // A late HTTP request must not start a drag after mouse-up.
	}
	point, ok := ops.cursor()
	if !ok {
		return false
	}
	if capture := ops.capture(); capture != 0 {
		if !ops.owned(capture) {
			return false // Never cancel another application's mouse operation.
		}
		ops.cancel(capture)
		if capture = ops.capture(); capture != 0 {
			if !ops.owned(capture) {
				return false
			}
			ops.release()
		}
		if ops.capture() != 0 {
			return false
		}
	}
	if !ops.valid() || !ops.pressed() {
		return false
	}
	ops.start(point)
	return true
}

func packWindowPoint(p nativePoint) uintptr {
	return uintptr(uint32(uint16(p.X)) | uint32(uint16(p.Y))<<16)
}

func (n *nativeAppWindow) dragWindow() {
	n.dragWindowAt(nil)
}

func (n *nativeAppWindow) dragWindowAt(origin *nativePoint) {
	hwnd := n.ownedHandle()
	if hwnd == 0 {
		return
	}
	beginWindowDrag(windowDragOps{
		valid: func() bool {
			visible, _, _ := appWindowVisible.Call(hwnd)
			iconic, _, _ := nativeIsIconic.Call(hwnd)
			return !n.closing.Load() && n.ownedHandle() == hwnd && visible != 0 && iconic == 0
		},
		pressed: func() bool {
			// A native mouse-down uses queue state: GetAsyncKeyState may
			// already describe a later mouse-up still queued for the move loop.
			proc := trayKeyState
			if origin != nil {
				proc = windowUser32.NewProc("GetKeyState")
			}
			state, _, _ := proc.Call(1)
			return state&0x8000 != 0
		},
		cursor: func() (nativePoint, bool) {
			var point nativePoint
			ok, _, _ := windowUser32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&point)))
			return point, ok != 0
		},
		capture: func() uintptr { capture, _, _ := windowUser32.NewProc("GetCapture").Call(); return capture },
		owned: func(capture uintptr) bool {
			child, _, _ := windowUser32.NewProc("IsChild").Call(hwnd, capture)
			return capture == hwnd || child != 0
		},
		cancel: func(capture uintptr) {
			// Chromium gets WM_CANCELMODE on its own thread and releases its
			// native capture. Bound the cross-process call if its UI is hung.
			var result uintptr
			windowUser32.NewProc("SendMessageTimeoutW").Call(capture, 0x001f, 0, 0, 0x0003, 50, uintptr(unsafe.Pointer(&result)))
		},
		release: func() { windowUser32.NewProc("ReleaseCapture").Call() },
		start: func(point nativePoint) {
			if origin != nil {
				point = *origin
			}
			// Standard non-client caption drag, even though the host has no
			// system caption. USER32 owns snapping and multi-monitor movement.
			trayDefProc.Call(hwnd, 0x0112, 0xf012, packWindowPoint(point)) // WM_SYSCOMMAND, SC_MOVE | HTCAPTION
		},
	})
}
