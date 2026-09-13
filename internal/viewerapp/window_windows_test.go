//go:build windows

package viewerapp

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var startupWindowScan struct {
	sync.Mutex
	pids []uint32
}

var recordVisibleTopLevelChromium = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
	visible, _, _ := appWindowVisible.Call(hwnd)
	parent, _, _ := nativeGetParent.Call(hwnd)
	if visible == 0 || parent != 0 {
		return 1
	}
	var name [128]uint16
	appWindowClass.Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	if syscall.UTF16ToString(name[:]) != "Chrome_WidgetWin_1" {
		return 1
	}
	var pid uint32
	appWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	startupWindowScan.Lock()
	startupWindowScan.pids = append(startupWindowScan.pids, pid)
	startupWindowScan.Unlock()
	return 1
})

func scanVisibleTopLevelChromium() {
	enumAppWindows.Call(recordVisibleTopLevelChromium, 0)
}

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

func TestNativeColdStartDoesNotFlashRawChromium(t *testing.T) {
	h := nativeWindowFixture(t)
	showWithoutRawChromium(t, h.window)
	assertNativeContent(t, h.window)
}

func showWithoutRawChromium(t *testing.T, b *appWindow) {
	t.Helper()
	startupWindowScan.Lock()
	startupWindowScan.pids = nil
	startupWindowScan.Unlock()

	shown := make(chan error, 1)
	go func() { shown <- b.Show() }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case err := <-shown:
			if err != nil {
				t.Fatal(err)
			}
			scanVisibleTopLevelChromium()
			startupWindowScan.Lock()
			seen := append([]uint32(nil), startupWindowScan.pids...)
			startupWindowScan.Unlock()
			for _, pid := range seen {
				if pid == b.browserPID {
					t.Fatalf("raw Chromium window for pid %d became visible before YuDesk's native frame was ready", pid)
				}
			}
			return
		case <-ticker.C:
			scanVisibleTopLevelChromium()
		case <-timeout.C:
			t.Fatal("YuDesk cold-start window timed out")
		}
	}
}

func TestNativeRestartAfterRendererExitDoesNotFlash(t *testing.T) {
	h := nativeWindowFixture(t)
	first := h.window
	showWithoutRawChromium(t, first)
	profile := first.profile
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// A new appWindow with the same URL and browser profile models a complete
	// YuDesk process restart, including recently-released profile lock files.
	restarted := newAppWindow(h.listener.Addr().String(), "caption-regression")
	restarted.profile = profile
	restarted.candidates = []string{os.Getenv("YUDESK_TEST_BROWSER")}
	restarted.onClose = func() {}
	t.Cleanup(func() {
		if err := restarted.Close(); err != nil {
			t.Errorf("restarted browser cleanup: %v", err)
		}
	})
	showWithoutRawChromium(t, restarted)
	assertNativeContent(t, restarted)
}

