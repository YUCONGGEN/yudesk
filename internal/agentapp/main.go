package agentapp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yudesk/yudesk/internal/approval"
	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/filetransfer"
	"github.com/yudesk/yudesk/internal/identity"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
	"github.com/yudesk/yudesk/internal/security"
	"github.com/yudesk/yudesk/internal/singleinstance"
	"github.com/yudesk/yudesk/internal/stream"
	"github.com/yudesk/yudesk/internal/systemaudio"
	"github.com/yudesk/yudesk/internal/uilifecycle"
)

const (
	defaultRelayAddress     = "www.yucg.cn:8233"
	defaultRelayFingerprint = "20FC953E48B6BEED7FB3A5F73BF177CC4E2557CF274C409FF4F0179E5FA0F836"
	defaultWebServer        = "http://www.yucg.cn:8235"
)

type agent struct {
	approvals               *approval.Broker
	captureDesktop          func(desktop.CaptureOptions) (desktop.Screenshot, error) // test-only synthetic desktop; nil selects native capture
	allowBrowserUI          bool
	openNoticeBrowser       func(string) error   // tests inject this to prove headless runs never launch a browser
	inputFactory            func() *inputSession // injectable native input boundary for deterministic tests
	id, pin, name, shareDir string
	deviceCode              string // protected by statusMu
	reportedPIN             string // last PIN acknowledged by the management server; protected by statusMu
	pinRevision             uint64 // protected by statusMu
	fileMu                  sync.RWMutex
	fileEpoch               uint64
	fileConsent             bool
	fileEnabled             bool
	filePermissionPath      string
	allowControl            bool
	privateKey              ed25519.PrivateKey
	quit                    chan struct{}
	quitOnce                sync.Once
	connectionMu            sync.Mutex
	relayConnection         net.Conn
	authMu                  sync.Mutex
	authFailures            []time.Time
	lockedUntil             time.Time
	statusMu                sync.RWMutex
	relayStatus             string
	connected               bool
	activeUntil             time.Time
	uiMessage               string
	uiURL                   string
	licenseMu               sync.Mutex
	licenseArmed            bool
	licenseExpiry           time.Time
	managed                 bool
	managementOnline        bool // protected by statusMu
	terminationMu           sync.Mutex
}

