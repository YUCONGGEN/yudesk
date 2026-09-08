package viewerapp

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
	"github.com/yudesk/yudesk/internal/singleinstance"
	"github.com/yudesk/yudesk/internal/stream"
	"github.com/yudesk/yudesk/internal/uilifecycle"
)

const (
	defaultRelayAddress     = "www.yucg.cn:8233"
	defaultStatusServer     = "http://www.yucg.cn:8235"
	defaultRelayFingerprint = "20FC953E48B6BEED7FB3A5F73BF177CC4E2557CF274C409FF4F0179E5FA0F836"
)

type responseResult struct {
	message protocol.Message
	err     error
}

type client struct {
	conn      *protocol.Conn
	seq       uint64
	pendingMu sync.Mutex
	pending   map[string]chan responseResult
	closed    chan struct{}
	closeOnce sync.Once
	frames    *frameHub
	tiles     *tileHub
	audio     *audioHub
	stats     networkStats
	acks      chan string
}

func newClient(conn net.Conn) *client {
	c := &client{conn: protocol.NewConn(conn), pending: map[string]chan responseResult{}, closed: make(chan struct{}), frames: newFrameHub(), tiles: newTileHub(), audio: newAudioHub(), acks: make(chan string, 32)}
	go func() {
		for {
			select {
			case <-c.closed:
				return
			case id := <-c.acks:
				if err := c.conn.WriteMessage(protocol.Message{Kind: "event", Method: "frame_ack", ID: id}); err != nil {
					_ = c.conn.Close()
					return
				}
			}
		}
	}()
	go c.readLoop()
	return c
}

func (c *client) readLoop() {
	defer c.closeOnce.Do(func() { close(c.closed) })
	for {
		message, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if message.Kind == "event" {
			switch message.Method {
			case "frame", "tiles":
				c.stats.frame(message.Meta)
				var frame stream.TileFrame
				if message.Method == "tiles" {
					if json.Unmarshal(message.Params, &frame) != nil {
						_ = c.conn.Close()
						return
					}
				} else {
					c.frames.publish(message.Data, message.Meta)
					w, _ := message.Meta["width"].(float64)
					h, _ := message.Meta["height"].(float64)
					frame = stream.TileFrame{Width: int(w), Height: int(h), Reset: true, Tiles: []stream.Tile{{Width: int(w), Height: int(h), Size: len(message.Data), MIME: "image/jpeg"}}}
				}
				if err := c.tiles.publish(frame, message.Data, message.Method == "frame"); err != nil {
					_ = c.conn.Close()
					return
				}
				if enabled, _ := message.Meta["frameAck"].(bool); enabled {
					select {
					case c.acks <- message.ID:
					case <-c.closed:
						return
					}
				}
			case "stream_error":
				log.Printf("desktop stream: %s", message.Error)
				c.stats.mu.Lock()
				c.stats.value.Error = message.Error
				c.stats.mu.Unlock()
			case "stream_waiting", "stream_resumed":
				c.stats.mu.Lock()
				// Keep stale pixels hidden until a replacement frame arrives.
				c.stats.value.DesktopWaiting = true
				c.stats.value.DesktopMessage = message.Error
				if message.Method == "stream_resumed" {
					c.stats.value.DesktopMessage = "桌面已恢复，正在同步画面…"
				}
				c.stats.mu.Unlock()
			case "audio":
				c.audio.publish(message.Data)
			case "input_error", "input_ok":
				c.stats.mu.Lock()
				c.stats.value.InputError = message.Error
				c.stats.mu.Unlock()
			case "audio_error":
				c.audio.setError(message.Error)
			}
			continue
		}
		if message.Kind == "response" {
			c.pendingMu.Lock()
			waiter := c.pending[message.ID]
			c.pendingMu.Unlock()
			if waiter != nil {
				select {
				case waiter <- responseResult{message: message}:
				default:
				}
			}
		}
	}
}

func (c *client) request(method string, params any, data []byte) (protocol.Message, error) {
	return c.requestTimeout(method, params, data, 30*time.Second)
}

func (c *client) requestTimeout(method string, params any, data []byte, timeout time.Duration) (protocol.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.requestContext(ctx, method, params, data)
}

func (c *client) requestContext(ctx context.Context, method string, params any, data []byte) (protocol.Message, error) {
	id := fmt.Sprintf("%d", atomic.AddUint64(&c.seq, 1))
	raw, err := json.Marshal(params)
	if err != nil {
		return protocol.Message{}, err
	}
	waiter := make(chan responseResult, 1)
	c.pendingMu.Lock()
	c.pending[id] = waiter
	c.pendingMu.Unlock()
	defer func() { c.pendingMu.Lock(); delete(c.pending, id); c.pendingMu.Unlock() }()
	if err := c.conn.WriteMessageContext(ctx, protocol.Message{Kind: "request", ID: id, Method: method, Params: raw, Data: data}); err != nil {
		return protocol.Message{}, err
	}
	select {
	case result := <-waiter:
		if result.err != nil {
			return protocol.Message{}, result.err
		}
		if !result.message.OK {
			return result.message, errors.New(result.message.Error)
		}
		return result.message, nil
	case <-c.closed:
		return protocol.Message{}, errors.New("remote connection closed")
	case <-ctx.Done():
		return protocol.Message{}, ctx.Err()
	}
}

type frameHub struct {
	mu       sync.Mutex
	data     []byte
	meta     map[string]any
	sequence uint64
	changed  chan struct{}
}

