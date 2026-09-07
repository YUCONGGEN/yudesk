package viewerapp

import (
	"context"
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
	ctx         context.Context
	cancel      context.CancelFunc
	listener    net.Listener
	server      *http.Server
	mu          sync.RWMutex
	page        *viewerPageListener
	handler     http.Handler
	desk        *unifiedDesk
	version     atomic.Value
	tracker     *uilifecycle.Tracker
	window      *appWindow
	remoteClose func()
	reopen      atomic.Bool
	openUI      bool
}

func newViewerHost(addr, token string) (*viewerHost, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &viewerHost{ctx: ctx, cancel: cancel, listener: l}
	h.window = newAppWindow(l.Addr().String(), token)
	h.version.Store("desktop-input-files-v3")
	windowClosed := func() {
		if ctx.Err() != nil {
			return
		}
		h.mu.RLock()
		closeRemote := h.remoteClose
		h.mu.RUnlock()
		if closeRemote != nil {
			h.reopen.Store(true)
			closeRemote()
		} else {
			cancel()
		}
	}
	tracker := uilifecycle.NewWithCallback(700*time.Millisecond, func() {
		// Managed native windows report target destruction directly, avoiding
		// duplicate/late HTTP watcher callbacks after the dashboard reopens.
		if !h.window.managed.Load() {
			windowClosed()
		}
	})
	h.window.onClose = windowClosed
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
			if err := h.window.Show(); err != nil {
				http.Error(w, err.Error(), 503)
				return
			}
			w.WriteHeader(http.StatusOK)
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
			serveViewerExitPage(w, "YuDesk 控制端已退出")
			time.AfterFunc(200*time.Millisecond, cancel)
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
	go func() { _ = h.server.Serve(l); cancel() }()
	return h, nil
}

func (h *viewerHost) Close() { h.cancel(); _ = h.window.Hide(); _ = h.server.Close() }

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
