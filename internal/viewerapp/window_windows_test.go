//go:build windows

package viewerapp

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestNativeCloseFailurePaths(t *testing.T) {
	t.Run("already destroyed", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		if err := waitNativeClose(done, time.Second, func() error { t.Fatal("must not message a stale HWND"); return nil }); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("post failure racing destruction", func(t *testing.T) {
		done := make(chan struct{})
		if err := waitNativeClose(done, time.Second, func() error { close(done); return syscall.Errno(1400) }); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("post failure without destruction is bounded", func(t *testing.T) {
		start := time.Now()
		err := waitNativeClose(make(chan struct{}), 20*time.Millisecond, func() error { return syscall.ERROR_ACCESS_DENIED })
		if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) || time.Since(start) > time.Second {
			t.Fatalf("unbounded or lost delivery error: %v", err)
		}
	})
	t.Run("invalid handle cannot target another window", func(t *testing.T) {
		n := &nativeAppWindow{}
		n.hwnd.Store(^uintptr(0))
		if n.ownedHandle() != 0 || n.postClose() == nil {
			t.Fatal("invalid handle accepted")
		}
	})
	t.Run("startup failure closes thread", func(t *testing.T) {
		ready := make(chan error, 1)
		n := &nativeAppWindow{done: make(chan struct{}), ready: ready}
		go n.run()
		select {
		case err := <-ready:
			if err == nil {
				t.Fatal("invalid browser accepted")
			}
		case <-time.After(time.Second):
			t.Fatal("startup did not fail")
		}
		select {
		case <-n.done:
		case <-time.After(time.Second):
			t.Fatal("failed host leaked a thread")
		}
	})
	t.Run("exit releases private job even if host close times out", func(t *testing.T) {
		done := make(chan struct{})
		released := false
		b := &appWindow{native: &nativeAppWindow{done: done}, stopBrowser: func() error { released = true; close(done); return nil }}
		if err := b.Close(); err == nil {
			t.Fatal("missing native cleanup error")
		}
		if !released || b.stopBrowser != nil {
			t.Fatal("failed native cleanup leaked the private process job")
		}
		if err := b.closeNativeWindow(); err != nil || b.native != nil {
			t.Fatalf("could not finish cleanup: %v", err)
		}
		if err := b.Close(); err != nil {
			t.Fatalf("repeated cleanup: %v", err)
		}
	})
}

// A real browser is opt-in. All pages, processes and profiles belong to this
// fixture; no remote connection, real input injection or user profile is used.
func nativeWindowFixture(t *testing.T) *viewerHost {
	t.Helper()
	if os.Getenv("YUDESK_NATIVE_WINDOW") != "1" || os.Getenv("YUDESK_TEST_BROWSER") == "" {
		t.Skip("opt-in isolated Chromium window, no input injection")
	}
	h, err := newViewerHost("127.0.0.1:0", "caption-regression")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := os.MkdirTemp("", "yudesk-caption-test-")
	if err != nil {
		h.Close()
		t.Fatal(err)
	}
	h.window.profile = profile
	h.window.candidates = []string{os.Getenv("YUDESK_TEST_BROWSER")}
	t.Cleanup(func() {
		pid := h.window.browserPID
		if err := h.window.Close(); err != nil {
			t.Errorf("owned browser cleanup: %v", err)
		}
		if h.window.processDone != nil {
			select {
			case <-h.window.processDone:
			case <-time.After(3 * time.Second):
				handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
				if err != nil {
					t.Errorf("owned browser wait did not finish; pid=%d query=%v", pid, err)
				} else {
					var code uint32
					queryErr := windows.GetExitCodeProcess(handle, &code)
					result, waitErr := windows.WaitForSingleObject(handle, 0)
					windows.CloseHandle(handle)
					t.Errorf("owned browser wait did not finish; pid=%d exit=%d query=%v wait=%d/%v", pid, code, queryErr, result, waitErr)
				}
			}
		}
		h.Close()
		// Chromium storage/AV handles can outlive Browser.close briefly.
		until := time.Now().Add(12 * time.Second)
		for {
			err := os.RemoveAll(profile)
			if err == nil {
				break
			}
			if time.Now().After(until) {
				// The private job has been verified empty above. Security scanners
				// can still briefly hold browser cache files after process exit;
				// this cache cleanup is not an active renderer/process leak.
				t.Logf("temporary fixture cache cleanup deferred: %v", err)
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	l, err := listenViewerPage("", h)
	if err != nil {
		t.Fatal(err)
	}
	attachViewerPage(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><title>YuDesk · %s</title><style>body{margin:0}header{height:40px;background:green}main{height:2000px}</style><header><button>隐藏</button><button>最小化</button><button>关闭</button></header><main>Local fixture only</main>`, r.URL.Path)
	}))
	return h
}

func waitNative(t *testing.T, label string, check func() bool) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if check() {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatal(label)
}

func assertNativeContent(t *testing.T, b *appWindow) {
	t.Helper()
	var content, client nativeRect
	var origin nativePoint
	waitNative(t, "web viewport must fill host client area within one physical pixel (no browser titlebar and no clipped app toolbar)", func() bool {
		if b.native == nil {
			return false
		}
		renderer := rendererWindow(b.native.browser)
		if renderer == 0 {
			return false
		}
		nativeGetRect.Call(renderer, uintptr(unsafe.Pointer(&content)))
		nativeGetClientRect.Call(b.nativeHandle(), uintptr(unsafe.Pointer(&client)))
		origin = nativePoint{}
		nativeClientToScreen.Call(b.nativeHandle(), uintptr(unsafe.Pointer(&origin)))
		dw, dh := content.Right-content.Left-client.Right, content.Bottom-content.Top-client.Bottom
		return content.Left == origin.X && content.Top == origin.Y && dw >= -1 && dw <= 1 && dh >= -1 && dh <= 1 && client.Right > 100 && client.Bottom > 100
	})
	index := int32(-16)
	style, _, _ := getAppWindowStyle.Call(b.nativeHandle(), uintptr(index))
	if style&windowCaption != 0 || style&windowResize != 0 || style&windowMaximizeBox != 0 || style&windowMinimizeBox == 0 {
		t.Fatalf("host style %#x must be captionless and fixed-size, with minimization only", style)
	}
	var outer nativeRect
	nativeGetRect.Call(b.nativeHandle(), uintptr(unsafe.Pointer(&outer)))
	if origin.X != outer.Left || origin.Y != outer.Top || client.Right != outer.Right-outer.Left || client.Bottom != outer.Bottom-outer.Top {
		t.Fatalf("native frame or edge remains: window=%+v client=%+v origin=%+v", outer, client, origin)
	}
	if parent, _, _ := nativeGetParent.Call(b.native.browser); parent != b.nativeHandle() {
		t.Fatal("browser is no longer clipped by its owned host")
	}
	t.Logf("viewport=%dx%d, origin=(%d,%d), host style=%#x", client.Right, client.Bottom, origin.X, origin.Y, style)
}

func TestNativeFramelessTransitions(t *testing.T) {
	h := nativeWindowFixture(t)
	b := h.window
	if err := b.Show(); err != nil {
		t.Fatal(err)
	}
	assertNativeContent(t, b)
	initial := b.nativeHandle()
	if err := b.Show(); err != nil {
		t.Fatal(err)
	}
	if b.nativeHandle() != initial {
		t.Fatal("duplicate show replaced host")
	}
	for _, path := range []string{"/connecting", "/session", "/return", "/"} {
		c, err := b.connection()
		if err != nil {
			t.Fatal(err)
		}
		id, err := b.target(c)
		if err != nil {
			t.Fatal(err)
		}
		// Navigate only the fixture target; no UI input or real remote session.
		var target struct{ SessionID string }
		if err := windowCommand(c, "Target.attachToTarget", map[string]any{"targetId": id}, &target); err != nil {
			t.Fatal(err)
		}
		message := fmt.Sprintf(`{"id":2,"method":"Page.navigate","params":{"url":%q}}`, strings.Replace(b.url, "/?", path+"?", 1))
		if err := windowCommand(c, "Target.sendMessageToTarget", map[string]any{"sessionId": target.SessionID, "message": message}, nil); err != nil {
			t.Fatal(err)
		}
		c.Close()
		waitNative(t, "fixture navigation did not complete", func() bool {
			var title [128]uint16
			windowUser32.NewProc("GetWindowTextW").Call(b.native.browser, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
			return syscall.UTF16ToString(title[:]) == "YuDesk · "+path
		})
		assertNativeContent(t, b)
		if err := b.Minimize(); err != nil {
			t.Fatal(err)
		}
		waitNative(t, "host did not minimize", func() bool { v, _, _ := nativeIsIconic.Call(initial); return v != 0 })
		if h.ctx.Err() != nil {
			t.Fatal("minimize stopped the application")
		}
		if err := b.Show(); err != nil {
			t.Fatal(err)
		}
		waitNative(t, "host did not restore", func() bool { v, _, _ := nativeIsIconic.Call(initial); return v == 0 })
		assertNativeContent(t, b)
	}
	var fixed nativeRect
	nativeGetRect.Call(initial, uintptr(unsafe.Pointer(&fixed)))
	if err := b.Fullscreen(true); err != nil {
		t.Fatal(err)
	}
	monitor, _, _ := nativeMonitorFromWindow.Call(initial, 2)
	monitorInfo := nativeMonitorInfo{Size: uint32(unsafe.Sizeof(nativeMonitorInfo{}))}
	if monitor == 0 {
		t.Fatal("fullscreen host has no monitor")
	}
	if ok, _, err := nativeGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&monitorInfo))); ok == 0 {
		t.Fatal(err)
	}
	waitNative(t, "host did not occupy its complete monitor", func() bool {
		var current nativeRect
		nativeGetRect.Call(initial, uintptr(unsafe.Pointer(&current)))
		return current == monitorInfo.Monitor
	})
	assertNativeContent(t, b)
	if err := b.Fullscreen(false); err != nil {
		t.Fatal(err)
	}
	waitNative(t, "host did not restore its fixed bounds after fullscreen", func() bool {
		var current nativeRect
		nativeGetRect.Call(initial, uintptr(unsafe.Pointer(&current)))
		return current == fixed
	})
	assertNativeContent(t, b)
	var limits nativeMinMaxInfo
	windowUser32.NewProc("SendMessageW").Call(initial, 0x0024, 0, uintptr(unsafe.Pointer(&limits)))
	width, height := fixed.Right-fixed.Left, fixed.Bottom-fixed.Top
	if limits.MinTrackSize != (nativePoint{width, height}) || limits.MaxTrackSize != (nativePoint{width, height}) {
		t.Fatalf("interactive size must remain %dx%d, limits=%+v", width, height, limits)
	}
	postAppWindowMessage.Call(initial, 0x0112, 0xf030, 0) // SC_MAXIMIZE
	time.Sleep(150 * time.Millisecond)
	if v, _, _ := windowUser32.NewProc("IsZoomed").Call(initial); v != 0 {
		t.Fatal("fixed host accepted maximize")
	}
	var afterMaximize nativeRect
	nativeGetRect.Call(initial, uintptr(unsafe.Pointer(&afterMaximize)))
	if afterMaximize != fixed {
		t.Fatalf("maximize changed fixed bounds: before=%+v after=%+v", fixed, afterMaximize)
	}
	assertNativeContent(t, b)
	// Emulate Chromium recomputing its frame after a display/theme transition.
	index := int32(-16)
	style, _, _ := getAppWindowStyle.Call(b.native.browser, uintptr(index))
	setAppWindowStyle.Call(b.native.browser, uintptr(index), style|windowCaption)
	setAppWindowPos.Call(b.native.browser, 0, 0, 0, 0, 0, 0x0037|windowAsyncPosition)
	waitNative(t, "browser caption style was not repaired", func() bool {
		s, _, _ := getAppWindowStyle.Call(b.native.browser, uintptr(index))
		return s&windowCaption == 0
	})
	assertNativeContent(t, b)
	h.tracker.Suspend()
	old := b.native
	if err := b.Hide(); err != nil {
		t.Fatal(err)
	}
	waitNative(t, "hide must remove window without disposing its renderer", func() bool {
		visible, _, _ := appWindowVisible.Call(initial)
		return visible == 0 && old.hwnd.Load() == initial
	})
	if h.ctx.Err() != nil {
		t.Fatal("hide exited application")
	}
	if err := b.Show(); err != nil {
		t.Fatal(err)
	}
	assertNativeContent(t, b)
	if b.native != old {
		t.Fatal("hide/reopen must reuse its live host")
	}
	pid := b.browserPID
	for i := 0; i < 10; i++ {
		if err := b.Hide(); err != nil {
			t.Fatal(err)
		}
		waitNative(t, "hidden window remains visible", func() bool { v, _, _ := appWindowVisible.Call(initial); return v == 0 })
		if err := b.Show(); err != nil {
			t.Fatal(err)
		}
		waitNative(t, "restored window remains hidden", func() bool { v, _, _ := appWindowVisible.Call(initial); return v != 0 })
		if b.native != old || b.browserPID != pid {
			t.Fatal("restore restarted renderer")
		}
		assertNativeContent(t, b)
	}
	postAppWindowMessage.Call(b.nativeHandle(), 0x10, 0, 0) // owned WM_CLOSE, no injected input
	select {
	case <-h.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("native close did not exit")
	}
}

func TestNativeCaptionGeometry(t *testing.T) {
	h := nativeWindowFixture(t)
	if err := h.window.Show(); err != nil {
		t.Fatal(err)
	}
	// A zero ClientToScreen top inset alone is insufficient: Chromium paints
	// its caption inside that client area. Verify the actual renderer too.
	assertNativeContent(t, h.window)
	if err := h.window.Minimize(); err != nil {
		t.Fatal(err)
	}
	if h.ctx.Err() != nil {
		t.Fatal("minimize exited")
	}
	if err := h.window.Show(); err != nil {
		t.Fatal(err)
	}
	assertNativeContent(t, h.window)
}