func newFrameHub() *frameHub { return &frameHub{changed: make(chan struct{})} }
func (h *frameHub) publish(data []byte, meta map[string]any) {
	h.mu.Lock()
	h.data = data
	h.meta = meta
	h.sequence++
	close(h.changed)
	h.changed = make(chan struct{})
	h.mu.Unlock()
}
func (h *frameHub) next(after uint64) ([]byte, map[string]any, uint64, <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.data, h.meta, h.sequence, h.changed
}

type audioHub struct {
	chunks chan []byte
	mu     sync.RWMutex
	err    string
	active bool
	stream chan struct{}
	failed chan struct{}
}

func newAudioHub() *audioHub {
	return &audioHub{chunks: make(chan []byte, 3), stream: make(chan struct{}, 1), failed: make(chan struct{}, 1)}
}

func (h *audioHub) publish(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.active || len(data) == 0 {
		return
	}
	if len(data)%4 != 0 {
		h.err = "系统声音数据未对齐"
		select {
		case h.failed <- struct{}{}:
		default:
		}
		return
	}
	// Old peers may send large packets. Bound playback backlog by bytes too.
	if len(data) > 3840 {
		data = data[len(data)-3840:]
	}
	chunk := append([]byte(nil), data...)
	select {
	case h.chunks <- chunk:
	default:
		select {
		case <-h.chunks:
		default:
		}
		select {
		case h.chunks <- chunk:
		default:
		}
	}
}

func (h *audioHub) setError(message string) {
	h.mu.Lock()
	h.err = message
	select {
	case h.failed <- struct{}{}:
	default:
	}
	h.mu.Unlock()
}

func (h *audioHub) errorMessage() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.err
}

type viewerConfig struct {
	unified                               bool
	deviceDir                             string
	ui                                    *viewerHost
	agentAddr, pin, relayAddr, deviceID   string
	statusServer                          string
	relayToken, relayAuth, relayCA        string
	relayFingerprint, web, once, stateDir string
	relayTLS, relayInsecure, openUI       bool
	control, audio                        bool
	fps, quality                          int
}

type viewerConnectRequest struct {
	deviceID string
	pin      string
	control  bool
	audio    bool
	webAddr  string
}

type viewerSessionOutcome struct {
	webAddr          string
	returnToLauncher bool
}

func Main()        { main(false) }
func UnifiedMain() { main(true) }

func main(unified bool) {
	addr := flag.String("addr", "127.0.0.1:9347", "agent address")
	pin := flag.String("pin", "", "agent pairing PIN")
	relayAddr := flag.String("relay", defaultRelayAddress, "third-party relay address")
	statusServer := flag.String("server", defaultStatusServer, "device status web server")
	deviceID := flag.String("device-id", "", "agent device ID")
	relayToken := flag.String("relay-token", "", "legacy relay token")
	relayAuth := flag.String("relay-auth", "", "account session token")
	relayTLS := flag.Bool("relay-tls", true, "use TLS for relay connection")
	relayCA := flag.String("relay-ca", "", "trusted relay CA/certificate PEM file")
	relayFingerprint := flag.String("relay-fingerprint", defaultRelayFingerprint, "expected relay TLS SHA-256 fingerprint")
	relayInsecure := flag.Bool("relay-insecure", false, "disable relay certificate verification (unsafe)")
	web := flag.String("web", "127.0.0.1:9348", "local visual console address")
	once := flag.String("once", "", "save one screenshot and exit")
	fps := flag.Int("fps", 30, "requested stream frames per second (1-60; actual rate depends on hardware and network)")
	quality := flag.Int("quality", 70, "JPEG stream quality (30-90)")
	viewOnly := flag.Bool("view-only", false, "watch without sending mouse, keyboard, clipboard writes, or uploads")
	audio := flag.Bool("audio", false, "listen to the remote computer's system audio when supported")
	openUI := flag.Bool("open", true, "open the visual remote desktop in the default browser")
	stateDir := flag.String("state-dir", "", "local viewer state directory")
	deviceDir := flag.String("device-dir", "", "local device identity directory")
	flag.Parse()
	config := viewerConfig{
		unified: unified, deviceDir: *deviceDir,
		agentAddr: *addr, pin: *pin, relayAddr: *relayAddr, deviceID: *deviceID,
		statusServer: *statusServer,
		relayToken:   *relayToken, relayAuth: *relayAuth, relayTLS: *relayTLS,
		relayCA: *relayCA, relayFingerprint: *relayFingerprint, relayInsecure: *relayInsecure,
		web: *web, once: *once, fps: *fps, quality: *quality, openUI: *openUI, stateDir: *stateDir,
		control: !*viewOnly, audio: *audio,
	}
	if err := runViewer(config); err != nil {
		log.Fatal(err)
	}
}

