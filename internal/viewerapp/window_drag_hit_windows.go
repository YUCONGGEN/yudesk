//go:build windows

package viewerapp

import (
	"errors"
	"math"
	"slices"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var dragHitClassName = syscall.StringToUTF16Ptr("YuDesk.DragHit")
var dragHitOnce sync.Once
var dragHitErr error
var dragHitProc = syscall.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	if msg == 0x0201 { // WM_LBUTTONDOWN: our own thread, actual local input
		stamp, _, _ := windowUser32.NewProc("GetMessageTime").Call()
		now, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetTickCount").Call()
		if uint32(now)-uint32(stamp) > 500 {
			return 0
		}
		parent, _, _ := nativeGetParent.Call(hwnd)
		if value, ok := nativeWindows.Load(parent); ok {
			n := value.(*nativeAppWindow)
			regions := n.dragRegions.Load()
			if regions != nil && time.Since(regions.received) < 3*time.Second {
				point := nativePoint{int32(int16(lp & 0xffff)), int32(int16((lp >> 16) & 0xffff))}
				nativeClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&point)))
				n.dragWindowAt(&point)
			}
		}
		return 0
	}
	if msg == 0x0021 {
		return 3
	} // MA_NOACTIVATE: leave web keyboard focus intact
	r, _, _ := trayDefProc.Call(hwnd, uintptr(msg), wp, lp)
	return r
})

func (n *nativeAppWindow) dragFromClient(point nativePoint) {
	regions := n.dragRegions.Load()
	if regions == nil || time.Since(regions.received) >= 3*time.Second {
		return
	}
	var client nativeRect
	hwnd := n.ownedHandle()
	if hwnd == 0 {
		return
	}
	nativeGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	if math.Abs(regions.Width*regions.Scale-float64(client.Right)) > 2 || math.Abs(regions.Height*regions.Scale-float64(client.Bottom)) > 2 {
		return
	}
	x, y := float64(point.X)/regions.Scale, float64(point.Y)/regions.Scale
	for _, r := range regions.Rects {
		if x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height {
			nativeClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&point)))
			n.dragWindowAt(&point)
			return
		}
	}
}

func (b *appWindow) SetDragRegions(value *windowDragRegions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.native == nil || b.native.closing.Load() {
		return errors.New("window not ready")
	}
	hwnd := b.native.ownedHandle()
	if hwnd == 0 {
		return errors.New("window not ready")
	}
	b.native.dragRegions.Store(value)
	if ok, _, err := postAppWindowMessage.Call(hwnd, nativeDragRegionsChanged, 0, 0); ok == 0 {
		return err
	}
	return nil
}

func (n *nativeAppWindow) layoutDragRegions() {
	hwnd := n.ownedHandle()
	if hwnd == 0 || n.closing.Load() {
		return
	}
	regions := n.dragRegions.Load()
	var client nativeRect
	nativeGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	visible, _, _ := appWindowVisible.Call(hwnd)
	iconic, _, _ := nativeIsIconic.Call(hwnd)
	valid := regions != nil && time.Since(regions.received) < 3*time.Second && visible != 0 && iconic == 0
	if valid {
		// Never let pre-resize/native-fullscreen rectangles cover new controls.
		valid = math.Abs(regions.Width*regions.Scale-float64(client.Right)) <= 2 && math.Abs(regions.Height*regions.Scale-float64(client.Bottom)) <= 2
	}
	count := 0
	if valid {
		count = len(regions.Rects)
	} else {
		regions = nil
	}
	previous := n.lastDragLayout
	if regions == nil && previous == nil {
		return
	}
	if regions != nil && previous != nil && regions.Width == previous.Width && regions.Height == previous.Height && regions.Scale == previous.Scale && slices.Equal(regions.Rects, previous.Rects) {
		return
	}
	instance, _, _ := trayModule.Call(0)
	dragHitOnce.Do(func() {
		cursor, _, _ := windowUser32.NewProc("LoadCursorW").Call(0, 32512)
		class := trayClass{Proc: dragHitProc, Instance: instance, Name: dragHitClassName, Cursor: cursor, Background: 6}
		class.Size = uint32(unsafe.Sizeof(class))
		if ok, _, err := trayRegister.Call(uintptr(unsafe.Pointer(&class))); ok == 0 {
			dragHitErr = err
		}
	})
	if dragHitErr != nil {
		return
	}
	for len(n.dragChildren) < count {
		// Alpha 1/255 keeps the existing web header visible, while only these
		// non-interactive rectangles receive input. No global hooks/subclassing
		// Chromium, and the OS destroys the children with their owned parent.
		child, _, _ := trayCreate.Call(0x00080000, uintptr(unsafe.Pointer(dragHitClassName)), 0, windowChild, 0, 0, 1, 1, hwnd, 0, instance, 0)
		if child == 0 {
			return
		}
		windowUser32.NewProc("SetLayeredWindowAttributes").Call(child, 0, 1, 2)
		n.dragChildren = append(n.dragChildren, child)
	}
	for i, child := range n.dragChildren {
		if i >= count {
			nativeShowWindow.Call(child, 0)
			continue
		}
		r := regions.Rects[i]
		x, y := math.Ceil(r.X*regions.Scale), math.Ceil(r.Y*regions.Scale)
		w, h := math.Floor((r.X+r.Width)*regions.Scale)-x, math.Floor((r.Y+r.Height)*regions.Scale)-y
		setAppWindowPos.Call(child, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), windowNoActivate|0x0040)
	}
	n.lastDragLayout = regions
}
