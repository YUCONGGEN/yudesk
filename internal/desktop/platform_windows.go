//go:build windows

package desktop

import (
	"fmt"
	"image"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/yudesk/yudesk/internal/winhost"
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
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	leave, err := enterCaptureDesktop()
	if err != nil {
		if !winhost.IsWorker() && winhost.Status().Installed {
			return captureThroughService(options)
		}
		return Screenshot{}, err
	}
	defer leave()
	sourceW, sourceH := metric(0), metric(1)
	if sourceW <= 0 || sourceH <= 0 {
		return Screenshot{}, fmt.Errorf("invalid screen size")
	}
	w, h := sourceW, sourceH
	if options.MaxWidth > 0 && w > options.MaxWidth {
		w = options.MaxWidth
		h = max(1, sourceH*w/sourceW)
	}
	// The protected desktop IPC has a 32 MiB frame bound, including portrait
	// and unusually tall displays. Preserve aspect ratio and source geometry.
	if winhost.IsWorker() {
		for w*h > 8388608 {
			w = max(1, w*9/10)
			h = max(1, sourceH*w/sourceW)
		}
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
	if options.Raw {
		img := ownedBGRA(pixels, w, h, options.Buffer)
		return Screenshot{Pixels: img, Width: w, Height: h, SourceWidth: sourceW, SourceHeight: sourceH}, nil
	}
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
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	leave, err := enterCaptureDesktop()
	if err != nil {
		if !winhost.IsWorker() && winhost.Status().Installed {
			_, e := winhost.Call("input", struct{ Events []InputEvent }{events})
			return e
		}
		return err
	}
	defer leave()
	for _, e := range events {
		w, h := 0, 0
		if e.Type == "move" {
			w, h = metric(0), metric(1)
		}
		inputs, err := buildWindowsInput(e, w, h)
		if err != nil {
			return err
		}
		if err := injectWindowsInput(inputs, sendWindowsInput); err != nil {
			return err
		}
	}
	return nil
}

func sendWindowsInput(inputs []winInput) (int, error) {
	if len(inputs) == 0 {
		return 0, nil
	}
	n, _, err := sendInput.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(winInput{}))
	if err == syscall.Errno(0) {
		err = nil
	}
	return int(n), err
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func metric(index uintptr) int { r, _, _ := getSystemMetrics.Call(index); return int(r) }

func configureDesktopCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