func Main() {
	defaultName, _ := os.Hostname()
	listen := flag.String("listen", ":9347", "address for incoming viewer connections")
	deviceName := flag.String("name", defaultName, "device name shown in the web administration page")
	pinFlag := flag.String("pin", "", "pairing PIN; generated and persisted when omitted")
	share := flag.String("share-dir", "", "directory allowed for file transfer; empty disables file transfer")
	control := flag.Bool("allow-control", true, "allow remote mouse and keyboard input")
	relayAddr := flag.String("relay", defaultRelayAddress, "third-party relay address")
	relayToken := flag.String("relay-token", "", "legacy relay token (only if relay was configured with -token)")
	relayAuth := flag.String("relay-auth", "", "account session token from yudesk-account login")
	relayTLS := flag.Bool("relay-tls", true, "use TLS for the relay connection")
	relayCA := flag.String("relay-ca", "", "trusted relay CA/certificate PEM file")
	relayFingerprint := flag.String("relay-fingerprint", defaultRelayFingerprint, "expected relay TLS SHA-256 fingerprint")
	relayInsecure := flag.Bool("relay-insecure", false, "disable relay certificate verification (unsafe)")
	webServer := flag.String("server", defaultWebServer, "device activation web server")
	configDir := flag.String("config-dir", "", "device identity directory; defaults to the current user's YuDesk configuration")
	uiAddr := flag.String("ui", "127.0.0.1:9350", "local visual status page")
	openUI := flag.Bool("open", true, "open the local status page")
	flag.Parse()
	identityDir := strings.TrimSpace(*configDir)
	if identityDir == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			log.Fatal(err)
		}
		identityDir = filepath.Join(config, "yudesk")
	}
	instanceLock, acquired, err := singleinstance.Acquire(filepath.Join(identityDir, "agent.lock"))
	if err != nil {
		log.Fatal(err)
	}
	if !acquired {
		existingURL := waitForAgentUI(identityDir, 5*time.Second)
		if existingURL != "" && !currentAgentUI(existingURL) {
			_ = requestExistingAgentExit(existingURL)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
				instanceLock, acquired, err = singleinstance.Acquire(filepath.Join(identityDir, "agent.lock"))
				if err != nil {
					log.Fatal(err)
				}
				if acquired {
					break
				}
			}
		}
		if !acquired {
			if existingURL != "" && *openUI {
				if err := openAgentBrowser(existingURL); err != nil {
					log.Printf("重新打开被控端页面失败: %v", err)
				}
			}
			return
		}
	}
	defer instanceLock.Close()
	id, err := identity.Load(identityDir, *pinFlag)
	if err != nil {
		log.Fatal(err)
	}
	var tlsConfig *tls.Config
	if *relayAddr == "" {
		tlsConfig, err = security.SelfSignedConfig(id.ID)
		if err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("Device ID: %s", id.ID)
	log.Printf("Pairing PIN: %s", id.PIN)
	a := &agent{id: id.ID, pin: id.PIN, name: strings.TrimSpace(*deviceName), shareDir: *share, allowControl: *control, privateKey: id.PrivateKey, quit: make(chan struct{}), managed: *relayAddr != "" && *relayAuth == "" && *relayToken == "", allowBrowserUI: *openUI}
	if err := a.configureFilePermission(identityDir); err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-a.quit:
			cancel()
		case <-ctx.Done():
		}
	}()
	options := relay.DialOptions{TLS: *relayTLS, CAFile: *relayCA, Fingerprint: *relayFingerprint, Insecure: *relayInsecure}
	if a.managed {
		go a.monitorManagement(ctx, *relayAddr, options)
	} else if *relayAddr != "" {
		go a.monitorLicense(*webServer)
	}
	go a.enforceLicenseDeadline()
	if *uiAddr != "" {
		if existingURL := activeAgentUI(identityDir); existingURL != "" && !currentAgentUI(existingURL) {
			_ = requestExistingAgentExit(existingURL)
			deadline := time.Now().Add(5 * time.Second)
			for agentUIAvailable(existingURL) && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
		}
		uiToken := agentAccessToken(id.PrivateKey)
		uiTracker := uilifecycle.New(3 * time.Second)
		uiURL, err := a.startUI(*uiAddr, *webServer, uiToken, uiTracker)
		if err != nil {
			existingURL := agentUIURL(*uiAddr, uiToken)
			if agentUIAvailable(existingURL) {
				if *openUI {
					if openErr := openAgentBrowser(existingURL); openErr != nil {
						log.Printf("重新打开被控端页面失败: %v", openErr)
					}
				}
				return
			}
			log.Fatal(err)
		}
		log.Printf("被控端界面: %s", uiURL)
		a.statusMu.Lock()
		a.uiURL = uiURL
		a.statusMu.Unlock()
		if err := writeAgentUIState(identityDir, uiURL); err != nil {
			a.requestExit()
			log.Fatal(err)
		}
		defer clearAgentUIState(identityDir, uiURL)
		go func() {
			<-uiTracker.Done()
			a.requestExit()
		}()
		if *openUI {
			go func() {
				time.Sleep(20 * time.Millisecond)
				if err := openAgentBrowser(uiURL); err != nil {
					log.Printf("打开浏览器失败: %v", err)
				}
			}()
		}
	}
	if *relayAddr != "" {
		for {
			if a.quitting() {
				return
			}
			if a.managed && !a.waitForActiveLicense(*webServer) {
				return
			}
			a.setRelayStatus("已启动，正在等待控制端连接", false, "")
			c, e := relay.DialWithContext(ctx, *relayAddr, options, relay.Hello{Role: "agent", ID: id.ID, Name: a.name, PIN: id.PIN, Token: *relayToken, Auth: *relayAuth, PublicKey: id.PrivateKey.Public().(ed25519.PublicKey)})
			if e != nil {
				switch relay.RejectionCode(e) {
				case "STOP", "DISABLED", "DELETED", "DENIED", "BUSY", "EXPIRED":
					a.terminateWithNotice("服务器已拒绝连接，被控端正在退出", e.Error())
					return
				case "LICENSE_REQUIRED":
					a.setRelayStatus("等待设备授权，不再重复连接中转服务器", false, e.Error())
					if !a.waitForActiveLicense(*webServer) {
						return
					}
					continue
				}
				a.setRelayStatus("尚未授权或服务器暂时不可用", false, e.Error())
				log.Printf("relay: %v; retrying in 5s", e)
				if !a.waitOrQuit(5 * time.Second) {
					return
				}
				continue
			}
			if !a.setRelayConnection(c) {
				_ = c.Close()
				return
			}
			a.setRelayStatus("控制端已连接", true, "")
			log.Printf("connected to relay %s", *relayAddr)
			a.handle(c)
			a.setRelayConnection(nil)
			a.setRelayStatus("连接已结束，等待下一次连接", false, "")
			if !a.waitOrQuit(time.Second) {
				return
			}
		}
	}
	if a.quitting() {
		return
	}
	ln, err := tlsListen(*listen, tlsConfig)
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	go func() {
		<-a.quit
		_ = ln.Close()
	}()
	log.Printf("YuDesk agent %s listening on %s", id.ID, *listen)
	for {
		c, err := ln.Accept()
		if err != nil {
			if a.quitting() {
				return
			}
			log.Printf("accept: %v", err)
			continue
		}
		go a.handle(c)
	}
}

func (a *agent) quitting() bool {
	select {
	case <-a.quit:
		return true
	default:
		return false
	}
}

func (a *agent) waitOrQuit(duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-a.quit:
		return false
	}
}

func (a *agent) setRelayConnection(connection net.Conn) bool {
	a.connectionMu.Lock()
	defer a.connectionMu.Unlock()
	if a.quitting() {
		return false
	}
	a.relayConnection = connection
	return true
}

func (a *agent) requestExit() {
	if a.approvals != nil {
		a.approvals.Cancel()
	}
	a.quitOnce.Do(func() { close(a.quit) })
	a.connectionMu.Lock()
	connection := a.relayConnection
	a.relayConnection = nil
	a.connectionMu.Unlock()
	if connection != nil {
		_ = connection.Close()
	}
}

// terminateWithNotice brings a hidden status window back before a terminal
// server or licensing action stops the process. Serializing this path avoids
// opening several browser windows when expiry and management notices race.
func (a *agent) terminateWithNotice(status, detail string) {
	a.terminationMu.Lock()
	defer a.terminationMu.Unlock()
	if a.quitting() {
		return
	}
	a.setRelayStatus(status, false, detail)
	a.statusMu.RLock()
	target := a.uiURL
	a.statusMu.RUnlock()
	if target != "" && a.allowBrowserUI {
		open := a.openNoticeBrowser
		if open == nil {
			open = openAgentBrowser
		}
		if err := open(target); err != nil {
			log.Printf("显示被控端终止通知失败: %v", err)
		}
		// Give the local page enough time to render the reason before shutdown.
		time.Sleep(1200 * time.Millisecond)
	}
	a.requestExit()
}