func runViewer(config viewerConfig) error {
	viewerDirectory, accessToken, err := loadViewerState(config.stateDir)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(viewerDirectory, "viewer.lock")
	instanceLock, acquired, err := singleinstance.Acquire(lockPath)
	if err != nil {
		return err
	}
	if !acquired {
		existingURL, fullscreen := waitForViewerUI(viewerDirectory, accessToken, config.web, 5*time.Second)
		if existingURL != "" && !currentViewerUICompatible(existingURL, config.unified) {
			_ = requestExistingViewerExit(existingURL, fullscreen)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
				instanceLock, acquired, err = singleinstance.Acquire(lockPath)
				if err != nil {
					return err
				}
				if acquired {
					break
				}
			}
		}
		if !acquired {
			if existingURL != "" && config.openUI {
				return openBrowser(existingURL, fullscreen)
			}
			return nil
		}
	}
	defer instanceLock.Close()

	useLauncher := config.pin == "" || config.deviceID == ""
	launcherAddr := config.web
	launcherMessage := ""
	if useLauncher {
		if existingURL, fullscreen := currentViewerUI(viewerDirectory, accessToken, config.web); existingURL != "" {
			if !currentViewerUICompatible(existingURL, config.unified) {
				_ = requestExistingViewerExit(existingURL, fullscreen)
				deadline := time.Now().Add(5 * time.Second)
				for viewerUIAvailable(existingURL) && time.Now().Before(deadline) {
					time.Sleep(100 * time.Millisecond)
				}
			} else {
				if config.openUI {
					return openBrowser(existingURL, fullscreen)
				}
				return nil
			}
		}
	}
	if config.once == "" {
		config.ui, err = newViewerHost(config.web, accessToken)
		if err != nil {
			return err
		}
		defer config.ui.Close()
		config.ui.openUI = config.openUI
		if config.unified {
			if err := enableUnified(config.ui, config, viewerDirectory, accessToken); err != nil {
				return err
			}
			defer config.ui.desk.Close()
		}
		config.web = config.ui.listener.Addr().String()
		launcherAddr = config.web
	}
	for {
		if config.ui != nil && config.ui.ctx.Err() != nil {
			return nil
		}
		if useLauncher {
			request, connect, err := runViewerLauncherWithMessage(launcherAddr, config.openUI, viewerDirectory, accessToken, launcherMessage, config.statusServer, config.ui)
			if err != nil || !connect {
				return err
			}
			config.deviceID = request.deviceID
			config.pin = request.pin
			config.control = request.control
			config.audio = request.audio
			config.web = request.webAddr
			launcherAddr = request.webAddr
			// The existing launcher window follows the local mode endpoint into
			// the session. Opening another browser window here leaves a stale
			// "connecting" page behind when the session ends.
			config.openUI = false
			launcherMessage = ""
		}

		outcome, err := runViewerSession(config, viewerDirectory, accessToken)
		if err != nil {
			if !useLauncher {
				return err
			}
			config.deviceID = ""
			config.pin = ""
			config.web = launcherAddr
			launcherMessage = "连接失败：" + err.Error()
			continue
		}
		if !outcome.returnToLauncher {
			return nil
		}

		useLauncher = true
		launcherAddr = outcome.webAddr
		config.web = outcome.webAddr
		config.deviceID = ""
		config.pin = ""
		config.openUI = false
	}
}

