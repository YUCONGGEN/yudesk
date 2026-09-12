package viewerapp

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yudesk/yudesk/internal/uilifecycle"
)

// One local HTTP listener and one browser tracker live for the entire process,
// including relay dialing and failed attempts. Only the active page handler
// changes. Closing a connecting window therefore cancels the dial as well.
type viewerHost struct {
	ctx      context.Context
	cancel   context.CancelFunc
	listener net.Listener
	server   *http.Server
	mu       sync.RWMutex
	windowMu sync.Mutex // serializes show/hide/exit together with tray state
	page     *viewerPageListener
	handler  http.Handler
	desk     *unifiedDesk
	version  atomic.Value
	tracker  *uilifecycle.Tracker
	window   *appWindow
	tray     *appTray
	openUI   bool
	journal  *lifecycleJournal
	lastShow time.Time
}

const duplicateShowWindow = 750 * time.Millisecond

func newViewerHost(addr, token string, journals ...*lifecycleJournal) (*viewerHost, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &viewerHost{ctx: ctx, cancel: cancel, listener: l}
	if len(journals) > 0 {
		h.journal = journals[0]
	}
	h.window = newAppWindow(l.Addr().String(), token)
	h.window.journal = h.journal
	h.version.Store("desktop-input-files-v3")
	// Closing a renderer unexpectedly still exits, so a browser crash cannot
	// leave an invisible process. Windows' owned native X has a separate callback
	// below and only hides the healthy window to the notification area.
	windowClosed := func() { h.requestExit("window_closed") }
	tracker := uilifecycle.NewWithCallback(700*time.Millisecond, func() {
		// Managed native windows report target destruction directly, avoiding
		// duplicate/late HTTP watcher callbacks after the dashboard reopens.
		if !h.window.managed.Load() {
			h.journal.record("browser_watch_closed", nil)
			windowClosed()
		}
	})
	h.window.onClose = windowClosed
	h.window.onUserClose = func() {
		h.journal.record("window_closed_to_tray", nil)
		if err := h.closeToTray(); err != nil {
			h.journal.record("window_close_to_tray_failed", err)
		}
	}
	h.tracker = tracker
	h.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	h.server.Handler = localSecurityHeaders(requireAccessToken(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveBrandAsset(w, r) {
			return
		}
		if r.URL.Path == "/assets/icon.svg" {
			serveAppIcon(w, r)
			return
		}
		if r.URL.Path == "/api/ui/show" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", 405)
				return
			}
			if err := h.showWindow(); err != nil {
				http.Error(w, err.Error(), 503)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/api/ui/close-to-tray" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if err := h.closeToTray(); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/api/ui/drag-regions" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", 405)
				return
			}
			regions, err := readWindowDragRegions(http.MaxBytesReader(w, r.Body, 8192))
			if err != nil {
				http.Error(w, "invalid drag regions", 400)
				return
			}
			if err = h.window.SetDragRegions(regions); err != nil {
				http.Error(w, err.Error(), 503)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/api/ui/fullscreen" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var command struct {
				Enabled bool `json:"enabled"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF {
				http.Error(w, "invalid fullscreen command", http.StatusBadRequest)
				return
			}
			if err := h.window.Fullscreen(command.Enabled); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/api/ui/minimize" || r.URL.Path == "/api/ui/drag" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var err error
			if r.URL.Path == "/api/ui/minimize" {
				err = h.minimizeWindow()
			} else {
				err = h.window.Drag()
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/api/ui/watch" {
			h.mu.RLock()
			desk := h.desk
			h.mu.RUnlock()
			if desk != nil {
				desk.hidden.Store(false)
			}
			tracker.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/ui/version" {
			_, _ = io.WriteString(w, h.version.Load().(string))
			return
		}
		h.mu.RLock()
		desk := h.desk
		h.mu.RUnlock()
		if desk != nil && desk.serve(w, r) {
			return
		}
		if (r.URL.Path == "/api/exit" || r.URL.Path == "/exit") && r.Method == http.MethodPost {
			// No exit confirmation/result page, and no navigation to a dead URL.
			w.WriteHeader(http.StatusNoContent)
			_ = http.NewResponseController(w).Flush()
			h.requestExit("exit_clicked")
			return
		}
		h.mu.RLock()
		handler, page := h.handler, h.page
		h.mu.RUnlock()
		if handler != nil {
			requestCtx, requestCancel := context.WithCancel(r.Context())
			stop := context.AfterFunc(page.ctx, requestCancel)
			defer stop()
			defer requestCancel()
			handler.ServeHTTP(w, r.WithContext(requestCtx))
			return
		}
		if r.URL.Path == "/api/ui/mode" {
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, "transition")
			return
		}
		if r.URL.Path == "/" {
			serveViewerTransitionPage(w, "正在连接远程电脑", "连接成功后会自动打开远程桌面。关闭本窗口可取消连接。", token, "session")
			return
		}
		http.Error(w, "页面正在切换，请稍候", http.StatusServiceUnavailable)
	})))
	go func() {
		select {
		case <-tracker.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	go func() {
		err := h.server.Serve(l)
		if h.ctx.Err() == nil {
			h.journal.record("local_server_stopped", err)
		}
		cancel()
	}()
	return h, nil
}

func (h *viewerHost) requestExit(reason string) {
	if h.ctx.Err() == nil {
		h.journal.record(reason, nil)
	}
	h.cancel()
}

func (h *viewerHost) setTrayVisible(visible bool) {
	h.mu.RLock()
	t := h.tray
	h.mu.RUnlock()
	if t != nil {
		t.Visible(visible)
	}
}

func (h *viewerHost) showWindow() error {
	h.windowMu.Lock()
	defer h.windowMu.Unlock()
	if err := h.ctx.Err(); err != nil {
		return err
	}
	now := time.Now()
	if !h.lastShow.IsZero() && now.Sub(h.lastShow) < duplicateShowWindow {
		return nil
	}
	if err := h.window.Show(); err != nil {
		return err
	}
	h.lastShow = time.Now()
	h.setDeskHidden(false)
	h.setTrayVisible(true)
	return nil
}

func (h *viewerHost) hideWindow() error {
	h.windowMu.Lock()
	defer h.windowMu.Unlock()
	h.lastShow = time.Time{}
	h.tracker.Suspend()
	if err := h.window.Hide(); err != nil {
		return err
	}
	h.setDeskHidden(true)
	h.setTrayVisible(false)
	return nil
}

// closeToTray differs from the explicit Hide action: the native window and
// taskbar button disappear, but the tray icon remains as the visible way back.
func (h *viewerHost) closeToTray() error {
	h.windowMu.Lock()
	defer h.windowMu.Unlock()
	if err := h.ctx.Err(); err != nil {
		return err
	}
	h.lastShow = time.Time{}
	h.tracker.Suspend()
	if err := h.window.Hide(); err != nil {
		return err
	}
	h.setDeskHidden(true)
	h.setTrayVisible(true)
	return nil
}

func (h *viewerHost) minimizeWindow() error {
	h.windowMu.Lock()
	defer h.windowMu.Unlock()
	if err := h.ctx.Err(); err != nil {
		return err
	}
	h.lastShow = time.Time{}
	return h.window.Minimize()
}

func (h *viewerHost) setDeskHidden(hidden bool) {
	h.mu.RLock()
	desk := h.desk
	h.mu.RUnlock()
	if desk != nil {
		desk.hidden.Store(hidden)
	}
}

func (h *viewerHost) Close() {
	h.cancel()
	h.windowMu.Lock()
	defer h.windowMu.Unlock()
	h.mu.RLock()
	t := h.tray
	h.mu.RUnlock()
	if t != nil {
		t.Close()
	}
	_ = h.window.Close()
	_ = h.server.Close()
}

type viewerPageListener struct {
	host   *viewerHost
	ctx    context.Context
	cancel context.CancelFunc
}

func listenViewerPage(addr string, h *viewerHost) (net.Listener, error) {
	if h == nil {
		return net.Listen("tcp", addr)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	return &viewerPageListener{host: h, ctx: ctx, cancel: cancel}, nil
}
func (l *viewerPageListener) Accept() (net.Conn, error) { <-l.ctx.Done(); return nil, net.ErrClosed }
func (l *viewerPageListener) Addr() net.Addr            { return l.host.listener.Addr() }
func (l *viewerPageListener) Close() error {
	l.host.mu.Lock()
	if l.host.page == l {
		l.host.page = nil
		l.host.handler = nil
	}
	l.host.mu.Unlock()
	l.cancel()
	return nil
}
func attachViewerPage(listener net.Listener, handler http.Handler) {
	if l, ok := listener.(*viewerPageListener); ok {
		l.host.mu.Lock()
		l.host.page = l
		l.host.handler = handler
		l.host.mu.Unlock()
	}
}
