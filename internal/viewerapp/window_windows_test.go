//go:build windows

package viewerapp

import (
	"net/http"
	"os"
	"testing"
	"time"
	"unsafe"
)

func TestNativeCaptionGeometry(t *testing.T) {
	if os.Getenv("YUDESK_NATIVE_WINDOW") != "1" {
		t.Skip("opt-in isolated native geometry, no input injection")
	}
	h, err := newViewerHost("127.0.0.1:0", "geometry-test")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.window.profile = t.TempDir()
	// Edge's storage utility can release its cache handle just after the main
	// browser process exits. Let that bounded asynchronous flush finish before
	// testing.TempDir's Windows deletion callback runs.
	t.Cleanup(func() { time.Sleep(2 * time.Second) })
	h.window.candidates = []string{os.Getenv("YUDESK_TEST_BROWSER")}
	l, _ := listenViewerPage("", h)
	attachViewerPage(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<title>YuDesk geometry test</title><p>Local test only</p>`))
	}))
	if err := h.window.Show(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	hwnd := h.window.nativeHandle()
	if hwnd == 0 {
		t.Fatal("no owned window")
	}
	var r struct{ Left, Top, Right, Bottom int32 }
	var p struct{ X, Y int32 }
	windowUser32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&r)))
	windowUser32.NewProc("ClientToScreen").Call(hwnd, uintptr(unsafe.Pointer(&p)))
	t.Logf("nonclient offset x=%d y=%d", p.X-r.Left, p.Y-r.Top)
	if p.Y-r.Top > 12 {
		t.Error("native caption still consumes height")
	}
	if err := h.window.Minimize(); err != nil {
		t.Fatal(err)
	}
	if h.ctx.Err() != nil {
		t.Fatal("minimize exited")
	}
	if err := h.window.Show(); err != nil {
		t.Fatal(err)
	}
	windowUser32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&r)))
	p.X, p.Y = 0, 0
	windowUser32.NewProc("ClientToScreen").Call(hwnd, uintptr(unsafe.Pointer(&p)))
	if p.Y-r.Top > 12 {
		t.Fatal("caption returned after restore")
	}
}