func (a *agent) setRelayStatus(status string, connected bool, detail string) {
	a.statusMu.Lock()
	a.relayStatus = status
	a.connected = connected
	if detail != "" && !strings.Contains(strings.ToLower(detail), "activation") {
		a.uiMessage = detail
	}
	a.statusMu.Unlock()
}

func (a *agent) startUI(addr, serverURL, token string, lifecycle *uilifecycle.Tracker) (string, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	a.registerFilePermission(mux, token)
	mux.Handle("/api/ui/watch", lifecycle)
	mux.HandleFunc("/api/ui/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "desktop-input-files-v3")
	})
	mux.HandleFunc("/api/ui/status", func(w http.ResponseWriter, r *http.Request) {
		a.statusMu.RLock()
		data := map[string]any{"status": a.relayStatus, "activeUntil": "尚未授权", "message": a.uiMessage}
		if a.activeUntil.After(time.Now()) {
			data["activeUntil"] = displayLicenseExpiry(a.activeUntil)
		}
		a.statusMu.RUnlock()
		data["filesEnabled"] = a.fileRoot() != ""
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(data)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		a.refreshLicense(serverURL)
		a.statusMu.RLock()
		data := map[string]any{"ID": a.id, "PIN": a.pin, "Name": a.name, "Status": a.relayStatus, "ActiveUntil": "尚未授权", "Message": a.uiMessage, "Token": token}
		if a.activeUntil.After(time.Now()) {
			data["ActiveUntil"] = displayLicenseExpiry(a.activeUntil)
		}
		a.statusMu.RUnlock()
		data["FilesEnabled"] = a.fileRoot() != ""
		data["FileDirectory"] = a.shareDir
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = agentPage.Execute(w, data)
	})
	mux.HandleFunc("/activate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "请求无效", http.StatusBadRequest)
			return
		}
		expires, err := a.activate(serverURL, r.FormValue("key"))
		a.statusMu.Lock()
		if err != nil {
			a.uiMessage = "激活失败：" + err.Error()
		} else {
			a.uiMessage = "激活成功"
		}
		a.statusMu.Unlock()
		if err == nil {
			a.setLicenseState(true, expires)
		}
		http.Redirect(w, r, "/?access_token="+url.QueryEscape(token), http.StatusSeeOther)
	})
	mux.HandleFunc("/hide", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		lifecycle.Suspend()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>YuDesk 已隐藏</title><style>body{margin:0;background:#07111f;color:#eaf2ff;font:18px system-ui;display:grid;place-items:center;height:100vh}div{text-align:center}p{color:#8ea4bf}</style></head><body><div><h1>YuDesk 已隐藏到后台</h1><p>远程连接仍可用；再次双击被控端可重新显示本窗口。</p></div><script>setTimeout(()=>window.close(),300)</script></body></html>`)
	})
	mux.HandleFunc("/exit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>YuDesk 已退出</title><style>body{margin:0;background:#07111f;color:#eaf2ff;font:18px system-ui;display:grid;place-items:center;height:100vh}div{text-align:center}p{color:#8ea4bf}</style></head><body><div><h1>YuDesk 被控端已退出</h1><p>现在无法远程连接；需要使用时请再次双击被控端。</p></div></body></html>`)
		go func() {
			time.Sleep(200 * time.Millisecond)
			a.requestExit()
		}()
	})
	server := &http.Server{Handler: localAgentSecurity(requireAgentToken(token, mux)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-a.quit
		shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("被控端界面: %v", err)
		}
	}()
	return agentUIURL(listener.Addr().String(), token), nil
}

func (a *agent) activate(serverURL, key string) (time.Time, error) {
	payload, _ := json.Marshal(map[string]string{"id": a.id, "name": a.name, "key": strings.TrimSpace(key), "publicKey": base64.StdEncoding.EncodeToString(a.privateKey.Public().(ed25519.PublicKey))})
	client := directHTTPClient(12 * time.Second)
	response, err := client.Post(strings.TrimRight(serverURL, "/")+"/api/device/activate", "application/json", bytes.NewReader(payload))
	if err != nil {
		return time.Time{}, err
	}
	defer response.Body.Close()
	var result struct {
		OK          bool      `json:"ok"`
		Error       string    `json:"error"`
		ActiveUntil time.Time `json:"activeUntil"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result) != nil {
		return time.Time{}, errors.New("服务器响应无效")
	}
	if response.StatusCode >= 400 || !result.OK {
		if result.Error == "" {
			result.Error = response.Status
		}
		return time.Time{}, errors.New(result.Error)
	}
	return result.ActiveUntil, nil
}

func (a *agent) refreshLicense(serverURL string) {
	if a.managed {
		return
	} // management messages are authenticated by relay TLS
	client := directHTTPClient(3 * time.Second)
	response, err := client.Get(strings.TrimRight(serverURL, "/") + "/api/device/status?id=" + url.QueryEscape(a.id))
	if err != nil {
		return
	}
	defer response.Body.Close()
	var result struct {
		Active      bool      `json:"active"`
		ActiveUntil time.Time `json:"activeUntil"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&result) == nil {
		a.setLicenseState(result.Active, result.ActiveUntil)
	}
}

