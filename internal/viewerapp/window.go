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
	candidates    []string // overridden only by native isolated-browser tests
	headless      bool
	browserPID    uint32
	processDone   chan struct{}
	stopBrowser   func() // releases only this private renderer process group
	managed       atomic.Bool
	onClose       func()
	cancelMonitor func()
}

func newAppWindow(address, token string) *appWindow {
	return &appWindow{url: viewerUIURL(address, token), candidates: chromiumBrowsers()}
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
	b.profile = filepath.Join(root, "YuDesk", "browser", "app-"+hex.EncodeToString(hash[:8]))
	return os.MkdirAll(b.profile, 0700)
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
	return "", errors.New("app window has not opened")
}

// Observe the actual owned target, not an abandoned HTTP fetch. Chromium may
// retain an unconsumed fetch after its tab closes, which makes X detection late.
func (b *appWindow) monitor(id string) error {
	b.stopMonitor()
	c, err := b.connection()
	if err != nil {
		return err
	}
	if err = windowCommand(c, "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		c.Close()
		return err
	}
	_ = c.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	b.cancelMonitor = func() { cancel(); _ = c.Close() }
	b.managed.Store(true)
	go func() {
		defer c.Close()
		for {
			var event struct {
				Method string
				Params struct{ TargetID string }
			}
			err := c.ReadJSON(&event)
			if ctx.Err() != nil {
				return
			}
			if err != nil || event.Method == "Target.targetDestroyed" && event.Params.TargetID == id {
				if b.onClose != nil {
					b.onClose()
				}
				return
			}
		}
	}()
	return nil
}

func (b *appWindow) stopMonitor() {
	if b.cancelMonitor != nil {
		b.cancelMonitor()
		b.cancelMonitor = nil
	}
}

func (b *appWindow) Show() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.initProfile(); err != nil {
		return err
	}
	if c, err := b.connection(); err == nil {
		id, err := b.target(c)
		if err == nil {
			// Activating a minimized Chromium target alone does not restore it.
			var state struct{ Bounds struct{ WindowState string } }
			if windowCommand(c, "Browser.getWindowForTarget", map[string]any{"targetId": id}, &state) == nil && state.Bounds.WindowState == "minimized" {
				_ = setAppWindowState(c, id, "normal")
			}
			err = windowCommand(c, "Target.activateTarget", map[string]any{"targetId": id}, nil)
			c.Close()
			if err != nil {
				return err
			}
			return b.monitor(id)
		}
		// Chromium can remain alive after its final app target closes. Starting
		// --app into that empty instance is not reliable (notably in headless
		// and background mode); finish only this private instance first.
		b.stopMonitor()
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
	for _, candidate := range b.candidates {
		resolved, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		args := []string{"--app=" + b.url, "--user-data-dir=" + b.profile, "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1", "--no-first-run", "--disable-first-run-ui", "--no-default-browser-check", "--disable-background-mode", fmt.Sprintf("--window-size=%d,%d", appWindowWidth, appWindowHeight)}
		if b.headless {
			args = append(args, "--headless=new", "--disable-gpu")
		}
		cmd := exec.Command(resolved, args...)
		configureBrowserProcess(cmd)
		if cmd.Start() != nil {
			continue
		}
		guard, err := newBrowserGuard(cmd)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			continue
		}
		if b.stopBrowser != nil {
			b.stopBrowser()
		}
		b.stopBrowser = guard
		done := make(chan struct{})
		b.browserPID = uint32(cmd.Process.Pid)
		b.processDone = done
		go func() { _ = cmd.Wait(); close(done) }() // only the child we started
		started = true
		break
	}
	if !started {
		return errors.New("请安装 Microsoft Edge、Google Chrome 或 Chromium 以打开 YuDesk 窗口")
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := b.connection(); err == nil {
			id, err := b.target(c)
			if err == nil {
				var result struct{ WindowID int }
				if windowCommand(c, "Browser.getWindowForTarget", map[string]any{"targetId": id}, &result) == nil {
					_ = windowCommand(c, "Browser.setWindowBounds", map[string]any{"windowId": result.WindowID, "bounds": map[string]any{"windowState": "normal"}}, nil)
					_ = windowCommand(c, "Browser.setWindowBounds", map[string]any{"windowId": result.WindowID, "bounds": map[string]any{"width": appWindowWidth, "height": appWindowHeight}}, nil)
				}
				c.Close()
				if !b.headless {
					b.removeNativeCaption()
				}
				return b.monitor(id)
			}
			c.Close()
		}
		time.Sleep(80 * time.Millisecond)
	}
	return errors.New("YuDesk 窗口启动超时，请重新双击打开")
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

// Close the private renderer, not the Go app. Reopening recreates the same UI
// using the existing app state. This really removes the window and taskbar item.
func (b *appWindow) Hide() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopMonitor()
	defer func() {
		if b.stopBrowser != nil {
			b.stopBrowser()
			b.stopBrowser = nil
		}
	}()
	if b.profile == "" {
		return nil
	} // no managed window in headless/CLI mode
	c, err := b.connection()
	if err != nil {
		return nil
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
		b.stopBrowser()
		b.stopBrowser = nil
		return nil
	}
	return err
}

func openBrowser(address string, _ bool) error {
	u, err := url.Parse(address)
	if err != nil {
		return err
	}
	u.Path = "/api/ui/show"
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 12 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("无法打开 YuDesk 窗口（%d）", response.StatusCode)
	}
	return nil
}
