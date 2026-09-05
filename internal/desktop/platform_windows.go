//go:build windows

package desktop

import (
	"bytes"
	"fmt"
	"image"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	gdi32                = syscall.NewLazyDLL("gdi32.dll")
	getDC                = user32.NewProc("GetDC")
	releaseDC            = user32.NewProc("ReleaseDC")
	getSystemMetrics     = user32.NewProc("GetSystemMetrics")
	setProcessDPIAware   = user32.NewProc("SetProcessDPIAware")
	setProcessDPIContext = user32.NewProc("SetProcessDpiAwarenessContext")
	sendInput            = user32.NewProc("SendInput")
	createCompatibleDC   = gdi32.NewProc("CreateCompatibleDC")
	createDIBSection     = gdi32.NewProc("CreateDIBSection")
	selectObject         = gdi32.NewProc("SelectObject")
	bitBlt               = gdi32.NewProc("BitBlt")
	stretchBlt           = gdi32.NewProc("StretchBlt")
	setStretchBltMode    = gdi32.NewProc("SetStretchBltMode")
	deleteObject         = gdi32.NewProc("DeleteObject")
	deleteDC             = gdi32.NewProc("DeleteDC")
)

func init() {
	// Without DPI awareness Windows virtualizes SM_CXSCREEN/SM_CYSCREEN on
	// scaled displays. BitBlt then captures only that logical rectangle, which
	// cuts off the right and bottom of the real desktop. Prefer per-monitor V2
	// and retain the older system-aware API for supported Windows 7/8 hosts.
	if err := setProcessDPIContext.Find(); err == nil {
		if ok, _, _ := setProcessDPIContext.Call(^uintptr(3)); ok != 0 { // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
			return
		}
	}
	_, _, _ = setProcessDPIAware.Call()
}

type bitmapInfoHeader struct {
	Size, Width, Height                                                         int32
	Planes, BitCount                                                            uint16
	Compression, SizeImage, XPelsPerMeter, YPelsPerMeter, ClrUsed, ClrImportant uint32
}
type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

func capturePlatform(options CaptureOptions) (Screenshot, error) {
	sourceW, sourceH := metric(0), metric(1)
	if sourceW <= 0 || sourceH <= 0 {
		return Screenshot{}, fmt.Errorf("invalid screen size")
	}
	w, h := sourceW, sourceH
	if options.MaxWidth > 0 && w > options.MaxWidth {
		w = options.MaxWidth
		h = max(1, sourceH*w/sourceW)
	}
	hdc, _, _ := getDC.Call(0)
	if hdc == 0 {
		return Screenshot{}, fmt.Errorf("GetDC failed")
	}
	defer releaseDC.Call(0, hdc)
	mem, _, _ := createCompatibleDC.Call(hdc)
	if mem == 0 {
		return Screenshot{}, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer deleteDC.Call(mem)
	bmi := bitmapInfo{Header: bitmapInfoHeader{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32, Compression: 0, SizeImage: uint32(w * h * 4)}}
	var pixelAddress unsafe.Pointer
	bmp, _, createErr := createDIBSection.Call(hdc, uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&pixelAddress)), 0, 0)
	if bmp == 0 {
		return Screenshot{}, fmt.Errorf("CreateDIBSection failed: %v", createErr)
	}
	defer deleteObject.Call(bmp)
	oldObject, _, _ := selectObject.Call(mem, bmp)
	if oldObject == 0 {
		return Screenshot{}, fmt.Errorf("SelectObject failed")
	}
	defer selectObject.Call(mem, oldObject)
	if w == sourceW && h == sourceH {
		if r, _, _ := bitBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), hdc, 0, 0, 0x40CC0020); r == 0 {
			return Screenshot{}, fmt.Errorf("BitBlt failed")
		}
	} else {
		_, _, _ = setStretchBltMode.Call(mem, 3) // COLORONCOLOR: fast real-time scaling.
		if r, _, _ := stretchBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), hdc, 0, 0, uintptr(sourceW), uintptr(sourceH), 0x40CC0020); r == 0 {
			return Screenshot{}, fmt.Errorf("StretchBlt failed")
		}
	}
	selectObject.Call(mem, oldObject)
	if pixelAddress == nil {
		return Screenshot{}, fmt.Errorf("CreateDIBSection returned no pixel memory")
	}
	pixels := unsafe.Slice((*byte)(pixelAddress), w*h*4)
	for i := 0; i < len(pixels); i += 4 {
		pixels[i], pixels[i+2] = pixels[i+2], pixels[i]
		pixels[i+3] = 255
	}
	img := &image.RGBA{Pix: pixels, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	options.MaxWidth = 0
	shot, err := encodeScreen(img, options)
	shot.SourceWidth, shot.SourceHeight = sourceW, sourceH
	return shot, err
}