func runViewerSession(config viewerConfig, viewerDirectory, accessToken string) (outcome viewerSessionOutcome, resultErr error) {
	parent := context.Background()
	if config.ui != nil {
		parent = config.ui.ctx
	}
	sessionCtx, cancelSession := context.WithCancel(parent)
	defer cancelSession()
	publicID := config.deviceID
	if relay.IsDeviceCode(config.deviceID) {
		resolved, err := relay.ResolveDevice(sessionCtx, config.relayAddr, relay.DialOptions{TLS: config.relayTLS, CAFile: config.relayCA, Fingerprint: config.relayFingerprint, Insecure: config.relayInsecure}, config.deviceID)
		if err != nil {
			return viewerSessionOutcome{}, err
		}
		config.deviceID = resolved.ID
	}
	if config.ui != nil && config.ui.desk != nil && config.ui.desk.device.Status().ID == config.deviceID {
		return viewerSessionOutcome{}, errors.New("不能连接本机，请输入另一台设备的设备码")
	}
	var raw net.Conn
	var err error
	if config.relayAddr != "" {
		raw, err = relay.DialWithContext(sessionCtx, config.relayAddr, relay.DialOptions{TLS: config.relayTLS, CAFile: config.relayCA, Fingerprint: config.relayFingerprint, Insecure: config.relayInsecure}, relay.Hello{Role: "viewer", ID: config.deviceID, Token: config.relayToken, Auth: config.relayAuth})
	} else {
		raw, err = (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}}).DialContext(sessionCtx, "tcp", config.agentAddr)
	}
	if err != nil {
		return viewerSessionOutcome{}, err
	}
	defer raw.Close()
	stopCancel := context.AfterFunc(sessionCtx, func() { _ = raw.Close() })
	defer stopCancel()
	wire := &measuredConn{Conn: raw}
	secured, err := secureconn.Connect(wire, config.deviceID)
	if err != nil {
		return viewerSessionOutcome{}, fmt.Errorf("end-to-end authentication failed: %w", err)
	}
	c := newClient(secured)
	mode := "view"
	if config.control {
		mode = "control"
	}
	authCtx, authCancel := context.WithTimeout(sessionCtx, 75*time.Second)
	defer authCancel()
	authResponse, err := c.requestContext(authCtx, "auth", map[string]any{"pin": config.pin, "mode": mode, "audio": config.audio, "audioOnDemand": true}, nil)
	if err != nil {
		return viewerSessionOutcome{}, fmt.Errorf("authentication failed: %w", err)
	}
	sessionControl := config.control
	if value, ok := authResponse.Meta["control"].(bool); ok {
		sessionControl = value
	}
	audioEnabled, _ := authResponse.Meta["audio"].(bool)
	audioOnDemand, _ := authResponse.Meta["audioOnDemand"].(bool)
	audioReason, _ := authResponse.Meta["audioReason"].(string)
	tileDelta, _ := authResponse.Meta["tileDeltaV1"].(bool)
	inputEvents, _ := authResponse.Meta["inputEventsV1"].(bool)
	go c.monitorNetwork(wire)
	name := config.deviceID
	if info, err := c.request("info", nil, nil); err == nil {
		if value, ok := info.Meta["name"].(string); ok && value != "" {
			name = value
		}
	}
	if err := updateHistory(viewerDirectory, connectionRecord{DeviceID: publicID, Name: name}, false); err != nil {
		log.Printf("save connection history: %v", err)
	}
	if config.once != "" {
		m, err := c.request("get_screenshot", nil, nil)
		if err != nil {
			return viewerSessionOutcome{}, err
		}
		if err := os.WriteFile(config.once, m.Data, 0600); err != nil {
			return viewerSessionOutcome{}, err
		}
		fmt.Printf("saved %s\n", config.once)
		return viewerSessionOutcome{}, nil
	}
	options := loadStreamOptions(viewerDirectory, config.fps, config.quality)
	options.TileDelta = tileDelta
	if _, err := c.request("stream_start", options, nil); err != nil {
		return viewerSessionOutcome{}, fmt.Errorf("start stream: %w", err)
	}
	mux := http.NewServeMux()
	var transitioning atomic.Bool
	mux.HandleFunc("/api/ui/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "desktop-input-files-v3")
	})
	mux.HandleFunc("/api/ui/mode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if transitioning.Load() {
			_, _ = io.WriteString(w, "transition")
			return
		}
		_, _ = io.WriteString(w, "session")
	})
	mux.HandleFunc("/", serveIndexWithCapabilities(options.FPS, options.Quality, accessToken, sessionControl, config.audio, audioEnabled, audioReason))
	registerViewerAssets(mux)
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(c.stats.snapshot())
	})
	mux.HandleFunc("/api/stream", c.serveStream)
	mux.HandleFunc("/api/frames", c.serveTiles)
	mux.HandleFunc("/api/audio", func(w http.ResponseWriter, r *http.Request) {
		if !audioEnabled {
			http.Error(w, audioReason, http.StatusNotFound)
			return
		}
		c.serveAudioOnDemand(w, r, audioOnDemand)
	})
	mux.HandleFunc("/api/audio/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": audioEnabled, "reason": audioReason, "error": c.audio.errorMessage(), "onDemand": audioOnDemand})
	})
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) { callJSON(w, c, "info", nil, nil) })
	mux.HandleFunc("/api/stream/options", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(loadStreamOptions(viewerDirectory, config.fps, config.quality))
			return
		}
		var p stream.Options
		if decodeJSON(w, r, &p, 16<<10) {
			p = p.Normalized()
			p.FrameAck = true
			p.TileDelta = tileDelta
			if _, err := c.request("stream_start", p, nil); err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			if err := saveStreamOptions(viewerDirectory, p); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(p)
		}
	})
	mux.HandleFunc("/api/input", func(w http.ResponseWriter, r *http.Request) {
		if !sessionControl {
			http.Error(w, "当前连接是仅观看模式", http.StatusForbidden)
			return
		}
		var p struct {
			Events []map[string]any `json:"events"`
			Width  int              `json:"width"`
			Height int              `json:"height"`
		}
		if !decodeJSON(w, r, &p, 128<<10) {
			return
		}
		if len(p.Events) > 64 {
			http.Error(w, "too many input events", 400)
			return
		}
		if inputEvents {
			c.writeInput(w, r, "input", p)
		} else {
			callJSON(w, c, "input", p, nil)
		}
	})
	mux.HandleFunc("/api/input/release", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		if !sessionControl {
			http.Error(w, "当前连接是仅观看模式", http.StatusForbidden)
			return
		}
		if inputEvents {
			c.writeInput(w, r, "input_release", nil)
		} else {
			callJSON(w, c, "input_release", nil, nil)
		}
	})
	mux.HandleFunc("/api/clipboard", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			m, err := c.request("clipboard_get", nil, nil)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write(m.Data)
			return
		}
		if !sessionControl {
			http.Error(w, "当前连接是仅观看模式", http.StatusForbidden)
			return
		}
		var p map[string]string
		if decodeJSON(w, r, &p, 2<<20) {
			callJSON(w, c, "clipboard_set", p, nil)
		}
	})
	fileV2, _ := authResponse.Meta["fileTransferV2"].(bool)
	registerFiles(mux, c.requestContext, sessionControl && fileV2)
	sessionDone := make(chan struct{})
	var sessionOnce sync.Once
	var returnToLauncher atomic.Bool
	finishSession := func(shouldReturn bool) {
		sessionOnce.Do(func() {
			returnToLauncher.Store(shouldReturn)
			close(sessionDone)
		})
	}
	uiTracker := uilifecycle.New(3 * time.Second)
	mux.Handle("/api/ui/watch", uiTracker)
	go func() {
		select {
		case <-uiTracker.Done():
		case <-sessionCtx.Done():
		case <-sessionDone:
			return
		}
		finishSession(false)
	}()
	mux.HandleFunc("/api/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		transitioning.Store(true)
		serveViewerTransitionPage(w, "正在结束远程控制", "即将返回控制端连接界面。", accessToken, "launcher")
		go func() {
			time.Sleep(200 * time.Millisecond)
			finishSession(true)
		}()
	})
	mux.HandleFunc("/api/exit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		serveViewerExitPage(w, "YuDesk 控制端已退出")
		go func() {
			time.Sleep(200 * time.Millisecond)
			finishSession(false)
		}()
	})
	listener, err := listenViewerPage(config.web, config.ui)
	if err != nil {
		return viewerSessionOutcome{}, err
	}
	server := &http.Server{Handler: localSecurityHeaders(requireAccessToken(accessToken, mux)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	attachViewerPage(listener, server.Handler)
	uiURL := viewerUIURL(listener.Addr().String(), accessToken)
	if err := writeViewerSession(viewerDirectory, uiURL); err != nil {
		_ = listener.Close()
		return viewerSessionOutcome{}, err
	}
	defer clearViewerSession(viewerDirectory, uiURL)
	log.Printf("visual remote desktop: %s", uiURL)
	if config.openUI {
		go func() {
			time.Sleep(20 * time.Millisecond)
			if err := openBrowser(uiURL, true); err != nil {
				log.Printf("open browser: %v", err)
			}
		}()
	}
	go func() {
		select {
		case <-c.closed:
			finishSession(false)
		case <-sessionDone:
		}
		_ = raw.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		return viewerSessionOutcome{}, err
	}
	<-sessionDone
	return viewerSessionOutcome{webAddr: listener.Addr().String(), returnToLauncher: returnToLauncher.Load()}, nil
}

func runViewerLauncher(addr string, openUI bool, stateDir, token string) (viewerConnectRequest, bool, error) {
	return runViewerLauncherWithMessage(addr, openUI, stateDir, token, "", defaultStatusServer)
}

func runViewerLauncherWithMessage(addr string, openUI bool, stateDir, token, initialMessage, statusServer string, hosts ...*viewerHost) (viewerConnectRequest, bool, error) {
	var host *viewerHost
	parent := context.Background()
	if len(hosts) > 0 {
		host = hosts[0]
		if host != nil {
			parent = host.ctx
		}
	}
	listener, err := listenViewerPage(addr, host)
	if err != nil {
		if existingURL := activeViewerSession(stateDir, token); existingURL != "" {
			if openUI {
				return viewerConnectRequest{}, false, openBrowser(existingURL, true)
			}
			return viewerConnectRequest{}, false, nil
		}
		if existingURL := activeViewerLauncher(stateDir, token); existingURL != "" {
			if openUI {
				return viewerConnectRequest{}, false, openBrowser(existingURL, false)
			}
			return viewerConnectRequest{}, false, nil
		}
		launcherURL := viewerUIURL(addr, token)
		if viewerUIAvailable(launcherURL) {
			if openUI {
				return viewerConnectRequest{}, false, openBrowser(launcherURL, false)
			}
			return viewerConnectRequest{}, false, nil
		}
		return viewerConnectRequest{}, false, err
	}
	var selected viewerConnectRequest
	var connect bool
	var transitioning atomic.Bool
	actionReady := make(chan struct{})
	var actionOnce sync.Once
	finish := func(request viewerConnectRequest, shouldConnect bool) {
		actionOnce.Do(func() {
			selected = request
			connect = shouldConnect
			close(actionReady)
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ui/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "desktop-input-files-v3")
	})
	mux.HandleFunc("/api/ui/mode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if transitioning.Load() {
			_, _ = io.WriteString(w, "transition")
			return
		}
		_, _ = io.WriteString(w, "launcher")
	})
	uiTracker := uilifecycle.New(3 * time.Second)
	mux.Handle("/api/ui/watch", uiTracker)
	mux.Handle("/api/device/status", serveViewerDeviceStatus(statusServer))
	go func() {
		select {
		case <-uiTracker.Done():
		case <-parent.Done():
		case <-actionReady:
			return
		}
		finish(viewerConnectRequest{}, false)
	}()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		records, err := loadHistory(stateDir)
		message := initialMessage
		if supplied := r.URL.Query().Get("message"); supplied != "" && len(supplied) < 512 {
			message = supplied
		}
		if err != nil {
			message = "读取连接记录失败"
		}
		deviceID := strings.ToUpper(r.URL.Query().Get("device_id"))
		if host != nil && host.desk != nil {
			host.desk.render(w, deviceID, message)
			return
		}
		_ = viewerLauncherPage.Execute(w, map[string]any{"Token": token, "History": records, "DeviceID": deviceID, "Message": message})
	})
	mux.HandleFunc("/history/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.ParseForm() != nil {
			http.Error(w, "请求无效", 400)
			return
		}
		if err := updateHistory(stateDir, connectionRecord{DeviceID: r.FormValue("device_id")}, true); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		http.Redirect(w, r, "/?access_token="+url.QueryEscape(token), http.StatusSeeOther)
	})
	mux.HandleFunc("/connect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.ParseForm() != nil {
			http.Error(w, "请求无效", http.StatusBadRequest)
			return
		}
		deviceID := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(r.FormValue("device_id"))), " ", "")
		pin := strings.TrimSpace(r.FormValue("pin"))
		if (!relay.IsDeviceCode(deviceID) && len(deviceID) != 24) || (pin != "" && (len(pin) < 6 || len(pin) > 8)) {
			http.Error(w, "设备码或 PIN 格式不正确", http.StatusBadRequest)
			return
		}
		if _, err := hex.DecodeString(deviceID); err != nil && !relay.IsDeviceCode(deviceID) {
			http.Error(w, "设备码格式不正确", http.StatusBadRequest)
			return
		}
		for _, char := range pin {
			if char < '0' || char > '9' {
				http.Error(w, "PIN 必须是 6–8 位数字", http.StatusBadRequest)
				return
			}
		}
		mode := strings.ToLower(strings.TrimSpace(r.FormValue("mode")))
		if mode == "" {
			mode = "control"
		}
		if mode != "control" && mode != "view" {
			http.Error(w, "连接模式无效", http.StatusBadRequest)
			return
		}
		listenAudio := r.FormValue("audio") == "1"
		if err := checkViewerPresence(r.Context(), statusServer, deviceID); err != nil {
			http.Redirect(w, r, "/?access_token="+url.QueryEscape(token)+"&device_id="+url.QueryEscape(deviceID)+"&message="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		if !transitioning.CompareAndSwap(false, true) {
			http.Error(w, "已有连接正在建立", http.StatusConflict)
			return
		}
		if pin == "" {
			serveViewerTransitionPage(w, "等待对方确认", "已请求连接；对方同意后打开远程桌面，最多等待 60 秒。", token, "session")
		} else {
			serveViewerTransitionPage(w, "正在连接远程电脑", "连接成功后会自动打开远程桌面，请稍候。", token, "session")
		}
		go func() {
			time.Sleep(200 * time.Millisecond)
			finish(viewerConnectRequest{deviceID: deviceID, pin: pin, control: mode == "control", audio: listenAudio, webAddr: listener.Addr().String()}, true)
		}()
	})
	mux.HandleFunc("/exit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		serveViewerExitPage(w, "YuDesk 控制端已退出")
		go func() {
			time.Sleep(200 * time.Millisecond)
			finish(viewerConnectRequest{}, false)
		}()
	})
	server := &http.Server{Handler: localSecurityHeaders(requireAccessToken(token, mux)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	attachViewerPage(listener, server.Handler)
	uiURL := viewerUIURL(listener.Addr().String(), token)
	if err := writeViewerLauncher(stateDir, uiURL); err != nil {
		_ = listener.Close()
		return viewerConnectRequest{}, false, err
	}
	defer clearViewerLauncher(stateDir, uiURL)
	if openUI {
		go func() {
			time.Sleep(20 * time.Millisecond)
			if err := openBrowser(uiURL, false); err != nil {
				log.Printf("打开浏览器失败: %v", err)
			}
		}()
	}
	log.Printf("控制端界面: %s", uiURL)
	go func() {
		<-actionReady
		shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	err = server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		return viewerConnectRequest{}, false, err
	}
	<-actionReady
	return selected, connect, nil
}

func browserProfileDirectory(component string) (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	profile := filepath.Join(root, "YuDesk", "browser", component)
	if err := os.MkdirAll(profile, 0700); err != nil {
		return "", err
	}
	return profile, nil
}

func chromiumBrowsers() []string {
	switch runtime.GOOS {
	case "windows":
		var result []string
		for _, candidate := range []struct{ env, path string }{
			{"ProgramFiles(x86)", `Microsoft\Edge\Application\msedge.exe`},
			{"ProgramFiles", `Microsoft\Edge\Application\msedge.exe`},
			{"LocalAppData", `Google\Chrome\Application\chrome.exe`},
			{"ProgramFiles", `Google\Chrome\Application\chrome.exe`},
			{"ProgramFiles(x86)", `Google\Chrome\Application\chrome.exe`},
		} {
			if root := os.Getenv(candidate.env); root != "" {
				result = append(result, filepath.Join(root, candidate.path))
			}
		}
		return result
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default:
		return []string{"microsoft-edge", "google-chrome-stable", "google-chrome", "chromium", "chromium-browser", "brave-browser"}
	}
}

func newAccessToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func loadViewerState(configuredDir string) (string, string, error) {
	directory := strings.TrimSpace(configuredDir)
	if directory == "" {
		configRoot, err := os.UserConfigDir()
		if err != nil {
			return "", "", err
		}
		directory = filepath.Join(configRoot, "yudesk")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", "", err
	}
	tokenPath := filepath.Join(directory, "viewer-ui-token")
	if data, err := os.ReadFile(tokenPath); err == nil {
		token := strings.TrimSpace(string(data))
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(token)
		if decodeErr != nil || len(decoded) < 24 {
			return "", "", errors.New("invalid viewer UI token file")
		}
		return directory, token, nil
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	token, err := newAccessToken()
	if err != nil {
		return "", "", err
	}
	file, err := os.OpenFile(tokenPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return loadViewerState(directory)
	}
	if err != nil {
		return "", "", err
	}
	if _, err = file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return "", "", err
	}
	if err = file.Close(); err != nil {
		return "", "", err
	}
	return directory, token, nil
}

func viewerUIURL(addr, token string) string {
	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		addr = net.JoinHostPort(host, port)
	}
	return "http://" + addr + "/?access_token=" + url.QueryEscape(token)
}