func (a *agent) setLicenseState(active bool, expiry time.Time) {
	a.statusMu.Lock()
	a.activeUntil = expiry
	a.statusMu.Unlock()

	now := time.Now()
	a.licenseMu.Lock()
	if active && expiry.After(now) {
		a.licenseArmed = true
		a.licenseExpiry = expiry
	}
	shouldExit := a.licenseArmed && (!active || !a.licenseExpiry.After(now))
	a.licenseMu.Unlock()
	if shouldExit {
		a.terminateWithNotice("设备授权已被撤销，被控端正在退出", "请联系管理员重新授权")
	}
}

func displayLicenseExpiry(expiry time.Time) string {
	if expiry.Year() >= 9999 {
		return "永久授权"
	}
	return expiry.Local().Format("2006-01-02 15:04:05")
}

func (a *agent) licenseDeadline() (time.Time, bool) {
	a.licenseMu.Lock()
	defer a.licenseMu.Unlock()
	return a.licenseExpiry, a.licenseArmed
}

func (a *agent) hasActiveLicense() bool {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	return (!a.managed || a.managementOnline) && a.activeUntil.After(time.Now())
}

func (a *agent) waitForActiveLicense(serverURL string) bool {
	interval := 10 * time.Second
	if a.managed {
		interval = 250 * time.Millisecond
	}
	for {
		a.refreshLicense(serverURL)
		if a.quitting() {
			return false
		}
		if a.hasActiveLicense() {
			return true
		}
		if !a.waitOrQuit(interval) {
			return false
		}
	}
}

func (a *agent) monitorManagement(ctx context.Context, addr string, options relay.DialOptions) {
	backoff := time.Second
	for {
		err := relay.WatchDeviceWithPIN(ctx, addr, options, relay.Hello{ID: a.id, Name: a.name, PIN: a.currentPIN(), PublicKey: a.privateKey.Public().(ed25519.PublicKey)}, a.privateKey, a.currentPIN, func(m relay.ControlMessage) {
			backoff = time.Second
			a.statusMu.Lock()
			a.managementOnline = true
			a.reportedPIN = m.PIN
			if relay.IsDeviceCode(m.DeviceCode) {
				a.deviceCode = m.DeviceCode
			}
			if !m.Active {
				a.relayStatus = "在线，等待管理员授权或输入激活码"
			}
			a.statusMu.Unlock()
			a.setLicenseState(m.Active, m.ActiveUntil)
		})
		a.statusMu.Lock()
		a.managementOnline = false
		a.statusMu.Unlock()
		if a.quitting() || ctx.Err() != nil {
			return
		}
		if relay.RejectionCode(err) != "" {
			a.terminateWithNotice("服务器已终止运行", err.Error())
			return
		}
		a.setRelayStatus("服务器连接暂时中断，正在恢复", false, "")
		if !a.waitOrQuit(backoff) {
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// Expiry must still stop the process when the server cannot be reached.
func (a *agent) enforceLicenseDeadline() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-a.quit:
			return
		case <-ticker.C:
			if expiry, armed := a.licenseDeadline(); armed && !expiry.After(time.Now()) {
				a.terminateWithNotice("授权码已到期，被控端正在退出", "请联系管理员续期")
				return
			}
		}
	}
}