func applyInputPlatform(events []InputEvent) error {
	for _, e := range events {
		in := winInput{}
		switch e.Type {
		case "move":
			w, h := metric(0), metric(1)
			mi := mouseInput{DX: absolutePixel(e.X, w), DY: absolutePixel(e.Y, h), Flags: 0x0001 | 0x8000}
			*(*mouseInput)(unsafe.Pointer(&in.Data[0])) = mi
		case "down", "up":
			in.Type = 0
			flags := mouseButtonFlags(e.Button, e.Type == "up")
			if flags == 0 {
				return fmt.Errorf("unsupported mouse button %d", e.Button)
			}
			*(*mouseInput)(unsafe.Pointer(&in.Data[0])) = mouseInput{Flags: flags}
		case "wheel":
			in.Type = 0
			*(*mouseInput)(unsafe.Pointer(&in.Data[0])) = mouseInput{MouseData: uint32(int32(-e.DeltaY)), Flags: 0x0800}
		case "key", "key_down", "key_up":
			in.Type = 1
			ki := keyboardInput{VK: uint16(keyCode(e.Key))}
			if ki.VK == 0 {
				return fmt.Errorf("unsupported key %q", e.Key)
			}
			if e.Type == "key_up" {
				ki.Flags = 0x0002
			}
			*(*keyboardInput)(unsafe.Pointer(&in.Data[0])) = ki
			if e.Type == "key" {
				if n, _, _ := sendInput.Call(1, uintptr(unsafe.Pointer(&in)), 40); n != 1 {
					return fmt.Errorf("SendInput failed")
				}
				ki.Flags = 0x0002
				*(*keyboardInput)(unsafe.Pointer(&in.Data[0])) = ki
			}
		default:
			return fmt.Errorf("unknown input event %q", e.Type)
		}
		if n, _, _ := sendInput.Call(1, uintptr(unsafe.Pointer(&in)), 40); n != 1 {
			return fmt.Errorf("SendInput failed")
		}
	}
	return nil
}

type mouseInput struct {
	DX, DY                 int32
	MouseData, Flags, Time uint32
	Extra                  uintptr
}
type keyboardInput struct {
	VK, Scan    uint16
	Flags, Time uint32
	Extra       uintptr
}
type winInput struct {
	Type uint32
	_    uint32
	Data [32]byte
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func metric(index uintptr) int { r, _, _ := getSystemMetrics.Call(index); return int(r) }
func keyCode(s string) int {
	if len(s) == 1 {
		c := s[0]
		if c >= 'a' && c <= 'z' {
			return int(c-'a') + 0x41
		}
		if c >= 'A' && c <= 'Z' {
			return int(c)
		}
		if c >= '0' && c <= '9' {
			return int(c-'0') + 0x30
		}
	}
	keys := map[string]int{"Enter": 0x0D, "Escape": 0x1B, "Backspace": 0x08, "Tab": 0x09, " ": 0x20, "Space": 0x20, "ArrowLeft": 0x25, "ArrowUp": 0x26, "ArrowRight": 0x27, "ArrowDown": 0x28, "Control": 0x11, "Shift": 0x10, "Alt": 0x12, "Meta": 0x5B, "Delete": 0x2E, "Insert": 0x2D, "Home": 0x24, "End": 0x23, "PageUp": 0x21, "PageDown": 0x22}
	if v := keys[s]; v != 0 {
		return v
	}
	if len(s) >= 2 && s[0] == 'F' {
		var n int
		if _, err := fmt.Sscanf(s, "F%d", &n); err == nil && n >= 1 && n <= 24 {
			return 0x6F + n
		}
	}
	return 0
}

func clipboardGetPlatform() (string, error) {
	b, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Get-Clipboard -Raw").Output()
	return strings.TrimSuffix(string(b), "\r\n"), err
}
func clipboardSetPlatform(value string) error {
	c := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$input | Set-Clipboard")
	c.Stdin = bytes.NewBufferString(value)
	return c.Run()
}