func viewerUIAvailable(target string) bool {
	client, transport := viewerHTTPClient(3 * time.Second)
	defer transport.CloseIdleConnections()
	response, err := client.Get(target)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func currentViewerUIVersion(target string) bool {
	return currentViewerUICompatible(target, false)
}

func currentViewerUICompatible(target string, unified bool) bool {
	endpoint, ok := localViewerEndpoint(target, "/api/ui/version")
	if !ok {
		return false
	}
	client, transport := viewerHTTPClient(2 * time.Second)
	defer transport.CloseIdleConnections()
	response, err := client.Get(endpoint)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64))
	want := "desktop-input-files-v3"
	if unified {
		want = unifiedVersion
	}
	return err == nil && response.StatusCode == http.StatusOK && strings.TrimSpace(string(data)) == want
}

func requestExistingViewerExit(target string, fullscreen bool) error {
	path := "/exit"
	if fullscreen {
		path = "/api/exit"
	}
	endpoint, ok := localViewerEndpoint(target, path)
	if !ok {
		return errors.New("invalid existing viewer UI URL")
	}
	client, transport := viewerHTTPClient(3 * time.Second)
	defer transport.CloseIdleConnections()
	response, err := client.Post(endpoint, "application/x-www-form-urlencoded", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("existing viewer exit returned %s", response.Status)
	}
	return nil
}

