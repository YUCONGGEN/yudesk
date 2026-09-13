package viewerapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const appWindowWidth, appWindowHeight = 860, 600

// Each local app owns a separate Chromium profile. No normal browser profile,
// unrelated tab, window or process is ever closed by these operations.
type appWindow struct {
	mu            sync.Mutex
	url, profile  string
	profileRoot   string
	candidates    []string // overridden only by native isolated-browser tests
	headless      bool
	browserPID    uint32
	processDone   chan struct{}
	stopBrowser   func() error // releases only this private renderer process group
	managed       atomic.Bool
	onClose       func()
	onUserClose   func()
	cancelMonitor func()
	monitorEpoch  uint64 // guarded by mu; retires stale monitor callbacks
	journal       *lifecycleJournal
	native        *nativeAppWindow
	shell         *windowShell
	useShell      bool
}

func newAppWindow(address, token string) *appWindow {
	return &appWindow{url: viewerUIURL(address, token), candidates: chromiumBrowsers(), useShell: runtime.GOOS == "darwin" || runtime.GOOS == "linux"}
}

func (b *appWindow) initProfile() error {
	if b.profile != "" {
		return nil
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(b.url))
	b.profileRoot = filepath.Join(root, "YuDesk", "browser", "app-"+hex.EncodeToString(hash[:8]))
	if err := os.MkdirAll(b.profileRoot, 0700); err != nil {
		return err
	}
	// Chromium records an unclean exit in its profile. If YuDesk is ended from
	// Task Manager and that profile is reused, Edge briefly restores its own
	// crash UI before the app window is embedded. UI settings and device history
	// live in Go, so every cold start can safely use a fresh renderer profile.
	b.profile, err = freshRendererProfile(b.profileRoot)
	return err
}

func freshRendererProfile(root string) (string, error) {
	// The single-instance lock is acquired before this function is reached, so
	// renderer-* directories here can only belong to an earlier stopped/crashed
	// YuDesk process. Best-effort removal prevents repeated forced exits from
	// accumulating complete browser caches on disk.
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "renderer-") {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
	return os.MkdirTemp(root, "renderer-")
}

func discardRendererProfile(root, profile string) {
	root, profile = filepath.Clean(root), filepath.Clean(profile)
	if root == "." || profile == "." || filepath.Dir(profile) != root || !strings.HasPrefix(filepath.Base(profile), "renderer-") {
		return
	}
	_ = os.RemoveAll(profile)
}

func browserProcessArgs(appURL, profile string, headless bool) []string {
	args := []string{"--app=" + appURL, "--user-data-dir=" + profile, "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1", "--no-first-run", "--disable-first-run-ui", "--no-default-browser-check", "--disable-background-mode", "--disable-session-crashed-bubble", "--hide-crash-restore-bubble", "--autoplay-policy=no-user-gesture-required", fmt.Sprintf("--window-size=%d,%d", appWindowWidth, appWindowHeight)}
	// Edge's desktop-media chooser cannot reliably hit-test its source cards
	// after the private app HWND is embedded in YuDesk's captionless Windows
	// host. The browser profile can only reach YuDesk's token-protected loopback
	// page, and capture is still started solely by the user's Share button, so
	// select the current screen directly instead of leaving an unusable dialog.
	if runtime.GOOS == "windows" && !headless {
		args = append(args, "--auto-select-screen-capture-source")
	}
	if headless {
		args = append(args, "--headless=new", "--disable-gpu")
	}
	return args
}

func (b *appWindow) connection() (*websocket.Conn, error) {
	data, err := os.ReadFile(filepath.Join(b.profile, "DevToolsActivePort"))
	if err != nil {
		return nil, err
	}
	parts := strings.Fields(string(data))
	if len(parts) != 2 {
		return nil, errors.New("invalid app browser endpoint")
	}
	port, err := strconv.Atoi(parts[0])
	if err != nil || port < 1024 || port > 65535 || !strings.HasPrefix(parts[1], "/devtools/browser/") || strings.ContainsAny(parts[1], "?#\\") {
		return nil, errors.New("invalid app browser endpoint")
	}
	// Never inherit a system HTTP/SOCKS proxy for local window management.
	dialer := websocket.Dialer{HandshakeTimeout: time.Second, NetDialContext: (&net.Dialer{Timeout: time.Second}).DialContext}
	c, _, err := dialer.Dial("ws://127.0.0.1:"+parts[0]+parts[1], nil)
	if c != nil {
		c.SetReadLimit(1 << 20)
	}
	return c, err
}