func (a *agent) monitorLicense(serverURL string) {
	const pollInterval = 10 * time.Second
	for {
		a.refreshLicense(serverURL)
		if a.quitting() {
			return
		}
		wait := pollInterval
		if expiry, armed := a.licenseDeadline(); armed {
			remaining := time.Until(expiry)
			if remaining <= 0 {
				a.terminateWithNotice("授权码已到期，被控端正在退出", "请联系管理员续期")
				return
			}
			if remaining < wait {
				wait = remaining
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-a.quit:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

func directHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		},
	}
}

func agentAccessToken(privateKey ed25519.PrivateKey) string {
	mac := hmac.New(sha256.New, privateKey)
	_, _ = mac.Write([]byte("yudesk-local-agent-ui-v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func agentUIURL(addr, token string) string {
	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		addr = net.JoinHostPort(host, port)
	}
	return "http://" + addr + "/?access_token=" + url.QueryEscape(token)
}

func agentUIAvailable(target string) bool {
	client := directHTTPClient(5 * time.Second)
	response, err := client.Get(target)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func currentAgentUI(target string) bool {
	endpoint, ok := localAgentEndpoint(target, "/api/ui/version")
	if !ok {
		return false
	}
	response, err := directHTTPClient(2 * time.Second).Get(endpoint)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64))
	return err == nil && response.StatusCode == http.StatusOK && strings.TrimSpace(string(data)) == "desktop-input-files-v3"
}

func requestExistingAgentExit(target string) error {
	endpoint, ok := localAgentEndpoint(target, "/exit")
	if !ok {
		return errors.New("invalid existing agent UI URL")
	}
	response, err := directHTTPClient(3*time.Second).Post(endpoint, "application/x-www-form-urlencoded", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("existing agent exit returned %s", response.Status)
	}
	return nil
}

func localAgentEndpoint(target, path string) (string, bool) {
	if !validAgentUIURL(target) {
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

func agentUIStatePath(directory string) string {
	return filepath.Join(directory, "agent-ui.url")
}

func writeAgentUIState(directory, target string) error {
	if !validAgentUIURL(target) {
		return errors.New("refusing to store a non-local agent URL")
	}
	return os.WriteFile(agentUIStatePath(directory), []byte(target+"\n"), 0600)
}

func activeAgentUI(directory string) string {
	path := agentUIStatePath(directory)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	target := strings.TrimSpace(string(data))
	if validAgentUIURL(target) && agentUIAvailable(target) {
		return target
	}
	return ""
}

func waitForAgentUI(directory string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		if target := activeAgentUI(directory); target != "" {
			return target
		}
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func clearAgentUIState(directory, target string) {
	path := agentUIStatePath(directory)
	data, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(data)) == target {
		_ = os.Remove(path)
	}
}

func validAgentUIURL(target string) bool {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Port() == "" || parsed.Query().Get("access_token") == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	address := net.ParseIP(host)
	return host == "localhost" || (address != nil && address.IsLoopback())
}

func requireAgentToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func localAgentSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

func openAgentBrowser(target string) error {
	if runtime.GOOS == "windows" {
		profile, profileErr := browserProfileDirectory("agent")
		for _, candidate := range []string{
			filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(os.Getenv("ProgramFiles"), `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(os.Getenv("LocalAppData"), `Google\Chrome\Application\chrome.exe`),
		} {
			if _, err := os.Stat(candidate); err == nil {
				if profileErr != nil {
					break
				}
				args := []string{
					"--app=" + target,
					"--user-data-dir=" + profile,
					"--no-first-run",
					"--disable-first-run-ui",
					"--new-window",
				}
				if err := exec.Command(candidate, args...).Start(); err == nil {
					return nil
				}
			}
		}
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		command = exec.Command("open", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
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

var agentPage = template.Must(template.New("agent").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>YuDesk 被控端</title>
<style>*{box-sizing:border-box}body{margin:0;background:#07111f;color:#eaf2ff;font:15px/1.6 system-ui}.wrap{max-width:760px;margin:50px auto;padding:20px}.card{background:#101d2d;border:1px solid #28405d;border-radius:18px;padding:28px;box-shadow:0 20px 45px #0005}h1{margin:0 0 4px;font-size:32px}.sub{color:#8ea4bf}.pair,.actions{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin:24px 0 12px}.value{background:#07111f;border:1px solid #294360;border-radius:12px;padding:16px}.value small{display:block;color:#8ea4bf}.value strong{display:block;font:22px ui-monospace;letter-spacing:1px;overflow-wrap:anywhere}.copy{padding:8px;margin-top:12px;background:#20364f}.copy-all{margin:0 0 8px}.copy-message{min-height:24px;margin:4px 0 12px;color:#7ce0ad;text-align:center}.status{padding:12px;border-radius:9px;background:#102f2a;color:#7ce0ad}.message{padding:12px;border-radius:9px;background:#3a2c16;color:#ffd98a}form{margin-top:20px}input,button{width:100%;padding:12px;border-radius:9px;border:1px solid #35506f;background:#07111f;color:white;font-size:15px}button{margin-top:10px;background:#2878e8;border:0;font-weight:600;cursor:pointer}.secondary{background:#20364f}.danger{background:#a43b49}.actions form{margin:0}.hint{color:#8ea4bf;margin-top:18px}@media(max-width:600px){.pair,.actions{grid-template-columns:1fr}.wrap{margin-top:10px}}</style></head>
<body data-device-id="{{.ID}}" data-pin="{{.PIN}}" data-token="{{.Token}}"><main class="wrap"><section class="card"><h1>YuDesk 被控端</h1><p class="sub">设备名称：{{.Name}}。保持本程序运行，控制端即可连接这台电脑。</p><div class="pair"><div class="value"><small>设备码</small><strong>{{.ID}}</strong><button class="copy" id="copy-device" type="button">复制设备码</button></div><div class="value"><small>临时 PIN</small><strong>{{.PIN}}</strong><button class="copy" id="copy-pin" type="button">复制 PIN</button></div></div><button class="copy-all" id="copy-both" type="button">一键复制设备码和 PIN</button><p class="copy-message" id="copy-message" role="status"></p><p class="status" id="status">{{.Status}} · 授权有效期：{{.ActiveUntil}}</p><p class="message" id="server-message">{{.Message}}</p><form method="post" action="/activate?access_token={{.Token}}"><label>输入管理员发放的授权码</label><input name="key" autocomplete="off" placeholder="YU-XXXX-XXXX-..." required><button>立即激活</button></form><section><h2>文件传输授权</h2><p class="hint">仅共享此接收目录，不开放电脑其他文件。目录：{{.FileDirectory}}</p><form method="post" action="/files/permission?access_token={{.Token}}"><input type="hidden" name="enabled" value="{{if .FilesEnabled}}0{{else}}1{{end}}"><button class="secondary" type="submit">{{if .FilesEnabled}}关闭文件传输授权{{else}}允许在接收目录上传和下载文件{{end}}</button></form></section><div class="actions"><form method="post" action="/hide?access_token={{.Token}}"><button class="secondary" type="submit">隐藏到后台</button></form><form class="exit-form" method="post" action="/exit?access_token={{.Token}}" onsubmit="return confirm('确定要退出 YuDesk 被控端吗？退出后将无法远程连接。')"><button class="danger" type="submit">退出被控端</button></form></div><p class="hint">“隐藏到后台”会继续保持在线，再次双击程序即可显示；直接关闭窗口或点“退出被控端”会结束进程。</p></section></main><script>const deviceID=document.body.dataset.deviceId,pin=document.body.dataset.pin,token=document.body.dataset.token,message=document.querySelector('#copy-message'),api=path=>path+'?access_token='+encodeURIComponent(token);fetch(api('/api/ui/watch')).catch(()=>{});async function refreshStatus(){try{const r=await fetch(api('/api/ui/status'),{cache:'no-store'}),v=await r.json();document.querySelector('#status').textContent=v.status+' · 授权有效期：'+v.activeUntil;document.querySelector('#server-message').textContent=v.message||''}catch{}setTimeout(refreshStatus,2000)}refreshStatus();async function copyText(value,label){try{if(navigator.clipboard&&window.isSecureContext){await navigator.clipboard.writeText(value)}else{const area=document.createElement('textarea');area.value=value;area.style.position='fixed';area.style.opacity='0';document.body.appendChild(area);area.select();if(!document.execCommand('copy'))throw new Error('copy failed');area.remove()}message.textContent=label+'已复制'}catch(e){message.textContent='复制失败，请手动选择复制'}}document.querySelector('#copy-device').onclick=()=>copyText(deviceID,'设备码');document.querySelector('#copy-pin').onclick=()=>copyText(pin,'PIN');document.querySelector('#copy-both').onclick=()=>copyText('设备码：'+deviceID+'\n临时 PIN：'+pin,'设备码和 PIN');</script></body></html>`))

func tlsListen(addr string, config *tls.Config) (net.Listener, error) {
	return tls.Listen("tcp", addr, config)
}

func (a *agent) handle(raw net.Conn) {
	defer raw.Close()
	if !a.authenticationAllowed() {
		log.Printf("connection rejected: pairing temporarily locked")
		return
	}
	secured, err := secureconn.Accept(raw, a.privateKey)
	if err != nil {
		log.Printf("end-to-end handshake failed: %v", err)
		return
	}
	_ = secured.SetReadDeadline(time.Now().Add(15 * time.Second))
	c := protocol.NewConn(secured)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-a.quit:
			cancel()
		case <-ctx.Done():
		}
	}()
	input := newInputSession()
	if a.inputFactory != nil {
		input = a.inputFactory()
	}
	capture := a.captureDesktop
	if capture == nil {
		capture = desktop.CaptureWithOptions
	}
	defer input.release()
	frameAcks := make(chan string, 32)
	first, err := c.ReadMessage()
	if err != nil {
		return
	}
	if first.Kind != "request" || first.Method != "auth" {
		_ = c.WriteMessage(protocol.Failure(first.ID, "authentication required"))
		return
	}
	var auth struct {
		PIN           string `json:"pin"`
		Mode          string `json:"mode"`
		Audio         bool   `json:"audio"`
		AudioOnDemand bool   `json:"audioOnDemand"`
	}
	if json.Unmarshal(first.Params, &auth) != nil {
		_ = c.WriteMessage(protocol.Failure(first.ID, "invalid authentication request"))
		return
	}
	if auth.Mode == "" {
		auth.Mode = "control"
	}
	if auth.Mode != "control" && auth.Mode != "view" {
		_ = c.WriteMessage(protocol.Failure(first.ID, "invalid connection mode"))
		return
	}
	type firstReadResult struct {
		message protocol.Message
		err     error
	}
	var afterApproval chan firstReadResult
	if auth.PIN == "" {
		if a.approvals == nil {
			_ = c.WriteMessage(protocol.Failure(first.ID, "对方版本不支持确认连接，请使用 PIN 或升级"))
			return
		}
		// While waiting, detect peer cancellation without a second concurrent
		// reader. Preserve the first post-auth message for the normal loop.
		_ = secured.SetReadDeadline(time.Now().Add(75 * time.Second))
		afterApproval = make(chan firstReadResult, 1)
		ready := make(chan struct{})
		go func() {
			m, err := c.ReadMessage()
			if err != nil {
				cancel()
			} else {
				select {
				case <-ready:
				default:
					cancel()
				}
			}
			afterApproval <- firstReadResult{m, err}
		}()
		if err := a.approvals.Request(ctx, auth.Mode); err != nil {
			_ = c.WriteMessage(protocol.Failure(first.ID, err.Error()))
			return
		}
		close(ready)
	} else if !a.matchesPIN(auth.PIN) {
		a.recordAuthenticationFailure()
		_ = c.WriteMessage(protocol.Failure(first.ID, "invalid PIN"))
		return
	}
	a.clearAuthenticationFailures()
	_ = secured.SetReadDeadline(time.Time{})
	sessionControl := a.allowControl && !strings.EqualFold(strings.TrimSpace(auth.Mode), "view")
	audioAvailable, audioReason := systemaudio.Available()
	audioEnabled := (auth.Audio || auth.AudioOnDemand) && audioAvailable
	if err := c.WriteMessage(protocol.Response(first.ID, nil, map[string]any{"id": a.id, "platform": runtime.GOOS, "control": sessionControl, "audio": audioEnabled, "audioOnDemand": true, "audioReason": audioReason, "tileDeltaV1": true, "inputEventsV1": true, "fileTransferV2": true})); err != nil {
		return
	}
	// A congested downstream frame must not stall the upstream input reader
	// merely because a ping is waiting for the writer mutex.
	responses := make(chan protocol.Message, 32)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case response := <-responses:
				if c.WriteMessage(response) != nil {
					_ = raw.Close()
					return
				}
			}
		}
	}()
	reply := func(response protocol.Message) bool {
		select {
		case responses <- response:
			return true
		default:
			_ = raw.Close()
			return false
		}
	}
	// File I/O, clipboard helpers and stream reconfiguration must not run in
	// the input reader. Their queue is bounded, and modern file chunks are small.
	requests := make(chan protocol.Message, 4)
	go func() {
		audio := &sessionAudio{ctx: ctx, capture: systemaudio.Capture, send: func(m protocol.Message) error { return c.WriteMessageContext(ctx, m) }}
		defer audio.stop()
		if audioEnabled && !auth.AudioOnDemand {
			_ = audio.start()
		}
		files := filetransfer.New()
		defer files.Close()
		fileSweep := time.NewTicker(time.Second)
		defer fileSweep.Stop()
		var fileEpoch uint64
		var stopStream context.CancelFunc
		var streamDone chan struct{}
		defer func() {
			if stopStream != nil {
				stopStream()
			}
		}()
		for {
			var m protocol.Message
			select {
			case <-ctx.Done():
				return
			case <-fileSweep.C:
				a.syncFilePermission(files, &fileEpoch)
				_ = files.Sweep()
				continue
			case m = <-requests:
			}
			if ctx.Err() != nil {
				return
			}
			if m.Method == "stream_start" {
				var options stream.Options
				if json.Unmarshal(m.Params, &options) != nil {
					if !reply(protocol.Failure(m.ID, "invalid stream options")) {
						return
					}
					continue
				}
				options = options.Normalized()
				if stopStream != nil {
					stopStream()
					if !waitStreamStopped(ctx, streamDone, raw, 7*time.Second) {
						return
					}
				}
				streamCtx, streamCancel := context.WithCancel(ctx)
				stopStream = streamCancel
				if !reply(protocol.Response(m.ID, nil, map[string]any{"fps": options.FPS, "quality": options.Quality})) {
					return
				}
				streamDone = make(chan struct{})
				go func(done chan struct{}, generation string, settings stream.Options) {
					defer close(done)
					if settings.TileDelta {
						runTileStream(streamCtx, c, settings, generation, frameAcks, input, capture)
					} else {
						runDesktopStream(streamCtx, c, settings, generation, frameAcks, input, capture)
					}
				}(streamDone, m.ID, options)
				continue
			}
			if m.Method == "stream_stop" {
				if stopStream != nil {
					stopStream()
					if !waitStreamStopped(ctx, streamDone, raw, 7*time.Second) {
						return
					}
					stopStream = nil
				}
				if !reply(protocol.Response(m.ID, nil, nil)) {
					return
				}
				continue
			}
			var data []byte
			var meta map[string]any
			var callErr error
			if m.Method == "audio_start" || m.Method == "audio_stop" {
				if !audioEnabled {
					callErr = errors.New(audioReason)
				} else if m.Method == "audio_start" {
					callErr = audio.start()
				} else {
					callErr = audio.stop()
				}
			} else if strings.HasPrefix(m.Method, "file_") {
				if !sessionControl {
					callErr = errors.New("session is view-only")
				} else {
					data, meta, callErr = a.handleFileRequest(files, &fileEpoch, m)
				}
			} else if !sessionControl && (m.Method == "clipboard_set" || m.Method == "upload" || m.Method == "download" || m.Method == "list_files") {
				callErr = errors.New("session is view-only")
			} else if m.Method == "upload" || m.Method == "download" || m.Method == "list_files" {
				callErr = errors.New("请更新控制端，使用安全分块文件传输")
			} else {
				data, meta, callErr = a.dispatch(m.Method, m.Params, m.Data)
			}
			if callErr != nil {
				if !reply(protocol.Failure(m.ID, callErr.Error())) {
					return
				}
			} else if !reply(protocol.Response(m.ID, data, meta)) {
				return
			}
		}
	}()
	var lastInputNotice time.Time
	inputFailed := false
	notifyInput := func(err error) {
		if err == nil && !inputFailed {
			return
		}
		if err != nil && time.Since(lastInputNotice) < time.Second {
			return
		}
		message := protocol.Message{Kind: "event", Method: "input_ok"}
		if err != nil {
			message.Method = "input_error"
			message.Error = "输入暂不可用，画面仍保持连接。请检查被控端控制权限或输入法；管理员窗口可能需要在本机授权提升被控端权限。"
		}
		select {
		case responses <- message:
			inputFailed = err != nil
			lastInputNotice = time.Now()
		default:
		}
	}
	for {
		var m protocol.Message
		var err error
		if afterApproval != nil {
			select {
			case first := <-afterApproval:
				m, err = first.message, first.err
			case <-ctx.Done():
				return
			}
			afterApproval = nil
		} else {
			m, err = c.ReadMessage()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("client %s: %v", a.id, err)
			}
			return
		}
		if m.Kind != "request" {
			if m.Kind == "event" && m.Method == "frame_ack" {
				select {
				case frameAcks <- m.ID:
				default:
				}
			}
			if m.Kind == "event" && (m.Method == "input" || m.Method == "input_release") {
				// Input is a one-way ordered upstream stream: no screen payload
				// or downstream acknowledgement is on its critical path.
				if !sessionControl {
					continue
				}
				if m.Method == "input_release" {
					notifyInput(input.release())
				} else if err := input.handle(m.Params); err != nil {
					_ = input.release()
					notifyInput(err)
				} else {
					notifyInput(nil)
				}
			}
			continue
		}
		var data []byte
		var meta map[string]any
		var callErr error
		if !sessionControl && (m.Method == "input" || m.Method == "input_release" || m.Method == "clipboard_set" || m.Method == "upload") {
			callErr = errors.New("session is view-only")
		} else if m.Method == "info" {
			meta = map[string]any{"id": a.id, "name": a.name, "control": sessionControl, "agentControl": a.allowControl, "files": sessionControl && a.fileRoot() != "", "fileTransferV2": true, "streamV2": true, "audio": audioEnabled, "audioReason": audioReason}
		} else if m.Method == "input" {
			if !a.allowControl {
				callErr = errors.New("remote control is disabled")
			} else {
				callErr = input.handle(m.Params)
				if callErr != nil {
					_ = input.release()
				}
				notifyInput(callErr)
			}
		} else if m.Method == "input_release" {
			callErr = input.release()
			notifyInput(callErr)
		} else if m.Method == "ping" {
			// Pings and legacy input retain priority over disk/helper work.
		} else {
			if len(m.Data) > filetransfer.ChunkSize {
				callErr = errors.New("请求块过大，请使用分块文件传输")
			} else {
				select {
				case requests <- m:
					continue
				default:
					callErr = errors.New("操作繁忙，请稍后重试")
				}
			}
		}
		if callErr != nil {
			callErr = fmt.Errorf("%s: %w", m.Method, callErr)
			if !reply(protocol.Failure(m.ID, callErr.Error())) {
				return
			}
		} else {
			if !reply(protocol.Response(m.ID, data, meta)) {
				return
			}
		}
	}
}