func localViewerEndpoint(target, path string) (string, bool) {
	if !validViewerUIURL(target, "") {
		return "", false
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return "", false
	}
	parsed.Path = path
	parsed.RawPath = ""
	parsed.Fragment = ""
	return parsed.String(), true
}

func viewerHTTPClient(timeout time.Duration) (*http.Client, *http.Transport) {
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}
	return &http.Client{Timeout: timeout, Transport: transport}, transport
}

func serveViewerDeviceStatus(statusServer string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		deviceIDs, err := viewerStatusDeviceIDs(r.URL.Query().Get("ids"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		endpoint, err := viewerStatusEndpoint(statusServer, deviceIDs)
		if err != nil {
			http.Error(w, "状态服务器配置无效", http.StatusServiceUnavailable)
			return
		}
		client, transport := viewerHTTPClient(5 * time.Second)
		defer transport.CloseIdleConnections()
		response, err := client.Get(endpoint)
		if err != nil {
			http.Error(w, "无法连接状态服务器", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 128<<10))
		if readErr != nil {
			http.Error(w, "读取状态服务器响应失败", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if response.StatusCode != http.StatusOK {
			w.WriteHeader(http.StatusBadGateway)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write(body)
	})
}

func viewerStatusDeviceIDs(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	if strings.TrimSpace(raw) == "" || len(parts) > 50 {
		return nil, errors.New("设备码数量无效")
	}
	result := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		deviceID := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(part)), " ", "")
		if !relay.IsDeviceCode(deviceID) && len(deviceID) != 24 {
			return nil, errors.New("设备码格式不正确")
		}
		if _, err := hex.DecodeString(deviceID); err != nil && !relay.IsDeviceCode(deviceID) {
			return nil, errors.New("设备码格式不正确")
		}
		if !seen[deviceID] {
			result = append(result, deviceID)
			seen[deviceID] = true
		}
	}
	return result, nil
}