func TestNativeRendererCrashRecoversInSameProcess(t *testing.T) {
	h := nativeWindowFixture(t)
	b := h.window
	showWithoutRawChromium(t, b)
	oldNative, oldPID := b.native, b.browserPID
	b.mu.Lock()
	stop := b.stopBrowser
	b.mu.Unlock()
	if stop == nil {
		t.Fatal("native browser has no process guard")
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	waitNative(t, "renderer failure did not hide recoverable host", func() bool {
		visible, _, _ := appWindowVisible.Call(oldNative.ownedHandle())
		return visible == 0
	})
	if h.ctx.Err() != nil {
		t.Fatal("renderer failure terminated YuDesk singleton")
	}
	showWithoutRawChromium(t, b)
	if b.browserPID == 0 || b.browserPID == oldPID {
		t.Fatalf("renderer was not replaced: old=%d new=%d", oldPID, b.browserPID)
	}
	if b.native == nil || b.native == oldNative {
		t.Fatal("stale native host was reused for replacement renderer")
	}
	assertNativeContent(t, b)
}

func TestNativeColdStartWaitsForPageStylesBeforeShowing(t *testing.T) {
	h := nativeWindowFixture(t)
	b := h.window
	cssStarted := make(chan struct{})
	releaseCSS := make(chan struct{})
	var signaled sync.Once
	l, err := listenViewerPage("", h)
	if err != nil {
		t.Fatal(err)
	}
	attachViewerPage(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow.css" {
			signaled.Do(func() { close(cssStarted) })
			<-releaseCSS
			w.Header().Set("Content-Type", "text/css")
			_, _ = w.Write([]byte("body{background:#fff;color:#123}"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<link rel="stylesheet" href="/slow.css?access_token=caption-regression"><main>styled fixture</main>`))
	}))

	shown := make(chan error, 1)
	go func() { shown <- b.Show() }()
	select {
	case <-cssStarted:
	case err := <-shown:
		t.Fatalf("window finished before requesting its stylesheet: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("browser did not request the fixture stylesheet")
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		visible := false
		if b.native != nil {
			if hwnd := b.native.ownedHandle(); hwnd != 0 {
				state, _, _ := appWindowVisible.Call(hwnd)
				visible = state != 0
			}
		}
		if visible {
			close(releaseCSS)
			t.Fatal("YuDesk host became visible while page styles were still loading")
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(releaseCSS)
	select {
	case err := <-shown:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("styled YuDesk window did not appear")
	}
	assertNativeContent(t, b)
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
	waitNative(t, "native close did not hide to tray", func() bool {
		visible, _, _ := appWindowVisible.Call(initial)
		return visible == 0
	})
	if h.ctx.Err() != nil {
		t.Fatal("native close stopped the application")
	}
	if b.native != old || b.browserPID != pid {
		t.Fatal("native close replaced the live renderer")
	}
	if err := h.showWindow(); err != nil {
		t.Fatal(err)
	}
	waitNative(t, "desktop relaunch did not restore the existing window", func() bool {
		visible, _, _ := appWindowVisible.Call(initial)
		return visible != 0
	})
	assertNativeContent(t, b)
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

// This opt-in regression opens only an isolated local fixture. It verifies
// that Windows capture resolves instead of hanging in Edge's unclickable
// embedded picker, then immediately stops every local capture track.
func TestNativeScreenShareBypassesEmbeddedPicker(t *testing.T) {
	if os.Getenv("YUDESK_NATIVE_SHARE_PICKER") != "1" {
		t.Skip("opt-in native display-media capture regression")
	}
	h := nativeWindowFixture(t)
	b := h.window
	if err := b.Show(); err != nil {
		t.Fatal(err)
	}
	c, err := b.connection()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id, err := b.target(c)
	if err != nil {
		t.Fatal(err)
	}
	var attached struct{ SessionID string }
	if err := windowCommand(c, "Target.attachToTarget", map[string]any{"targetId": id, "flatten": true}, &attached); err != nil {
		t.Fatal(err)
	}
	defer windowCommand(c, "Target.detachFromTarget", map[string]any{"sessionId": attached.SessionID}, nil)
	if err := windowSessionCommand(c, attached.SessionID, "Runtime.evaluate", map[string]any{
		"expression":    `window.__displayMediaResult = "pending"; window.__displayMedia = navigator.mediaDevices.getDisplayMedia({video:true,audio:true,systemAudio:"include",windowAudio:"system",selfBrowserSurface:"exclude"}).then(stream => { window.__displayMediaResult = "selected:" + stream.getVideoTracks().length + ":" + stream.getAudioTracks().length; for (const track of stream.getTracks()) track.stop(); }, error => { window.__displayMediaResult = error.name + ": " + error.message; })`,
		"returnByValue": true,
		"userGesture":   true,
	}, nil); err != nil {
		t.Fatal(err)
	}
	var captureStatus string
	deadline := time.Now().Add(5 * time.Second)
	for captureStatus == "" || captureStatus == "pending" {
		var status struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if err := windowSessionCommand(c, attached.SessionID, "Runtime.evaluate", map[string]any{
			"expression":    `window.__displayMediaResult`,
			"returnByValue": true,
		}, &status); err != nil {
			t.Fatal(err)
		}
		captureStatus = status.Result.Value
		if captureStatus != "" && captureStatus != "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("screen capture remained pending in Edge's embedded picker: %q", captureStatus)
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !strings.HasPrefix(captureStatus, "selected:1:") {
		t.Fatalf("screen capture did not start: %q", captureStatus)
	}
	t.Logf("display-media result=%q", captureStatus)
}