func (a *agent) authenticationAllowed() bool {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	return time.Now().After(a.lockedUntil)
}
func (a *agent) recordAuthenticationFailure() {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	kept := a.authFailures[:0]
	for _, at := range a.authFailures {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	a.authFailures = append(kept, time.Now())
	if len(a.authFailures) >= 5 {
		a.lockedUntil = time.Now().Add(5 * time.Minute)
		a.authFailures = nil
	}
}
func (a *agent) clearAuthenticationFailures() {
	a.authMu.Lock()
	a.authFailures = nil
	a.lockedUntil = time.Time{}
	a.authMu.Unlock()
}

func (a *agent) dispatch(method string, raw json.RawMessage, data []byte) ([]byte, map[string]any, error) {
	switch method {
	case "ping":
		return nil, nil, nil
	case "info":
		return nil, map[string]any{"id": a.id, "name": a.name, "control": a.allowControl, "files": a.shareDir != "", "streamV2": true}, nil
	case "get_screenshot":
		s, err := desktop.Capture()
		if err != nil {
			return nil, nil, err
		}
		return s.JPEG, map[string]any{"width": s.Width, "height": s.Height, "contentType": "image/jpeg"}, nil
	case "input":
		if !a.allowControl {
			return nil, nil, errors.New("remote control is disabled")
		}
		var p struct {
			Events []desktop.InputEvent `json:"events"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, nil, err
		}
		if len(p.Events) > 64 {
			return nil, nil, errors.New("too many input events")
		}
		return nil, nil, desktop.ApplyInput(p.Events)
	case "clipboard_get":
		v, err := desktop.ClipboardGet()
		return []byte(v), map[string]any{"contentType": "text/plain; charset=utf-8"}, err
	case "clipboard_set":
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, nil, err
		}
		if len(p.Text) > 1<<20 {
			return nil, nil, errors.New("clipboard is too large")
		}
		return nil, nil, desktop.ClipboardSet(p.Text)
	case "list_files":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, nil, err
		}
		entries, err := a.listFiles(p.Path)
		if err != nil {
			return nil, nil, err
		}
		b, _ := json.Marshal(entries)
		return b, map[string]any{"contentType": "application/json"}, nil
	case "download":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, nil, err
		}
		b, err := a.readFile(p.Path)
		return b, nil, err
	case "upload":
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, nil, err
		}
		if len(data) > 20<<20 {
			return nil, nil, errors.New("file is too large")
		}
		return nil, nil, a.writeFile(p.Path, data)
	default:
		return nil, nil, fmt.Errorf("unknown method %q", method)
	}
}

type fileEntry struct {
	Name      string `json:"name"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size,omitempty"`
}

func (a *agent) rooted(rel string) (string, error) {
	directory := a.fileRoot()
	if directory == "" {
		return "", errors.New("file transfer is disabled")
	}
	root, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	check := target
	if _, e := os.Stat(target); e != nil {
		check = filepath.Dir(target)
	}
	resolved, err := filepath.EvalSymlinks(check)
	if err != nil {
		return "", err
	}
	if check != target {
		resolved = filepath.Join(resolved, filepath.Base(target))
	}
	r, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes shared directory")
	}
	return resolved, nil
}
func (a *agent) listFiles(rel string) ([]fileEntry, error) {
	p, err := a.rooted(rel)
	if err != nil {
		return nil, err
	}
	ds, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]fileEntry, 0, len(ds))
	for _, d := range ds {
		e := fileEntry{Name: d.Name(), Directory: d.IsDir()}
		if !d.IsDir() {
			if info, x := d.Info(); x == nil {
				e.Size = info.Size()
			}
		}
		out = append(out, e)
	}
	return out, nil
}
func (a *agent) readFile(rel string) ([]byte, error) {
	p, err := a.rooted(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if info.IsDir() || info.Size() > 20<<20 {
		return nil, errors.New("file is missing or larger than 20 MB")
	}
	return os.ReadFile(p)
}
func (a *agent) writeFile(rel string, data []byte) error {
	p, err := a.rooted(rel)
	if err != nil {
		return err
	}
	if filepath.Base(p) == "." || filepath.Base(p) == string(filepath.Separator) {
		return errors.New("invalid file name")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}