func viewerStatusEndpoint(statusServer string, deviceIDs []string) (string, error) {
	endpoint, err := url.Parse(strings.TrimSpace(statusServer))
	if err != nil || endpoint.User != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return "", errors.New("invalid status server")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/device/status"
	endpoint.RawPath = ""
	endpoint.RawQuery = url.Values{"ids": {strings.Join(deviceIDs, ",")}}.Encode()
	endpoint.Fragment = ""
	return endpoint.String(), nil
}

func currentViewerUI(directory, token, launcherAddr string) (string, bool) {
	if target := activeViewerSession(directory, token); target != "" {
		return target, true
	}
	if target := activeViewerLauncher(directory, token); target != "" {
		return target, false
	}
	target := viewerUIURL(launcherAddr, token)
	if viewerUIAvailable(target) {
		return target, false
	}
	return "", false
}

func waitForViewerUI(directory, token, launcherAddr string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if target, fullscreen := currentViewerUI(directory, token, launcherAddr); target != "" {
			return target, fullscreen
		}
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func viewerSessionPath(directory string) string {
	return filepath.Join(directory, "viewer-session.url")
}

func viewerLauncherPath(directory string) string {
	return filepath.Join(directory, "viewer-launcher.url")
}

func writeViewerSession(directory, target string) error {
	return writeViewerURL(viewerSessionPath(directory), target)
}

func writeViewerLauncher(directory, target string) error {
	return writeViewerURL(viewerLauncherPath(directory), target)
}

func writeViewerURL(path, target string) error {
	if !validViewerUIURL(target, "") {
		return errors.New("refusing to store a non-local viewer URL")
	}
	return os.WriteFile(path, []byte(target+"\n"), 0600)
}

func activeViewerSession(directory, token string) string {
	return activeViewerURL(viewerSessionPath(directory), token)
}

func activeViewerLauncher(directory, token string) string {
	return activeViewerURL(viewerLauncherPath(directory), token)
}

func activeViewerURL(path, token string) string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	target := strings.TrimSpace(string(data))
	if validViewerUIURL(target, token) && viewerUIAvailable(target) {
		return target
	}
	return ""
}

func clearViewerSession(directory, target string) {
	clearViewerURL(viewerSessionPath(directory), target)
}

func clearViewerLauncher(directory, target string) {
	clearViewerURL(viewerLauncherPath(directory), target)
}

func clearViewerURL(path, target string) {
	data, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(data)) == target {
		_ = os.Remove(path)
	}
}

func validViewerUIURL(target, token string) bool {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Port() == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	address := net.ParseIP(host)
	if host != "localhost" && (address == nil || !address.IsLoopback()) {
		return false
	}
	return token == "" || parsed.Query().Get("access_token") == token
}