func windowCommand(c *websocket.Conn, method string, params any, output any) error {
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := c.WriteJSON(map[string]any{"id": 1, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		var response struct {
			ID     int
			Result json.RawMessage
			Error  *struct{ Message string }
		}
		if err := c.ReadJSON(&response); err != nil {
			return err
		}
		if response.ID != 1 {
			continue
		}
		if response.Error != nil {
			return errors.New(response.Error.Message)
		}
		if output != nil {
			return json.Unmarshal(response.Result, output)
		}
		return nil
	}
}

func windowSessionCommand(c *websocket.Conn, sessionID, method string, params any, output any) error {
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := c.WriteJSON(map[string]any{"id": 2, "method": method, "params": params, "sessionId": sessionID}); err != nil {
		return err
	}
	for {
		var response struct {
			ID        int
			SessionID string `json:"sessionId"`
			Result    json.RawMessage
			Error     *struct{ Message string }
		}
		if err := c.ReadJSON(&response); err != nil {
			return err
		}
		if response.ID != 2 || (response.SessionID != "" && response.SessionID != sessionID) {
			continue
		}
		if response.Error != nil {
			return errors.New(response.Error.Message)
		}
		if output != nil {
			return json.Unmarshal(response.Result, output)
		}
		return nil
	}
}

// Chromium creates its renderer HWND before stylesheets and fonts are ready.
// Keep the native host hidden until the complete document is drawable, or a
// cold/crash restart can expose one unstyled white frame.
func waitWindowDocumentReady(c *websocket.Conn, targetID string) error {
	var attached struct{ SessionID string }
	if err := windowCommand(c, "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, &attached); err != nil {
		return err
	}
	if attached.SessionID == "" {
		return errors.New("missing app browser session")
	}
	defer func() {
		_ = windowCommand(c, "Target.detachFromTarget", map[string]any{"sessionId": attached.SessionID}, nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var result struct {
			Result struct{ Value bool }
		}
		if err := windowSessionCommand(c, attached.SessionID, "Runtime.evaluate", map[string]any{
			"expression":    `document.readyState === "complete" && Array.from(document.querySelectorAll('link[rel~="stylesheet"]')).every(link => !!link.sheet)`,
			"returnByValue": true,
		}, &result); err != nil {
			return err
		}
		if result.Result.Value {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("YuDesk 页面资源加载超时")
}

type windowTarget struct{ TargetID, Type, URL string }

func (b *appWindow) target(c *websocket.Conn) (string, error) {
	var result struct{ TargetInfos []windowTarget }
	if err := windowCommand(c, "Target.getTargets", nil, &result); err != nil {
		return "", err
	}
	want, _ := url.Parse(b.url)
	for _, target := range result.TargetInfos {
		actual, err := url.Parse(target.URL)
		if err == nil && target.Type == "page" && actual.Scheme == want.Scheme && actual.Host == want.Host && actual.Query().Get("access_token") == want.Query().Get("access_token") {
			return target.TargetID, nil
		}
	}
	return "", errWindowTargetMissing
}

func (b *appWindow) Show() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.useShell && !b.headless {
		return b.showShell()
	}
	if err := b.initProfile(); err != nil {
		return err
	}
	if c, err := b.connection(); err == nil {
		id, err := b.target(c)
		if err == nil {
			// Activating a minimized Chromium target alone does not restore it.
			staged := b.stageNativeBrowser()
			if b.native == nil && !staged {
				var state struct{ Bounds struct{ WindowState string } }
				if windowCommand(c, "Browser.getWindowForTarget", map[string]any{"targetId": id}, &state) == nil && state.Bounds.WindowState == "minimized" {
					_ = setAppWindowState(c, id, "normal")
				}
			}
			err = windowCommand(c, "Target.activateTarget", map[string]any{"targetId": id}, nil)
			if err == nil {
				err = waitWindowDocumentReady(c, id)
			}
			c.Close()
			if err != nil {
				return err
			}
			return b.finishShow(id)
		}
		// Chromium can remain alive after its final app target closes. Starting
		// --app into that empty instance is not reliable (notably in headless
		// and background mode); finish only this private instance first.
		b.stopMonitor()
		b.closeNativeWindow()
		_ = windowCommand(c, "Browser.close", nil, nil)
		c.Close()
		if b.processDone != nil {
			select {
			case <-b.processDone:
			case <-time.After(3 * time.Second):
			}
		}
		if b.stopBrowser != nil {
			b.stopBrowser()
			b.stopBrowser = nil
		}
	}
	var started bool
	if b.profileRoot == "" {
		b.profileRoot = b.profile
	}
	// A replaced renderer gets fresh browser cache/lock files. Connection
	// history and settings belong to Go, not Chromium's profile. This avoids
	// racing a recently exited browser's storage/AV handles on hide/reopen on
	// platforms without an in-process native host (and headless fixtures).
	if b.processDone != nil {
		// A dead renderer can leave its captionless host HWND alive. Retire that
		// host before starting a replacement; otherwise showNativeWindow would
		// reuse a valid HWND that still points at the old browser process.
		if err := b.closeNativeWindow(); err != nil {
			if b.stopBrowser != nil {
				_ = b.stopBrowser()
				b.stopBrowser = nil
			}
			if retryErr := b.closeNativeWindow(); retryErr != nil {
				return errors.Join(err, retryErr)
			}
		}
		if b.stopBrowser != nil {
			if err := b.stopBrowser(); err != nil {
				return err
			}
			b.stopBrowser = nil
		}
		fresh, err := freshRendererProfile(b.profileRoot)
		if err != nil {
			return err
		}
		b.profile = fresh
	}
	for _, candidate := range b.candidates {
		resolved, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		args := browserProcessArgs(b.url, b.profile, b.headless)
		pid, done, guard, err := startBrowserProcess(resolved, args)
		if err != nil {
			continue
		}
		if b.stopBrowser != nil {
			b.stopBrowser()
		}
		b.stopBrowser = guard
		b.browserPID = pid
		b.processDone = done
		started = true
		break
	}
	if !started {
		return errors.New("请安装 Microsoft Edge、Google Chrome 或 Chromium 以打开 YuDesk 窗口")
	}
	deadline := time.Now().Add(8 * time.Second)
	var startupErr error
	for time.Now().Before(deadline) {
		if c, err := b.connection(); err == nil {
			id, err := b.target(c)
			if err == nil {
				staged := b.stageNativeBrowser()
				var result struct{ WindowID int }
				if windowCommand(c, "Browser.getWindowForTarget", map[string]any{"targetId": id}, &result) == nil {
					// Windows embeds this hidden top-level HWND into YuDesk's native
					// frame. Restoring it through CDP first produces a one-frame
					// Chromium/title-bar flash on a cold restart.
					if !staged {
						_ = windowCommand(c, "Browser.setWindowBounds", map[string]any{"windowId": result.WindowID, "bounds": map[string]any{"windowState": "normal"}}, nil)
					}
					_ = windowCommand(c, "Browser.setWindowBounds", map[string]any{"windowId": result.WindowID, "bounds": map[string]any{"width": appWindowWidth, "height": appWindowHeight}}, nil)
				}
				if err = waitWindowDocumentReady(c, id); err == nil {
					c.Close()
					return b.finishShow(id)
				}
				startupErr = err
			}
			startupErr = err
			c.Close()
		} else {
			startupErr = err
		}
		time.Sleep(80 * time.Millisecond)
	}
	return fmt.Errorf("YuDesk 窗口启动超时，请重新双击打开: %w", startupErr)
}

// Every entry point (launch, tray, duplicate launch and hide/reopen) must prepare
// the native frame before reporting success. Page transitions keep this host.
func (b *appWindow) finishShow(id string) error {
	if !b.headless {
		if err := b.showNativeWindow(); err != nil {
			b.stopMonitor()
			b.closeNativeWindow()
			if b.stopBrowser != nil {
				b.stopBrowser()
				b.stopBrowser = nil
			}
			b.browserPID = 0
			return err
		}
	}
	return b.monitor(id)
}

func setAppWindowState(c *websocket.Conn, id, state string) error {
	var result struct{ WindowID int }
	if err := windowCommand(c, "Browser.getWindowForTarget", map[string]any{"targetId": id}, &result); err != nil {
		return err
	}
	return windowCommand(c, "Browser.setWindowBounds", map[string]any{"windowId": result.WindowID, "bounds": map[string]any{"windowState": state}}, nil)
}

// Minimize retains the target and lifecycle watcher; unlike Hide, it does not
// destroy the renderer or interrupt the active remote session.
func (b *appWindow) Minimize() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.shell != nil {
		return b.shell.send("minimize", "")
	}
	if handled, err := b.minimizeNativeWindow(); handled {
		return err
	}
	if b.profile == "" {
		return errors.New("当前页面不是 YuDesk 独立窗口")
	}
	c, err := b.connection()
	if err != nil {
		return err
	}
	defer c.Close()
	id, err := b.target(c)
	if err != nil {
		return err
	}
	return setAppWindowState(c, id, "minimized")
}

// Fullscreen changes only the local YuDesk shell. The page applies its own
// matching layout after this succeeds, so remote input and session state stay
// in the same renderer instead of opening another window.
func (b *appWindow) Fullscreen(enabled bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.shell != nil {
		action := "exit-fullscreen"
		if enabled {
			action = "enter-fullscreen"
		}
		return b.shell.send(action, "")
	}
	if handled, err := b.fullscreenNativeWindow(enabled); handled {
		return err
	}
	if b.profile == "" {
		return errors.New("当前页面不是 YuDesk 独立窗口")
	}
	c, err := b.connection()
	if err != nil {
		return err
	}
	defer c.Close()
	id, err := b.target(c)
	if err != nil {
		return err
	}
	if enabled {
		return setAppWindowState(c, id, "fullscreen")
	}
	if err = setAppWindowState(c, id, "normal"); err != nil {
		return err
	}
	var result struct{ WindowID int }
	if err = windowCommand(c, "Browser.getWindowForTarget", map[string]any{"targetId": id}, &result); err != nil {
		return err
	}
	return windowCommand(c, "Browser.setWindowBounds", map[string]any{"windowId": result.WindowID, "bounds": map[string]any{"width": appWindowWidth, "height": appWindowHeight}}, nil)
}

// Windows hides the actual owned host and keeps its renderer/session intact.
// Its destruction watcher remains active, so a renderer crash cannot leave a
// hidden, unresponsive Go app. Platforms without a host close only their UI.
func (b *appWindow) Hide() error {
	b.mu.Lock()
	if b.shell != nil {
		err := b.shell.send("hide", "")
		b.mu.Unlock()
		return err
	}
	handled, err := b.hideNativeWindow()
	b.mu.Unlock()
	if handled {
		return err
	}
	return b.Close()
}

// Exit disposes the native host and only our dedicated browser process group.
func (b *appWindow) Close() (result error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.shell != nil {
		b.shell.close()
		b.shell = nil
	}
	b.stopMonitor()
	nativeErr := b.closeNativeWindow()
	defer func() {
		b.browserPID = 0
		if b.stopBrowser != nil {
			result = errors.Join(result, b.stopBrowser())
			b.stopBrowser = nil
		}
		discardRendererProfile(b.profileRoot, b.profile)
	}()
	if b.profile == "" {
		return nativeErr
	} // no managed window in headless/CLI mode
	c, err := b.connection()
	if err != nil {
		return nativeErr
	} // already closed
	defer c.Close()
	// An empty private profile is also ours; Browser.close cannot touch the
	// user's ordinary browser because --user-data-dir is app-specific.
	err = windowCommand(c, "Browser.close", nil, nil)
	_ = c.Close()
	if b.processDone != nil {
		select {
		case <-b.processDone:
		case <-time.After(3 * time.Second):
		}
	}
	// The OS-owned process group below also closes storage/crash helpers that
	// outlive the browser's main process; it never includes the user's profile.
	if b.stopBrowser != nil {
		return nativeErr
	}
	return errors.Join(err, nativeErr)
}

func openBrowser(address string, _ bool) error {
	u, err := url.Parse(address)
	if err != nil {
		return err
	}
	u.Path = "/api/ui/show"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	var lastErr error
	for {
		r, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
		if requestErr != nil {
			return requestErr
		}
		response, requestErr := client.Do(r)
		if response != nil {
			_ = response.Body.Close()
		}
		if requestErr == nil && response.StatusCode == http.StatusOK {
			return nil
		}
		if requestErr == nil {
			lastErr = fmt.Errorf("无法打开 YuDesk 窗口（%d）", response.StatusCode)
			// Authentication and malformed requests are permanent. A busy host or
			// a renderer being rebuilt is transient and should be retried.
			if response.StatusCode != http.StatusServiceUnavailable && response.StatusCode != http.StatusTooManyRequests {
				return lastErr
			}
		} else {
			lastErr = requestErr
		}
		timer := time.NewTimer(80 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("无法恢复 YuDesk 窗口: %w", errors.Join(lastErr, ctx.Err()))
		case <-timer.C:
		}
	}
}