func serveViewerExitPage(w http.ResponseWriter, title string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>%s</title><style>html,body{width:100%%;height:100%%;overflow:hidden}body{margin:0;background:#f5f8fc;color:#24364d;font:13px 'Segoe UI','Microsoft YaHei',system-ui;display:grid;place-items:center}body>div{text-align:center;padding:28px 36px;background:#fff;border:1px solid #e5ebf3;border-radius:12px;max-width:70vw;box-shadow:0 8px 35px #16315b0b}h1{font-size:17px;font-weight:600;margin:15px 0 9px}p{font-size:12px;color:#8291a4;line-height:1.8;margin:0}.mark{display:inline-grid;place-items:center;width:38px;height:38px;background:#1478f7;border-radius:10px;color:white;font-size:21px;font-weight:700}</style></head><body><div><span class="mark">Yu</span><h1>%s</h1><p>需要使用时，请再次双击 YuDesk 控制端。</p></div></body></html>`, template.HTMLEscapeString(title), template.HTMLEscapeString(title))
}

func serveViewerTransitionPage(w http.ResponseWriter, title, detail, token, wantedMode string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>YuDesk %s</title><style>html,body{width:100%%;height:100%%;overflow:hidden}body{margin:0;background:#f5f8fc;color:#24364d;font:13px 'Segoe UI','Microsoft YaHei',system-ui;display:grid;place-items:center}body>div{text-align:center;padding:28px 36px;background:#fff;border:1px solid #e5ebf3;border-radius:12px;max-width:70vw;box-shadow:0 8px 35px #16315b0b}h1{font-size:17px;font-weight:600;margin:15px 0 9px}p{font-size:12px;color:#8291a4;line-height:1.8;margin:0}.mark{display:inline-grid;place-items:center;width:38px;height:38px;background:#1478f7;border-radius:10px;color:white;font-size:21px;font-weight:700}</style></head><body data-token="%s" data-mode="%s"><div><span class="mark">Yu</span><h1>%s</h1><p>%s</p></div><script>
const token=document.body.dataset.token;
const wanted=document.body.dataset.mode;
const target='/?access_token='+encodeURIComponent(token);
const shell=document.createElement('script');shell.src='/assets/window-ui.js?access_token='+encodeURIComponent(token);document.head.append(shell);
fetch('/api/ui/watch?access_token='+encodeURIComponent(token)).catch(()=>{});
async function follow(){
  try{
    const response=await fetch('/api/ui/mode?access_token='+encodeURIComponent(token),{cache:'no-store'});
    const mode=response.ok?(await response.text()).trim():'';
    if(mode===wanted||(wanted==='session'&&mode==='launcher')){location.replace(target);return;}
  }catch(_){ }
  setTimeout(follow,200);
}
setTimeout(follow,300);
</script></body></html>`, template.HTMLEscapeString(title), template.HTMLEscapeString(token), template.HTMLEscapeString(wantedMode), template.HTMLEscapeString(title), template.HTMLEscapeString(detail))
}

var viewerLauncherPage = template.Must(template.New("viewer-launcher").Parse(launcherHTML))

func requireAccessToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (c *client) serveStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=yudeskframe")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	var sequence uint64
	for {
		frame, _, next, changed := c.frames.next(sequence)
		if next == sequence {
			select {
			case <-changed:
				continue
			case <-r.Context().Done():
				return
			case <-c.closed:
				return
			}
		}
		sequence = next
		if _, err := fmt.Fprintf(w, "--yudeskframe\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame)); err != nil {
			return
		}
		if _, err := w.Write(frame); err != nil {
			return
		}
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return
		}
		flusher.Flush()
	}
}

func (c *client) serveAudio(w http.ResponseWriter, r *http.Request) {
	c.serveAudioOnDemand(w, r, false)
}

func (c *client) serveAudioOnDemand(w http.ResponseWriter, r *http.Request, onDemand bool) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	// Serialize listeners, including remote cleanup. A cancelled older request
	// cannot send audio_stop after the replacement request sends audio_start.
	select {
	case c.audio.stream <- struct{}{}:
	case <-r.Context().Done():
		return
	case <-c.closed:
		return
	}
	defer func() { <-c.audio.stream }()
	// Cancellation and admission can both be ready. Do not restart capture for
	// a listener which disconnected while waiting for the previous cleanup.
	if r.Context().Err() != nil {
		return
	}
	c.audio.mu.Lock()
	c.audio.active = true
	c.audio.err = ""
	for len(c.audio.chunks) > 0 {
		<-c.audio.chunks
	}
	for len(c.audio.failed) > 0 {
		<-c.audio.failed
	}
	c.audio.mu.Unlock()
	defer func() {
		c.audio.mu.Lock()
		c.audio.active = false
		c.audio.mu.Unlock()
		if onDemand {
			_, _ = c.requestTimeout("audio_stop", nil, nil, 3*time.Second)
		}
	}()
	if onDemand {
		// Do not use the HTTP context for protocol writes: stopping playback
		// must not close a partially written desktop transport record.
		if _, err := c.requestTimeout("audio_start", nil, nil, 3*time.Second); err != nil {
			c.audio.setError(err.Error())
			http.Error(w, err.Error(), 502)
			return
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-YuDesk-Audio-Format", "s16le;rate=48000;channels=2")
	_, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if controller.Flush() != nil { // Silent endpoints still complete the handshake.
		return
	}
	_ = controller.SetWriteDeadline(time.Time{})
	for {
		select {
		case chunk := <-c.audio.chunks:
			_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Time{})
		case <-c.audio.failed:
			return
		case <-r.Context().Done():
			return
		case <-c.closed:
			return
		}
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any, max int64) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, max)).Decode(target); err != nil {
		http.Error(w, "invalid request", 400)
		return false
	}
	return true
}
func (c *client) call(method string, params any, data []byte) (any, error) {
	m, err := c.request(method, params, data)
	if err != nil {
		return nil, err
	}
	if len(m.Data) > 0 {
		var v any
		if json.Unmarshal(m.Data, &v) == nil {
			return v, nil
		}
	}
	return map[string]any{"ok": true, "meta": m.Meta}, nil
}
func callJSON(w http.ResponseWriter, c *client, method string, params any, data []byte) {
	v, err := c.call(method, params, data)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func localSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' blob: data:")
		next.ServeHTTP(w, r)
	})
}

var page = template.Must(template.New("index").Parse(indexHTML + footerHTML))

type pageData struct {
	FPS, Quality   int
	Token          string
	Control        bool
	AudioRequested bool
	Audio          bool
	AudioReason    string
}

func serveIndex(fps, quality int, token string) http.HandlerFunc {
	return serveIndexWithCapabilities(fps, quality, token, true, false, false, "")
}

func serveIndexWithCapabilities(fps, quality int, token string, control, audioRequested, audio bool, audioReason string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, pageData{FPS: fps, Quality: quality, Token: token, Control: control, AudioRequested: audioRequested, Audio: audio, AudioReason: audioReason})
	}
}
