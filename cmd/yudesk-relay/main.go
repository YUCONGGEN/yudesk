package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/releaseinfo"
	"github.com/yudesk/yudesk/internal/security"
)

type waiting struct {
	conn    net.Conn
	role    string
	owner   string
	ready   chan net.Conn
	signals *waitingSignals
}

// Serialize WAIT/PING/OK so signalling can never follow the final OK into the
// encrypted application stream. This lock is per connection, not per broker.
type waitingSignals struct {
	sync.Mutex
	paired bool
}
type activeSession struct {
	agent, viewer net.Conn
	owner         string
}
type broker struct {
	sync.Mutex
	devices        map[string]waiting
	active         map[string]activeSession
	controls       map[string]*deviceControl
	stopRequests   map[string]string
	adminFlashes   map[string]adminFlash
	autoActivate   bool
	token          string
	accounts       *account.Store
	deviceLicenses bool
	lookups        map[string]lookupWindow
}

type adminFlash struct {
	generated []string
	message   string
	expiresAt time.Time
}

func main() {
	listen := flag.String("listen", ":9347", "relay listen address")
	token := flag.String("token", "", "optional legacy relay token; empty requires account session auth")
	tlsFlag := flag.Bool("tls", true, "enable TLS for relay connections")
	httpTLS := flag.Bool("http-tls", false, "serve the homepage and account API over TLS using -cert and -key")
	downloadHTTP := flag.String("download-http", "", "optional plain HTTP address for the download homepage only")
	certFile := flag.String("cert", "yudesk-relay.crt", "relay TLS certificate file")
	keyFile := flag.String("key", "yudesk-relay.key", "relay TLS private key file")
	httpAddr := flag.String("http", "127.0.0.1:9380", "account HTTP API address; empty disables it")
	accountsFile := flag.String("accounts", "yudesk.db", "SQLite account database file")
	downloads := flag.String("downloads", "downloads", "directory containing cross-platform client downloads")
	publicRelay := flag.String("public-relay", "", "public relay host:port shown on the usage guide")
	publicAccount := flag.String("public-account", "", "public HTTPS account API URL shown on the usage guide")
	deviceLicenses := flag.Bool("device-licenses", false, "authorize standalone devices without user accounts")
	adminKeyFile := flag.String("admin-key-file", "", "file containing the web administration password")
	flag.Parse()
	if *downloadHTTP != "" && *publicAccount == "" {
		log.Fatal("-public-account is required with -download-http")
	}
	if *deviceLicenses && *adminKeyFile == "" {
		log.Fatal("-admin-key-file is required with -device-licenses")
	}
	adminKey := ""
	if *adminKeyFile != "" {
		var keyErr error
		adminKey, keyErr = loadOrCreateAdminKey(*adminKeyFile)
		if keyErr != nil {
			log.Fatal(keyErr)
		}
	}
	var ln net.Listener
	var err error
	var relayTLSConfig *tls.Config
	var fingerprint string
	if *tlsFlag || *httpTLS {
		cfg, loadedFingerprint, e := security.LoadOrCreateServerConfig(*certFile, *keyFile, "yudesk-relay")
		if e != nil {
			log.Fatal(e)
		}
		relayTLSConfig = cfg
		fingerprint = loadedFingerprint
		log.Printf("server TLS SHA-256 fingerprint: %s", fingerprint)
	}
	if *tlsFlag {
		ln, err = tls.Listen("tcp", *listen, relayTLSConfig)
	} else {
		ln, err = net.Listen("tcp", *listen)
	}
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	store, err := account.Open(*accountsFile)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	autoActivate, err := store.DeviceAutoActivation()
	if err != nil {
		log.Fatal(err)
	}
	b := &broker{devices: map[string]waiting{}, active: map[string]activeSession{}, stopRequests: map[string]string{}, token: *token, accounts: store, deviceLicenses: *deviceLicenses, autoActivate: autoActivate}
	log.Printf("YuDesk relay listening on %s (tls=%v)", *listen, *tlsFlag)
	if *httpAddr != "" {
		go serveAccounts(b, *httpAddr, *downloads, *httpTLS, *certFile, *keyFile, fingerprint, *publicRelay, *publicAccount, adminKey)
	}
	if *downloadHTTP != "" {
		go serveDownloads(*downloadHTTP, *downloads, fingerprint, *publicRelay, *publicAccount, *deviceLicenses)
	}
	for {
		c, e := ln.Accept()
		if e != nil {
			log.Print(e)
			continue
		}
		go b.handle(c)
	}
}

func loadOrCreateAdminKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		key := strings.ToUpper(hex.EncodeToString(raw))
		if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
			return "", err
		}
		return key, nil
	}
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if len(key) < 10 {
		return "", errors.New("admin key must contain at least 10 characters")
	}
	return key, nil
}

func (b *broker) automaticDeviceActivation() bool {
	b.Lock()
	defer b.Unlock()
	return b.autoActivate
}

func (b *broker) setAutomaticDeviceActivation(enabled bool) {
	b.Lock()
	b.autoActivate = enabled
	b.Unlock()
}

func (b *broker) ensureAutomaticDeviceLicense(deviceID, remoteAddr string) error {
	if !b.automaticDeviceActivation() {
		return nil
	}
	granted, err := b.accounts.EnsurePermanentDeviceLicense(deviceID)
	if err != nil {
		return err
	}
	if granted {
		b.accounts.Audit(account.AuditEntry{Action: "device_auto_activated", DeviceID: deviceID, RemoteAddr: remoteAddr, Detail: "permanent"})
	}
	return nil
}

func (b *broker) handle(c net.Conn) {
	paired := false
	defer func() {
		if !paired {
			_ = c.Close()
		}
	}()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReaderSize(c, 16<<10)
	line, err := r.ReadString('\n')
	_ = c.SetReadDeadline(time.Time{})
	if err != nil || len(line) > 16<<10 {
		return
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
	if len(parts) != 2 || parts[0] != "YU_RELAY/1" {
		return
	}
	var h relay.Hello
	if json.Unmarshal([]byte(parts[1]), &h) != nil || h.ID == "" || (h.Role != "agent" && h.Role != "viewer" && h.Role != "control" && h.Role != "resolve") {
		return
	}
	h.ID = strings.ToUpper(h.ID)
	if h.Role == "resolve" {
		b.handleResolve(c, h.ID)
		return
	}
	if h.Role == "control" {
		b.handleControl(c, r, h)
		return
	}
	if h.Role == "agent" {
		if reason, stop := b.takeStopRequest(h.ID); stop {
			writeRelayRejection(c, "STOP", reason)
			return
		}
	}
	legacyOK := b.token != "" && h.Token == b.token
	owner := "legacy"
	if b.deviceLicenses {
		if h.Role == "agent" {
			if err := b.accounts.RegisterLicensedDeviceNamed(h.ID, h.PublicKey, h.Name); err != nil {
				b.accounts.Audit(account.AuditEntry{Action: "device_register_failed", DeviceID: h.ID, RemoteAddr: c.RemoteAddr().String(), Detail: err.Error()})
				code := "DENIED"
				if strings.Contains(strings.ToLower(err.Error()), "revoked") {
					code = "DISABLED"
				}
				writeRelayRejection(c, code, err.Error())
				return
			}
			if err := b.ensureAutomaticDeviceLicense(h.ID, c.RemoteAddr().String()); err != nil {
				writeRelayRejection(c, "DENIED", "automatic device activation failed")
				return
			}
		}
		licenseExpiry, licenseErr := b.accounts.DeviceLicenseExpiry(h.ID)
		if licenseErr != nil {
			b.accounts.Audit(account.AuditEntry{Action: "device_license_denied", DeviceID: h.ID, RemoteAddr: c.RemoteAddr().String()})
			code := "DENIED"
			if strings.Contains(strings.ToLower(licenseErr.Error()), "revoked") {
				code = "DISABLED"
			}
			writeRelayRejection(c, code, licenseErr.Error())
			return
		}
		if !licenseExpiry.After(time.Now()) {
			b.accounts.Audit(account.AuditEntry{Action: "device_license_denied", DeviceID: h.ID, RemoteAddr: c.RemoteAddr().String()})
			writeRelayRejection(c, "LICENSE_REQUIRED", "device activation is required or has expired")
			return
		}
		owner = "device:" + h.ID
		if !account.IsPermanentDeviceLicense(licenseExpiry) {
			_ = c.SetDeadline(licenseExpiry)
		}
	} else if !legacyOK {
		var valid bool
		owner, valid = b.accounts.Authenticate(h.Auth)
		if !valid {
			b.accounts.Audit(account.AuditEntry{Action: "relay_auth_failed", DeviceID: h.ID, RemoteAddr: c.RemoteAddr().String()})
			writeRelayRejection(c, "DENIED", "invalid relay credentials")
			return
		}
		licenseExpiry, licenseErr := b.accounts.LicenseExpiry(owner)
		if licenseErr != nil || !licenseExpiry.After(time.Now()) {
			b.accounts.Audit(account.AuditEntry{Username: owner, Action: "relay_license_denied", DeviceID: h.ID, RemoteAddr: c.RemoteAddr().String()})
			writeRelayRejection(c, "LICENSE_REQUIRED", "account activation is required or has expired")
			return
		}
		// Enforce the paid period on already-connected sessions as well as on new
		// handshakes. Renewing a license takes effect on the next reconnect.
		_ = c.SetDeadline(licenseExpiry)
		if h.Role == "agent" {
			if err := b.accounts.BindDevice(owner, h.ID, h.PublicKey, ""); err != nil {
				b.accounts.Audit(account.AuditEntry{Username: owner, Action: "device_bind_failed", DeviceID: h.ID, RemoteAddr: c.RemoteAddr().String(), Detail: err.Error()})
				writeRelayRejection(c, "DENIED", err.Error())
				return
			}
		} else if !b.accounts.OwnsDevice(owner, h.ID) {
			b.accounts.Audit(account.AuditEntry{Username: owner, Action: "device_access_denied", DeviceID: h.ID, RemoteAddr: c.RemoteAddr().String()})
			writeRelayRejection(c, "DENIED", "device is not owned by this account")
			return
		}
	}
	ready := make(chan net.Conn, 1)
	signals := &waitingSignals{}
	for {
		b.Lock()
		if _, busy := b.active[h.ID]; busy {
			b.Unlock()
			writeRelayRejection(c, "BUSY", "device already has an active session")
			return
		}
		old, ok := b.devices[h.ID]
		if !ok {
			signals.Lock()
			b.devices[h.ID] = waiting{conn: c, role: h.Role, owner: owner, ready: ready, signals: signals}
			b.Unlock()
			b.accounts.Audit(account.AuditEntry{Username: owner, Action: "relay_wait", DeviceID: h.ID, RemoteAddr: c.RemoteAddr().String(), Detail: h.Role})
			_, _ = fmt.Fprintln(c, "WAIT")
			signals.Unlock()
			break
		}
		if old.role != h.Role {
			if old.owner != owner {
				b.Unlock()
				writeRelayRejection(c, "DENIED", "device session belongs to another account")
				return
			}
			delete(b.devices, h.ID)
			if b.active == nil {
				b.active = map[string]activeSession{}
			}
			active := activeSession{owner: owner}
			if h.Role == "agent" {
				active.agent, active.viewer = c, old.conn
			} else {
				active.agent, active.viewer = old.conn, c
			}
			b.active[h.ID] = active
			b.Unlock()
			if old.signals != nil {
				old.signals.Lock()
				old.signals.paired = true
			}
			_, _ = fmt.Fprintln(old.conn, "OK")
			if old.signals != nil {
				old.signals.Unlock()
			}
			_, _ = fmt.Fprintln(c, "OK")
			old.ready <- c
			b.accounts.Audit(account.AuditEntry{Username: owner, Action: "relay_paired", DeviceID: h.ID, RemoteAddr: c.RemoteAddr().String()})
			paired = true
			return
		}
		delete(b.devices, h.ID)
		b.Unlock()
		_ = old.conn.Close()
		old.ready <- nil
	}
	stream, disconnected := watchWaitingConnection(c, r)
	defer stream.Close()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case peer := <-ready:
			if peer == nil {
				return
			}
			paired = true
			b.Lock()
			active, stillActive := b.active[h.ID]
			matches := stillActive && ((h.Role == "agent" && active.agent == c && active.viewer == peer) || (h.Role == "viewer" && active.agent == peer && active.viewer == c))
			b.Unlock()
			if !matches {
				_ = c.Close()
				_ = peer.Close()
				return
			}
			defer func() {
				b.Lock()
				if current, ok := b.active[h.ID]; ok && current.agent == active.agent && current.viewer == active.viewer {
					delete(b.active, h.ID)
				}
				b.Unlock()
			}()
			relay.Proxy(stream, peer)
			return
		case <-disconnected:
			b.Lock()
			if current, ok := b.devices[h.ID]; ok && current.conn == c {
				delete(b.devices, h.ID)
			}
			if current, ok := b.active[h.ID]; ok && (current.agent == c || current.viewer == c) {
				_ = current.agent.Close()
				_ = current.viewer.Close()
				delete(b.active, h.ID)
			}
			b.Unlock()
			return
		case <-ticker.C:
			signals.Lock()
			var pingErr error
			if !signals.paired {
				_, pingErr = fmt.Fprintln(c, "PING")
			}
			signals.Unlock()
			if pingErr != nil {
				b.Lock()
				if cur, ok := b.devices[h.ID]; ok && cur.conn == c {
					delete(b.devices, h.ID)
				}
				b.Unlock()
				return
			}
		}
	}
}

type attemptWindow struct {
	count int
	reset time.Time
}
type loginLimiter struct {
	sync.Mutex
	attempts map[string]attemptWindow
}

func (l *loginLimiter) allow(key string) bool {
	l.Lock()
	defer l.Unlock()
	v := l.attempts[key]
	if time.Now().After(v.reset) {
		delete(l.attempts, key)
		return true
	}
	return v.count < 5
}
func (l *loginLimiter) failure(key string) {
	l.Lock()
	defer l.Unlock()
	v := l.attempts[key]
	if time.Now().After(v.reset) {
		v = attemptWindow{reset: time.Now().Add(15 * time.Minute)}
	}
	v.count++
	l.attempts[key] = v
}
func (l *loginLimiter) success(key string) { l.Lock(); delete(l.attempts, key); l.Unlock() }

func serveAccounts(b *broker, addr, downloadDir string, useTLS bool, certFile, keyFile, fingerprint, publicRelay, publicAccount, adminKey string) {
	store := b.accounts
	mux := http.NewServeMux()
	limiter := &loginLimiter{attempts: map[string]attemptWindow{}}
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	registerDownloadRoutes(mux, downloadDir, func(r *http.Request) guideConfig {
		return makeGuideConfig(r, scheme, fingerprint, publicRelay, publicAccount, b.deviceLicenses)
	})
	if b.deviceLicenses {
		registerStandaloneDeviceRoutes(mux, b, adminKey)
	}
	mux.HandleFunc("/api/register", func(w http.ResponseWriter, r *http.Request) {
		if b.deviceLicenses {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var p struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&p) != nil {
			writeJSON(w, account.PublicError(fmt.Errorf("bad request")))
			return
		}
		if err := store.Register(p.Username, p.Password); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, account.PublicError(err))
			return
		}
		store.Audit(account.AuditEntry{Username: p.Username, Action: "account_registered", RemoteAddr: r.RemoteAddr})
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		if b.deviceLicenses {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var p struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&p) != nil {
			writeJSON(w, account.PublicError(fmt.Errorf("bad request")))
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		key := host + "|" + strings.ToLower(p.Username)
		if !limiter.allow(key) {
			store.Audit(account.AuditEntry{Username: p.Username, Action: "login_rate_limited", RemoteAddr: r.RemoteAddr})
			writeJSONStatus(w, http.StatusTooManyRequests, account.PublicError(fmt.Errorf("too many login attempts; retry later")))
			return
		}
		token, err := store.Login(p.Username, p.Password)
		if err != nil {
			limiter.failure(key)
			store.Audit(account.AuditEntry{Username: p.Username, Action: "login_failed", RemoteAddr: r.RemoteAddr})
			writeJSONStatus(w, http.StatusUnauthorized, account.PublicError(err))
			return
		}
		limiter.success(key)
		store.Audit(account.AuditEntry{Username: p.Username, Action: "login_succeeded", RemoteAddr: r.RemoteAddr})
		expires, _ := store.LicenseExpiry(p.Username)
		writeJSON(w, map[string]any{"ok": true, "token": token, "username": p.Username, "activeUntil": expires})
	})
	mux.HandleFunc("/api/activation/redeem", func(w http.ResponseWriter, r *http.Request) {
		if b.deviceLicenses {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, user, ok := authenticateRequest(store, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var p struct {
			Key string `json:"key"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&p) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		expiry, err := store.RedeemActivationKey(user, p.Key)
		if err != nil {
			store.Audit(account.AuditEntry{Username: user, Action: "activation_failed", RemoteAddr: r.RemoteAddr, Detail: err.Error()})
			writeJSONStatus(w, 400, account.PublicError(err))
			return
		}
		store.Audit(account.AuditEntry{Username: user, Action: "activation_redeemed", RemoteAddr: r.RemoteAddr, Detail: expiry.Format(time.RFC3339)})
		writeJSON(w, map[string]any{"ok": true, "activeUntil": expiry})
	})
	mux.HandleFunc("/api/license", func(w http.ResponseWriter, r *http.Request) {
		if b.deviceLicenses {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, user, ok := authenticateRequest(store, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		expires, err := store.LicenseExpiry(user)
		if err != nil {
			writeJSONStatus(w, 500, account.PublicError(err))
			return
		}
		writeJSON(w, map[string]any{"ok": true, "username": user, "active": store.HasActiveLicense(user), "activeUntil": expires})
	})
	mux.HandleFunc("/api/logout", func(w http.ResponseWriter, r *http.Request) {
		if b.deviceLicenses {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token, user, ok := authenticateRequest(store, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := store.Logout(token); err != nil {
			writeJSONStatus(w, 500, account.PublicError(err))
			return
		}
		store.Audit(account.AuditEntry{Username: user, Action: "logout", RemoteAddr: r.RemoteAddr})
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		if b.deviceLicenses {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, user, ok := authenticateRequest(store, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		devices, err := store.ListDevices(user)
		if err != nil {
			writeJSONStatus(w, 500, account.PublicError(err))
			return
		}
		b.Lock()
		for i := range devices {
			waiting, isWaiting := b.devices[devices[i].ID]
			_, isActive := b.active[devices[i].ID]
			if (isWaiting && waiting.role == "agent") || isActive {
				devices[i].Online = true
			}
		}
		b.Unlock()
		writeJSON(w, map[string]any{"ok": true, "devices": devices})
	})
	mux.HandleFunc("/api/devices/revoke", func(w http.ResponseWriter, r *http.Request) {
		if b.deviceLicenses {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, user, ok := authenticateRequest(store, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var p struct {
			ID string `json:"id"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&p) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if err := store.RevokeDevice(user, p.ID); err != nil {
			writeJSONStatus(w, 400, account.PublicError(err))
			return
		}
		b.terminateDevice(user, p.ID, "device access was revoked")
		store.Audit(account.AuditEntry{Username: user, Action: "device_revoked", DeviceID: p.ID, RemoteAddr: r.RemoteAddr})
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/audit", func(w http.ResponseWriter, r *http.Request) {
		if b.deviceLicenses {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_, user, ok := authenticateRequest(store, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		entries, err := store.RecentAudit(user, 100)
		if err != nil {
			writeJSONStatus(w, 500, account.PublicError(err))
			return
		}
		writeJSON(w, map[string]any{"ok": true, "entries": entries})
	})
	if b.deviceLicenses {
		log.Printf("device website and administration: %s://%s", scheme, addr)
	} else {
		log.Printf("homepage and account API: %s://%s", scheme, addr)
	}
	server := &http.Server{Addr: addr, Handler: securityHeaders(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	var err error
	if useTLS {
		err = server.ListenAndServeTLS(certFile, keyFile)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil {
		log.Print(err)
	}
}

func registerStandaloneDeviceRoutes(mux *http.ServeMux, b *broker, adminKey string) {
	if enabled, err := b.accounts.DeviceAutoActivation(); err == nil {
		b.setAutomaticDeviceActivation(enabled)
	}
	csrfBytes := make([]byte, 24)
	if _, err := rand.Read(csrfBytes); err != nil {
		fallback := sha256.Sum256([]byte(time.Now().String() + adminKey))
		csrfBytes = fallback[:]
	}
	adminCSRF := base64.RawURLEncoding.EncodeToString(csrfBytes)
	mux.HandleFunc("/api/device/activate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var payload struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Key       string `json:"key"`
			PublicKey string `json:"publicKey"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&payload) != nil {
			writeJSONStatus(w, http.StatusBadRequest, account.PublicError(errors.New("请求格式错误")))
			return
		}
		publicKey, err := base64.StdEncoding.DecodeString(payload.PublicKey)
		if err != nil || payload.ID == "" || payload.Key == "" {
			writeJSONStatus(w, http.StatusBadRequest, account.PublicError(errors.New("设备码或激活码无效")))
			return
		}
		if err := b.accounts.RegisterLicensedDeviceNamed(payload.ID, publicKey, payload.Name); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, account.PublicError(err))
			return
		}
		expires, err := b.accounts.RedeemDeviceActivationKey(payload.ID, payload.Key)
		if err != nil {
			b.accounts.Audit(account.AuditEntry{Action: "device_activation_failed", DeviceID: payload.ID, RemoteAddr: r.RemoteAddr, Detail: err.Error()})
			writeJSONStatus(w, http.StatusBadRequest, account.PublicError(err))
			return
		}
		b.accounts.Audit(account.AuditEntry{Action: "device_activated", DeviceID: payload.ID, RemoteAddr: r.RemoteAddr, Detail: expires.Format(time.RFC3339)})
		writeJSON(w, map[string]any{"ok": true, "active": true, "activeUntil": expires})
	})
	mux.HandleFunc("/api/device/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if !b.permitLookup(r.RemoteAddr, 1) {
			http.Error(w, "查询过于频繁，请稍后重试", http.StatusTooManyRequests)
			return
		}
		if rawIDs := strings.TrimSpace(r.URL.Query().Get("ids")); rawIDs != "" {
			parts := strings.Split(rawIDs, ",")
			if len(parts) > 50 {
				writeJSONStatus(w, http.StatusBadRequest, account.PublicError(errors.New("最多查询 50 台设备")))
				return
			}
			statuses := make([]publicDeviceStatus, 0, len(parts))
			seen := make(map[string]bool, len(parts))
			for _, part := range parts {
				deviceID, valid := validPublicDeviceID(part)
				if !valid {
					writeJSONStatus(w, http.StatusBadRequest, account.PublicError(errors.New("设备码格式不正确")))
					return
				}
				if !seen[deviceID] {
					statuses = append(statuses, b.publicDeviceStatus(deviceID))
					seen[deviceID] = true
				}
			}
			writeJSON(w, map[string]any{"ok": true, "devices": statuses})
			return
		}
		deviceID, valid := validPublicDeviceID(r.URL.Query().Get("id"))
		if !valid {
			writeJSONStatus(w, http.StatusBadRequest, account.PublicError(errors.New("设备码格式不正确")))
			return
		}
		writeJSON(w, b.publicDeviceStatus(deviceID))
	})

	admin := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !adminAuthorized(r, adminKey) {
				w.Header().Set("WWW-Authenticate", `Basic realm="YuDesk 管理后台", charset="UTF-8"`)
				http.Error(w, "需要管理员密码", http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodPost {
				if r.ParseForm() != nil || subtle.ConstantTimeCompare([]byte(r.FormValue("_csrf")), []byte(adminCSRF)) != 1 {
					http.Error(w, "安全校验失败，请返回管理首页后重试", http.StatusForbidden)
					return
				}
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/admin", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		message := r.URL.Query().Get("message")
		generated, flashMessage, found := b.takeAdminFlash(r.URL.Query().Get("result"))
		if flashMessage != "" {
			message = flashMessage
		} else if r.URL.Query().Get("result") != "" && !found {
			message = "授权码显示结果已过期，请重新生成"
		}
		serveAdminPage(w, r, b, generated, message, adminCSRF)
	}))
	mux.HandleFunc("/admin/app.js", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = fmt.Fprint(w, adminScript)
	}))
	mux.HandleFunc("/admin/generate", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		if r.ParseForm() != nil {
			redirectAdmin(w, r, "请求无效，请重新提交")
			return
		}
		days, _ := strconv.Atoi(r.FormValue("days"))
		hours, _ := strconv.Atoi(r.FormValue("hours"))
		count, _ := strconv.Atoi(r.FormValue("count"))
		if count < 1 || count > 100 {
			redirectAdmin(w, r, "生成数量必须在 1 到 100 之间")
			return
		}
		duration, err := adminLicenseDuration(days, hours)
		if err != nil {
			redirectAdmin(w, r, err.Error())
			return
		}
		generated := make([]string, 0, count)
		for i := 0; i < count; i++ {
			key, err := b.accounts.GenerateActivationKey(duration, 90*24*time.Hour)
			if err != nil {
				redirectAdmin(w, r, "生成失败："+err.Error())
				return
			}
			generated = append(generated, key)
		}
		resultID, err := b.putAdminFlash(generated, fmt.Sprintf("已生成 %d 个授权码", len(generated)))
		if err != nil {
			redirectAdmin(w, r, "授权码已生成，但结果页面创建失败，请重新生成")
			return
		}
		target := adminReturnTarget(r)
		targetURL, _ := url.Parse(target)
		query := targetURL.Query()
		query.Set("result", resultID)
		targetURL.RawQuery = query.Encode()
		http.Redirect(w, r, targetURL.RequestURI(), http.StatusSeeOther)
	}))
	mux.HandleFunc("/admin/grant", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		if r.ParseForm() != nil {
			redirectAdmin(w, r, "请求无效，请重新提交")
			return
		}
		deviceID := b.canonicalDeviceID(strings.ToUpper(strings.TrimSpace(r.FormValue("device_id"))))
		days, _ := strconv.Atoi(r.FormValue("days"))
		hours, _ := strconv.Atoi(r.FormValue("hours"))
		duration, err := adminLicenseDuration(days, hours)
		if err != nil {
			redirectAdmin(w, r, err.Error())
			return
		}
		expires, err := b.accounts.GrantDeviceLicense(deviceID, duration)
		if err != nil {
			redirectAdmin(w, r, "授权失败："+err.Error())
			return
		}
		b.accounts.Audit(account.AuditEntry{Action: "admin_device_granted", DeviceID: deviceID, RemoteAddr: r.RemoteAddr, Detail: expires.Format(time.RFC3339)})
		redirectAdmin(w, r, "设备 "+deviceID+" 已授权至 "+formatAdminTime(expires)+"（北京时间）")
	}))
	mux.HandleFunc("/admin/settings/auto-activate", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		enabled := r.FormValue("enabled") == "1"
		if err := b.accounts.SetDeviceAutoActivation(enabled); err != nil {
			redirectAdmin(w, r, "免激活设置保存失败："+err.Error())
			return
		}
		b.setAutomaticDeviceActivation(enabled)
		automaticallyActivated := 0
		if enabled {
			deviceIDs := make(map[string]struct{})
			b.Lock()
			for deviceID := range b.controls {
				deviceIDs[deviceID] = struct{}{}
			}
			for deviceID, waitingDevice := range b.devices {
				if waitingDevice.role == "agent" {
					deviceIDs[deviceID] = struct{}{}
				}
			}
			for deviceID := range b.active {
				deviceIDs[deviceID] = struct{}{}
			}
			b.Unlock()
			for deviceID := range deviceIDs {
				granted, err := b.accounts.EnsurePermanentDeviceLicense(deviceID)
				if err != nil {
					redirectAdmin(w, r, "免激活已开启，但在线设备自动授权失败："+err.Error())
					return
				}
				if granted {
					automaticallyActivated++
					b.accounts.Audit(account.AuditEntry{Action: "device_auto_activated", DeviceID: deviceID, RemoteAddr: r.RemoteAddr, Detail: "permanent"})
				}
			}
		}
		action := "admin_auto_activation_disabled"
		message := "免激活已关闭；未授权设备必须使用授权码"
		if enabled {
			action = "admin_auto_activation_enabled"
			message = fmt.Sprintf("免激活已开启；新设备连接后自动永久授权，本次已激活 %d 台在线设备", automaticallyActivated)
		}
		b.accounts.Audit(account.AuditEntry{Action: action, RemoteAddr: r.RemoteAddr})
		redirectAdmin(w, r, message)
	}))
	mux.HandleFunc("/admin/revoke", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		if r.ParseForm() != nil {
			redirectAdmin(w, r, "请求无效，请重新提交")
			return
		}
		deviceID := b.canonicalDeviceID(strings.ToUpper(strings.TrimSpace(r.FormValue("device_id"))))
		if err := b.accounts.RevokeLicensedDevice(deviceID); err != nil {
			redirectAdmin(w, r, "吊销失败："+err.Error())
			return
		}
		b.terminateDevice("device:"+deviceID, deviceID, "administrator disabled this device")
		b.accounts.Audit(account.AuditEntry{Action: "admin_device_revoked", DeviceID: deviceID, RemoteAddr: r.RemoteAddr})
		redirectAdmin(w, r, "设备 "+deviceID+" 已禁用，退出指令已发送；再次运行也会被拒绝")
	}))
	mux.HandleFunc("/admin/unrevoke", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		deviceID := b.canonicalDeviceID(strings.ToUpper(strings.TrimSpace(r.FormValue("device_id"))))
		if err := b.accounts.UnrevokeLicensedDevice(deviceID); err != nil {
			redirectAdmin(w, r, "解禁失败："+err.Error())
			return
		}
		b.Lock()
		delete(b.stopRequests, deviceID)
		b.Unlock()
		b.accounts.Audit(account.AuditEntry{Action: "admin_device_unrevoked", DeviceID: deviceID, RemoteAddr: r.RemoteAddr})
		redirectAdmin(w, r, "设备 "+deviceID+" 已解禁；如授权已过期，仍需重新授权")
	}))
	mux.HandleFunc("/admin/name", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		if r.ParseForm() != nil {
			redirectAdmin(w, r, "请求无效，请重新提交")
			return
		}
		deviceID := b.canonicalDeviceID(strings.ToUpper(strings.TrimSpace(r.FormValue("device_id"))))
		name := strings.TrimSpace(r.FormValue("name"))
		if err := b.accounts.RenameLicensedDevice(deviceID, name); err != nil {
			redirectAdmin(w, r, "改名失败："+err.Error())
			return
		}
		b.accounts.Audit(account.AuditEntry{Action: "admin_device_renamed", DeviceID: deviceID, RemoteAddr: r.RemoteAddr, Detail: name})
		redirectAdmin(w, r, "设备名称已更新")
	}))
	mux.HandleFunc("/admin/disconnect", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		if r.ParseForm() != nil {
			redirectAdmin(w, r, "请求无效，请重新提交")
			return
		}
		deviceID := b.canonicalDeviceID(strings.ToUpper(strings.TrimSpace(r.FormValue("device_id"))))
		b.terminateDevice("device:"+deviceID, deviceID, "administrator disconnected this device")
		b.accounts.Audit(account.AuditEntry{Action: "admin_device_disconnected", DeviceID: deviceID, RemoteAddr: r.RemoteAddr})
		redirectAdmin(w, r, "设备连接已强制断开，被控端进程终止指令已发送")
	}))
	mux.HandleFunc("/admin/delete", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		if r.ParseForm() != nil {
			redirectAdmin(w, r, "请求无效，请重新提交")
			return
		}
		deviceID := b.canonicalDeviceID(strings.ToUpper(strings.TrimSpace(r.FormValue("device_id"))))
		if err := b.accounts.DeleteLicensedDevice(deviceID); err != nil {
			redirectAdmin(w, r, "删除失败："+err.Error())
			return
		}
		b.terminateDevice("device:"+deviceID, deviceID, "administrator deleted this device")
		b.accounts.Audit(account.AuditEntry{Action: "admin_device_deleted", DeviceID: deviceID, RemoteAddr: r.RemoteAddr})
		redirectAdmin(w, r, "设备记录已删除，退出指令已发送；再次运行时需重新授权")
	}))
	mux.HandleFunc("/admin/revoke-key", admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		if r.ParseForm() != nil {
			redirectAdmin(w, r, "请求无效，请重新提交")
			return
		}
		fingerprint := strings.ToUpper(strings.TrimSpace(r.FormValue("fingerprint")))
		if err := b.accounts.RevokeActivationKeyByFingerprint(fingerprint); err != nil {
			redirectAdmin(w, r, "授权码吊销失败："+err.Error())
			return
		}
		b.accounts.Audit(account.AuditEntry{Action: "admin_activation_key_revoked", RemoteAddr: r.RemoteAddr, Detail: fingerprint})
		redirectAdmin(w, r, "未使用的授权码已吊销")
	}))
}

type publicDeviceStatus struct {
	Name        string    `json:"name,omitempty"`
	DeviceCode  string    `json:"deviceCode,omitempty"`
	OK          bool      `json:"ok"`
	ID          string    `json:"id"`
	Active      bool      `json:"active"`
	Online      bool      `json:"online"`
	Connected   bool      `json:"connected"`
	Ready       bool      `json:"ready"`
	ActiveUntil time.Time `json:"activeUntil,omitempty"`
}

func validPublicDeviceID(value string) (string, bool) {
	deviceID := strings.ToUpper(strings.TrimSpace(value))
	deviceID = strings.ReplaceAll(deviceID, " ", "")
	if relay.IsDeviceCode(deviceID) {
		return deviceID, true
	}
	if len(deviceID) != 24 {
		return "", false
	}
	_, err := hex.DecodeString(deviceID)
	return deviceID, err == nil
}

func (b *broker) publicDeviceStatus(deviceID string) publicDeviceStatus {
	requestedID := deviceID
	deviceID = b.canonicalDeviceID(deviceID)
	name, shortCode := b.accounts.DevicePublicLabel(deviceID)
	expires, licenseErr := b.accounts.DeviceLicenseExpiry(deviceID)
	b.Lock()
	waitingDevice, waiting := b.devices[deviceID]
	_, connected := b.active[deviceID]
	control := b.controls[deviceID]
	online := connected || (waiting && waitingDevice.role == "agent") || (control != nil && !control.stopping)
	ready := waiting && waitingDevice.role == "agent" && !connected && (control == nil || !control.stopping)
	b.Unlock()
	return publicDeviceStatus{
		OK: true, ID: requestedID, Name: name, DeviceCode: shortCode, Active: licenseErr == nil && expires.After(time.Now()),
		Online: online, Connected: connected, Ready: ready && licenseErr == nil && expires.After(time.Now()), ActiveUntil: expires,
	}
}

func redirectAdmin(w http.ResponseWriter, r *http.Request, message string) {
	targetURL, _ := url.Parse(adminReturnTarget(r))
	query := targetURL.Query()
	if message != "" {
		query.Set("message", message)
	}
	targetURL.RawQuery = query.Encode()
	http.Redirect(w, r, targetURL.RequestURI(), http.StatusSeeOther)
}

func adminReturnTarget(r *http.Request) string {
	target := strings.TrimSpace(r.FormValue("_return"))
	parsed, err := url.Parse(target)
	if err != nil || parsed.IsAbs() || parsed.Path != "/admin" {
		return "/admin"
	}
	query := parsed.Query()
	query.Del("message")
	query.Del("result")
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.RequestURI()
}

func adminLicenseDuration(days, hours int) (time.Duration, error) {
	if hours < 0 || hours > 23 {
		return 0, errors.New("授权时长无效：小时必须在 0 到 23 之间")
	}
	if days < 0 || days > 3650 {
		return 0, errors.New("授权时长无效：天数必须在 0 到 3650 之间")
	}
	duration := time.Duration(days*24+hours) * time.Hour
	if duration < time.Hour || duration > 10*365*24*time.Hour {
		return 0, errors.New("授权时长必须在 1 小时到 10 年之间")
	}
	return duration, nil
}

func (b *broker) putAdminFlash(generated []string, message string) (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()
	b.Lock()
	defer b.Unlock()
	if b.adminFlashes == nil {
		b.adminFlashes = make(map[string]adminFlash)
	}
	for key, flash := range b.adminFlashes {
		if now.After(flash.expiresAt) {
			delete(b.adminFlashes, key)
		}
	}
	b.adminFlashes[id] = adminFlash{generated: append([]string(nil), generated...), message: message, expiresAt: now.Add(5 * time.Minute)}
	return id, nil
}

func (b *broker) takeAdminFlash(id string) ([]string, string, bool) {
	if id == "" {
		return nil, "", false
	}
	b.Lock()
	defer b.Unlock()
	flash, ok := b.adminFlashes[id]
	delete(b.adminFlashes, id)
	if !ok || time.Now().After(flash.expiresAt) {
		return nil, "", false
	}
	return flash.generated, flash.message, true
}

func adminAuthorized(r *http.Request, adminKey string) bool {
	username, password, ok := r.BasicAuth()
	if !ok || username != "admin" || adminKey == "" {
		return false
	}
	expected := sha256.Sum256([]byte(adminKey))
	provided := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(expected[:], provided[:]) == 1
}

type adminDeviceView struct {
	ShortCode                                      string
	ID, Name, PIN, Status, LastSeen, ActiveUntil   string
	ConnectionLabel, ConnectionClass, LicenseClass string
	Online, Connected, Permanent, PINAvailable     bool
}

type adminKeyView struct {
	Fingerprint, Duration, Status, RedeemBy, StatusClass string
	Revocable                                            bool
}

type adminAuditView struct {
	At, Action, ActionLabel, DeviceID, RemoteAddr, Detail string
}

type adminPager struct {
	Page, TotalPages, Total, Start, End int
	HasPrev, HasNext                    bool
	PrevURL, NextURL                    string
	Links                               []adminPageLink
}

type adminPageLink struct {
	Number  int
	URL     string
	Current bool
}

func serveAdminPage(w http.ResponseWriter, r *http.Request, b *broker, generated []string, message, csrfToken string) {
	queryValues := make(url.Values)
	if r != nil {
		for key, values := range r.URL.Query() {
			queryValues[key] = append([]string(nil), values...)
		}
	}
	queryValues.Del("message")
	queryValues.Del("result")
	deviceQuery := strings.TrimSpace(queryValues.Get("dq"))
	deviceState := adminAllowedFilter(queryValues.Get("ds"), "connected", "online", "offline")
	deviceLicense := adminAllowedFilter(queryValues.Get("dl"), "active", "unlicensed", "expired", "revoked")
	keyQuery := strings.TrimSpace(queryValues.Get("kq"))
	keyState := adminAllowedFilter(queryValues.Get("ks"), "available", "used", "expired", "revoked")
	auditQuery := strings.TrimSpace(queryValues.Get("aq"))

	devices, err := b.accounts.ListLicensedDevices(5000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	deviceViews := make([]adminDeviceView, 0, len(devices))
	onlineCount := 0
	connectedCount := 0
	idleCount := 0
	offlineCount := 0
	activeLicenseCount := 0
	expiredCount := 0
	revokedCount := 0
	unlicensedCount := 0
	b.Lock()
	for _, device := range devices {
		status := "未授权"
		licenseClass := "unlicensed"
		permanent := false
		if !device.RevokedAt.IsZero() {
			status = "已禁用"
			licenseClass = "revoked"
			revokedCount++
		} else if account.IsPermanentDeviceLicense(device.ActiveUntil) {
			status = "永久授权"
			licenseClass = "active"
			permanent = true
			activeLicenseCount++
		} else if device.ActiveUntil.After(now) {
			status = "授权有效"
			licenseClass = "active"
			activeLicenseCount++
		} else if !device.ActiveUntil.IsZero() {
			status = "已过期"
			licenseClass = "expired"
			expiredCount++
		} else {
			unlicensedCount++
		}
		waitingDevice, waiting := b.devices[device.ID]
		_, active := b.active[device.ID]
		control := b.controls[device.ID]
		online := (control != nil && !control.stopping) || (waiting && waitingDevice.role == "agent") || active
		if online {
			onlineCount++
		}
		if active {
			connectedCount++
		} else if online {
			idleCount++
		} else {
			offlineCount++
		}
		connectionLabel := "离线"
		connectionClass := "offline"
		if active {
			connectionLabel = "连接中"
			connectionClass = "connected"
		} else if online {
			connectionLabel = "在线 · 未被连接"
			connectionClass = "online"
		}
		activeUntil := "-"
		if permanent {
			activeUntil = "永久"
		} else if !device.ActiveUntil.IsZero() {
			activeUntil = formatAdminTime(device.ActiveUntil)
		}
		name := device.Name
		if name == "" {
			name = "未命名设备"
		}
		lastSeen := formatAdminTime(adminLastSeen(device.LastSeen, control))
		pairingPIN := device.PairingPIN
		pinAvailable := pairingPIN != ""
		if !pinAvailable {
			pairingPIN = "未上报"
		}
		deviceViews = append(deviceViews, adminDeviceView{
			ShortCode: device.Code,
			ID:        device.ID, Name: name, PIN: pairingPIN, Status: status, LastSeen: lastSeen, ActiveUntil: activeUntil,
			ConnectionLabel: connectionLabel, ConnectionClass: connectionClass, LicenseClass: licenseClass,
			Online: online, Connected: active, Permanent: permanent, PINAvailable: pinAvailable,
		})
	}
	b.Unlock()
	filteredDevices := make([]adminDeviceView, 0, len(deviceViews))
	deviceNeedle := strings.ToLower(deviceQuery)
	for _, device := range deviceViews {
		if deviceNeedle != "" && !strings.Contains(strings.ToLower(device.Name+" "+device.ID+" "+device.ShortCode+" "+device.PIN), deviceNeedle) {
			continue
		}
		if deviceState != "" && device.ConnectionClass != deviceState {
			continue
		}
		if deviceLicense != "" && device.LicenseClass != deviceLicense {
			continue
		}
		filteredDevices = append(filteredDevices, device)
	}
	devicePage, devicePager := paginateAdmin(filteredDevices, adminPageNumber(queryValues.Get("dp")), 20, "dp", queryValues)

	keys, err := b.accounts.ListActivationKeys(5000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	keyViews := make([]adminKeyView, 0, len(keys))
	availableKeyCount := 0
	usedKeyCount := 0
	invalidKeyCount := 0
	for _, key := range keys {
		status := "未使用"
		statusClass := "available"
		if !key.RevokedAt.IsZero() {
			status = "已吊销"
			statusClass = "revoked"
			invalidKeyCount++
		} else if key.RedeemedBy != "" {
			status = "已使用：" + strings.TrimPrefix(key.RedeemedBy, "device:")
			statusClass = "used"
			usedKeyCount++
		} else if now.After(key.RedeemBy) {
			status = "兑换期已过"
			statusClass = "expired"
			invalidKeyCount++
		} else {
			availableKeyCount++
		}
		keyViews = append(keyViews, adminKeyView{Fingerprint: key.Fingerprint, Duration: formatAdminDuration(key.Duration), Status: status, RedeemBy: key.RedeemBy.In(adminTimeZone).Format("2006-01-02"), StatusClass: statusClass, Revocable: statusClass == "available"})
	}
	filteredKeys := make([]adminKeyView, 0, len(keyViews))
	keyNeedle := strings.ToLower(keyQuery)
	for _, key := range keyViews {
		if keyNeedle != "" && !strings.Contains(strings.ToLower(key.Fingerprint+" "+key.Status+" "+key.Duration), keyNeedle) {
			continue
		}
		if keyState != "" && key.StatusClass != keyState {
			continue
		}
		filteredKeys = append(filteredKeys, key)
	}
	keyPage, keyPager := paginateAdmin(filteredKeys, adminPageNumber(queryValues.Get("kp")), 20, "kp", queryValues)

	audits, err := b.accounts.RecentAuditAll(1000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	auditViews := make([]adminAuditView, 0, len(audits))
	for _, entry := range audits {
		auditViews = append(auditViews, adminAuditView{At: formatAdminTime(entry.At), Action: entry.Action, ActionLabel: adminAuditLabel(entry.Action), DeviceID: entry.DeviceID, RemoteAddr: entry.RemoteAddr, Detail: entry.Detail})
	}
	filteredAudits := make([]adminAuditView, 0, len(auditViews))
	auditNeedle := strings.ToLower(auditQuery)
	for _, entry := range auditViews {
		haystack := entry.Action + " " + entry.ActionLabel + " " + entry.DeviceID + " " + entry.RemoteAddr + " " + entry.Detail
		if auditNeedle == "" || strings.Contains(strings.ToLower(haystack), auditNeedle) {
			filteredAudits = append(filteredAudits, entry)
		}
	}
	auditPage, auditPager := paginateAdmin(filteredAudits, adminPageNumber(queryValues.Get("ap")), 30, "ap", queryValues)
	returnURL := "/admin"
	if encoded := queryValues.Encode(); encoded != "" {
		returnURL += "?" + encoded
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = adminPage.Execute(w, map[string]any{
		"Devices": devicePage, "Keys": keyPage, "Generated": generated, "Message": message, "Audits": auditPage,
		"OnlineCount": onlineCount, "ConnectedCount": connectedCount, "IdleCount": idleCount, "OfflineCount": offlineCount,
		"TotalCount": len(deviceViews), "ActiveLicenseCount": activeLicenseCount, "ExpiredCount": expiredCount,
		"RevokedCount": revokedCount, "UnlicensedCount": unlicensedCount, "AttentionCount": expiredCount + revokedCount + unlicensedCount,
		"AvailableKeyCount": availableKeyCount, "UsedKeyCount": usedKeyCount, "InvalidKeyCount": invalidKeyCount,
		"UpdatedAt": formatAdminTime(now), "CSRF": csrfToken, "ReturnURL": returnURL, "AutoActivate": b.automaticDeviceActivation(),
		"DeviceQuery": deviceQuery, "DeviceState": deviceState, "DeviceLicense": deviceLicense, "DevicePager": devicePager,
		"KeyQuery": keyQuery, "KeyState": keyState, "KeyPager": keyPager, "AuditQuery": auditQuery, "AuditPager": auditPager,
		"DeviceClearURL": adminClearURL(queryValues, "dq", "ds", "dl", "dp"),
		"KeyClearURL":    adminClearURL(queryValues, "kq", "ks", "kp"), "AuditClearURL": adminClearURL(queryValues, "aq", "ap"),
	})
}

func adminAllowedFilter(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return ""
}

func adminPageNumber(value string) int {
	page, err := strconv.Atoi(value)
	if err != nil || page < 1 {
		return 1
	}
	return page
}

func paginateAdmin[T any](items []T, page, pageSize int, pageParam string, query url.Values) ([]T, adminPager) {
	total := len(items)
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	startIndex := (page - 1) * pageSize
	endIndex := min(startIndex+pageSize, total)
	pager := adminPager{Page: page, TotalPages: totalPages, Total: total, HasPrev: page > 1, HasNext: page < totalPages}
	if total > 0 {
		pager.Start = startIndex + 1
		pager.End = endIndex
	}
	if pager.HasPrev {
		pager.PrevURL = adminPageURL(query, pageParam, page-1)
	}
	if pager.HasNext {
		pager.NextURL = adminPageURL(query, pageParam, page+1)
	}
	linkStart := max(1, page-2)
	linkEnd := min(totalPages, linkStart+4)
	linkStart = max(1, linkEnd-4)
	for number := linkStart; number <= linkEnd; number++ {
		pager.Links = append(pager.Links, adminPageLink{Number: number, URL: adminPageURL(query, pageParam, number), Current: number == page})
	}
	return items[startIndex:endIndex], pager
}

func adminPageURL(query url.Values, pageParam string, page int) string {
	copyValues := make(url.Values, len(query))
	for key, values := range query {
		copyValues[key] = append([]string(nil), values...)
	}
	copyValues.Del("message")
	copyValues.Del("result")
	if page <= 1 {
		copyValues.Del(pageParam)
	} else {
		copyValues.Set(pageParam, strconv.Itoa(page))
	}
	if encoded := copyValues.Encode(); encoded != "" {
		return "/admin?" + encoded
	}
	return "/admin"
}

func adminClearURL(query url.Values, keys ...string) string {
	copyValues := make(url.Values, len(query))
	for key, values := range query {
		copyValues[key] = append([]string(nil), values...)
	}
	for _, key := range keys {
		copyValues.Del(key)
	}
	if encoded := copyValues.Encode(); encoded != "" {
		return "/admin?" + encoded
	}
	return "/admin"
}

func adminAuditLabel(action string) string {
	labels := map[string]string{
		"admin_device_granted":           "管理员授权/续期",
		"admin_device_revoked":           "禁用设备",
		"admin_device_unrevoked":         "解禁设备",
		"admin_device_disconnected":      "断开并退出设备",
		"admin_device_deleted":           "删除设备",
		"admin_device_renamed":           "修改设备名称",
		"admin_activation_key_revoked":   "吊销授权码",
		"device_activated":               "设备兑换授权码",
		"device_auto_activated":          "设备自动永久激活",
		"device_activation_failed":       "设备激活失败",
		"device_registered":              "设备首次登记",
		"device_license_denied":          "设备授权校验拒绝",
		"relay_paired":                   "建立远程连接",
		"relay_disconnected":             "远程连接结束",
		"relay_auth_failed":              "中转身份校验失败",
		"admin_auto_activation_enabled":  "开启免激活",
		"admin_auto_activation_disabled": "关闭免激活",
	}
	if label := labels[action]; label != "" {
		return label
	}
	return action
}

func formatAdminDuration(duration time.Duration) string {
	days := int(duration / (24 * time.Hour))
	hours := int((duration % (24 * time.Hour)) / time.Hour)
	if days > 0 && hours > 0 {
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	}
	if days > 0 {
		return fmt.Sprintf("%d 天", days)
	}
	return fmt.Sprintf("%d 小时", hours)
}

func serveDownloads(addr, downloadDir, fingerprint, publicRelay, publicAccount string, deviceMode bool) {
	mux := http.NewServeMux()
	registerDownloadRoutes(mux, downloadDir, func(r *http.Request) guideConfig {
		return makeGuideConfig(r, "http", fingerprint, publicRelay, publicAccount, deviceMode)
	})
	log.Printf("HTTP download homepage (no account API): http://%s", addr)
	server := &http.Server{Addr: addr, Handler: securityHeaders(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		log.Print(err)
	}
}

func registerDownloadRoutes(mux *http.ServeMux, downloadDir string, guide func(*http.Request) guideConfig) {
	registerHomepageSite(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveDownloadHome(w, downloadDir)
	})
	mux.HandleFunc("/guide", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/guide" {
			http.NotFound(w, r)
			return
		}
		serveGuide(w, guide(r))
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		serveDownload(w, r, downloadDir)
	})
	mux.HandleFunc("/SHA256SUMS.txt", func(w http.ResponseWriter, r *http.Request) {
		serveNamedDownloadFile(w, r, downloadDir, "SHA256SUMS.txt", "text/plain; charset=utf-8")
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"ok": true}) })
}

func makeGuideConfig(r *http.Request, scheme, fingerprint, publicRelay, publicAccount string, deviceMode bool) guideConfig {
	if publicRelay == "" {
		publicRelay = "relay.example.com:9347"
	}
	if publicAccount == "" {
		publicAccount = scheme + "://" + r.Host
	}
	return guideConfig{AccountServer: strings.TrimRight(publicAccount, "/"), RelayServer: publicRelay, Fingerprint: fingerprint, DeviceMode: deviceMode}
}

func writeRelayRejection(connection net.Conn, code, message string) error {
	message = strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(message))
	_, err := fmt.Fprintf(connection, "ERR %s %s\n", code, message)
	return err
}

func (b *broker) takeStopRequest(deviceID string) (string, bool) {
	b.Lock()
	defer b.Unlock()
	deviceID = strings.ToUpper(deviceID)
	reason, ok := b.stopRequests[deviceID]
	if ok {
		delete(b.stopRequests, deviceID)
	}
	return reason, ok
}

func (b *broker) clearStopRequest(deviceID, reason string) {
	b.Lock()
	defer b.Unlock()
	deviceID = strings.ToUpper(deviceID)
	if b.stopRequests[deviceID] == reason {
		delete(b.stopRequests, deviceID)
	}
}

func (b *broker) terminateDevice(owner, deviceID, reason string) bool {
	deviceID = strings.ToUpper(deviceID)
	var directStop net.Conn
	var connections []net.Conn
	found := false
	b.Lock()
	control := b.controls[deviceID]
	if control != nil && owner == "device:"+deviceID {
		found = true
		control.stopping = true
		select {
		case control.stop <- reason:
		default:
		}
	}
	if waiting, ok := b.devices[deviceID]; ok && waiting.owner == owner {
		found = true
		delete(b.devices, deviceID)
		if waiting.role == "agent" && control == nil {
			directStop = waiting.conn
			if b.stopRequests == nil {
				b.stopRequests = map[string]string{}
			}
			b.stopRequests[deviceID] = reason
		} else {
			connections = append(connections, waiting.conn)
		}
		waiting.ready <- nil
	}
	if active, ok := b.active[deviceID]; ok && active.owner == owner {
		found = true
		delete(b.active, deviceID)
		connections = append(connections, active.agent, active.viewer)
		if active.agent != nil && control == nil {
			if b.stopRequests == nil {
				b.stopRequests = map[string]string{}
			}
			b.stopRequests[deviceID] = reason
		}
	}
	// Legacy or not-yet-authorized clients can be between short connection
	// attempts and therefore absent from the live maps. Preserve one stop for
	// their next handshake so an administrator can still terminate the process.
	if owner == "device:"+deviceID && control == nil && directStop == nil {
		if b.stopRequests == nil {
			b.stopRequests = map[string]string{}
		}
		b.stopRequests[deviceID] = reason
		found = true
	}
	b.Unlock()
	if directStop != nil {
		if writeRelayRejection(directStop, "STOP", reason) == nil {
			b.clearStopRequest(deviceID, reason)
		}
		_ = directStop.Close()
	}
	for _, connection := range connections {
		if connection != nil {
			_ = connection.Close()
		}
	}
	return found
}

func (b *broker) disconnectDevice(owner, deviceID string) {
	deviceID = strings.ToUpper(deviceID)
	var connections []net.Conn
	b.Lock()
	if waiting, ok := b.devices[deviceID]; ok && waiting.owner == owner {
		delete(b.devices, deviceID)
		connections = append(connections, waiting.conn)
		waiting.ready <- nil
	}
	if active, ok := b.active[deviceID]; ok && active.owner == owner {
		delete(b.active, deviceID)
		connections = append(connections, active.agent, active.viewer)
	}
	b.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}
func authenticateRequest(store *account.Store, r *http.Request) (string, string, bool) {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(value, "Bearer ") {
		return "", "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
	user, ok := store.Authenticate(token)
	return token, user, ok
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type downloadLink struct {
	Name, Description, Path, Size, Version string
	Available                              bool
}

type downloadPlatform struct {
	Name, Detail string
	Links        []downloadLink
}

var downloadLayout = []downloadPlatform{
	{Name: "Windows", Detail: "Windows 10/11 · x64", Links: componentLinks("windows-amd64", ".exe")},
	{Name: "Linux", Detail: "Linux desktop · x64", Links: componentLinks("linux-amd64", "")},
	{Name: "macOS Intel", Detail: "Intel Mac · x64", Links: componentLinks("darwin-amd64", "")},
	{Name: "macOS Apple Silicon", Detail: "M1/M2/M3/M4 · arm64", Links: componentLinks("darwin-arm64", "")},
	{Name: "Android 预览版", Detail: "Android 8+ · 需真机验收", Links: []downloadLink{{Name: "YuDesk Android", Description: "控制 / 授权共享屏幕 · 预览测试", Path: "android/yudesk.apk"}}},
}

func componentLinks(platform, suffix string) []downloadLink {
	return []downloadLink{
		{Name: "YuDesk", Description: "控制与被控合一 · 双击运行", Path: platform + "/yudesk" + suffix},
	}
}

func downloadPlatforms(root string) []downloadPlatform {
	result := make([]downloadPlatform, len(downloadLayout))
	for i, platform := range downloadLayout {
		result[i] = platform
		result[i].Links = append([]downloadLink(nil), platform.Links...)
		for j := range result[i].Links {
			path := filepath.Join(root, filepath.FromSlash(result[i].Links[j].Path))
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				result[i].Links[j].Available = true
				result[i].Links[j].Size = humanSize(info.Size())
				result[i].Links[j].Version = fmt.Sprintf("%x-%x", info.ModTime().UnixNano(), info.Size())
			}
		}
	}
	return result
}

func serveDownloadHome(w http.ResponseWriter, root string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	version, date := "", "待发布"
	if release, err := releaseinfo.Read(root); err == nil {
		version, date = release.Version, release.Date()+"（北京时间）"
	}
	_ = downloadPage.Execute(w, map[string]any{"Platforms": downloadPlatforms(root), "Checksums": regularFile(filepath.Join(root, "SHA256SUMS.txt")), "Version": version, "PublishedAt": date})
}

type guideConfig struct {
	AccountServer, RelayServer, Fingerprint string
	DeviceMode                              bool
}

func serveGuide(w http.ResponseWriter, config guideConfig) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = guidePage.Execute(w, config)
}

func serveDownload(w http.ResponseWriter, r *http.Request, root string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, "/download/")
	// Old bookmarks download the unified application, not stale role binaries.
	for _, platform := range []string{"windows-amd64", "linux-amd64", "darwin-amd64", "darwin-arm64"} {
		suffix := ""
		if platform == "windows-amd64" {
			suffix = ".exe"
		}
		if relative == platform+"/yudesk-agent"+suffix || relative == platform+"/yudesk-viewer"+suffix {
			http.Redirect(w, r, "/download/"+platform+"/yudesk"+suffix, http.StatusTemporaryRedirect)
			return
		}
	}
	allowed := false
	for _, platform := range downloadLayout {
		for _, link := range platform.Links {
			if relative == link.Path {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	// The API's 30-second response deadline can truncate multi-megabyte
	// downloads on a slow link. Extend only validated download responses,
	// including range/resume requests; keep the rest of the server bounded.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		http.Error(w, "download temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	filename := filepath.Base(path)
	if release, err := releaseinfo.Read(root); err == nil {
		platform := filepath.Dir(relative)
		if platform == "android" {
			platform = "android-preview"
		}
		filename = releaseinfo.Filename(platform, filepath.Ext(path), release.Version)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if strings.HasSuffix(relative, ".apk") {
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	http.ServeFile(w, r, path)
}

func serveNamedDownloadFile(w http.ResponseWriter, r *http.Request, root, name, contentType string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := filepath.Join(root, name)
	if !regularFile(path) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	http.ServeFile(w, r, path)
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func humanSize(size int64) string {
	if size >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	}
	if size >= 1<<10 {
		return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
	}
	return fmt.Sprintf("%d B", size)
}

var downloadPage = template.Must(template.New("downloads").Funcs(homepageTemplateFuncs).Parse(downloadPageTemplate))

var adminPage = template.Must(template.New("admin").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>YuDesk 服务器管理</title>
<style>
*{box-sizing:border-box}html{scroll-behavior:smooth}body{margin:0;background:#07111f;color:#eaf2ff;font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}a{color:inherit}.topbar{position:sticky;top:0;z-index:20;border-bottom:1px solid #20344c;background:#081422ed;backdrop-filter:blur(14px)}.topbar-inner{max-width:1500px;margin:auto;min-height:62px;padding:10px 22px;display:flex;align-items:center;gap:18px}.brand{font-size:21px;font-weight:800;text-decoration:none;letter-spacing:.2px}.brand span{color:#67a7ff}.nav{display:flex;gap:5px;overflow:auto}.nav a,.top-action{padding:8px 11px;border-radius:8px;text-decoration:none;color:#9db0c8;white-space:nowrap}.nav a:hover,.top-action:hover{background:#12263b;color:#fff}.top-actions{margin-left:auto;display:flex;align-items:center;gap:8px}.refresh-state{color:#72869f;font-size:12px}.top-action{border:1px solid #2c425d;background:#0e1d2d;cursor:pointer}main{max-width:1500px;margin:auto;padding:28px 22px 70px}.hero{display:flex;align-items:flex-end;justify-content:space-between;gap:20px;margin-bottom:18px}.hero h1{font-size:34px;line-height:1.15;margin:0 0 7px}.hero p{margin:0;color:#8fa4bf}.updated{color:#70849e;text-align:right;font-size:12px}.stats{display:grid;grid-template-columns:repeat(6,minmax(130px,1fr));gap:12px;margin:18px 0}.stat{position:relative;overflow:hidden;min-height:112px;padding:16px 17px;background:linear-gradient(145deg,#112135,#0d1928);border:1px solid #243953;border-radius:14px}.stat:after{content:"";position:absolute;width:70px;height:70px;right:-28px;top:-30px;border-radius:50%;background:var(--accent,#67a7ff);opacity:.12}.stat-label{color:#8fa4bf;font-size:13px}.stat-value{display:block;margin-top:5px;font-size:30px;line-height:1;font-weight:800}.stat-note{display:block;margin-top:8px;color:#71859e;font-size:12px}.stat.online-card{--accent:#55d99c}.stat.connected-card{--accent:#ffcb67}.stat.warning-card{--accent:#ff7d8c}.message{display:flex;align-items:center;gap:10px;margin:14px 0;padding:12px 15px;border:1px solid #285174;border-radius:10px;background:#0e2a42;color:#a9d6ff}.message:before{content:"✓";display:grid;place-items:center;width:22px;height:22px;border-radius:50%;background:#216cae;color:white;font-weight:800}.grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.panel{background:#0e1b2b;border:1px solid #243953;border-radius:15px;margin:14px 0;box-shadow:0 16px 35px #0002}.panel-head{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:17px 19px;border-bottom:1px solid #20344c}.panel-head h2{margin:0;font-size:19px}.panel-head p{margin:3px 0 0;color:#8196b0;font-size:13px}.panel-body{padding:18px}.count-pills{display:flex;gap:7px;flex-wrap:wrap}.count-pill{padding:5px 9px;border-radius:99px;background:#13253a;color:#9eb1c8;font-size:12px}.form-grid{display:grid;grid-template-columns:repeat(4,minmax(90px,1fr));gap:10px;align-items:end}.form-grid.grant{grid-template-columns:minmax(170px,2fr) 1fr 1fr auto}.field{display:flex;flex-direction:column;gap:5px;color:#93a7c0;font-size:12px}input,select,button{font:inherit}input,select{width:100%;min-height:39px;border-radius:8px;border:1px solid #304963;padding:8px 10px;background:#081422;color:#edf5ff;outline:none}input:focus,select:focus{border-color:#4f99ef;box-shadow:0 0 0 3px #2f7dce2c}button,.button{display:inline-flex;align-items:center;justify-content:center;gap:5px;min-height:36px;border:0;border-radius:8px;padding:8px 12px;background:#2878e8;color:white;text-decoration:none;cursor:pointer;white-space:nowrap}button:hover,.button:hover{filter:brightness(1.1)}button.secondary,.button.secondary{background:#1a3048;color:#c6d7e9;border:1px solid #304963}button.warning{background:#95641e}button.danger{background:#a43b49}button.ghost{min-height:30px;padding:5px 8px;background:transparent;border:1px solid #334b67;color:#a9bed5;font-size:12px}.generated{border-color:#2c7258;background:linear-gradient(145deg,#102b2a,#0e1b2b)}.generated-list{display:grid;grid-template-columns:repeat(2,minmax(260px,1fr));gap:9px}.generated-key{display:flex;align-items:center;gap:8px;padding:11px 12px;border-radius:9px;background:#071821;border:1px solid #275445}.mono{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;color:#7ce0ad;word-break:break-all}.generated-key code{flex:1}.toolbar{display:flex;align-items:center;gap:9px;flex-wrap:wrap;padding:13px 18px;border-bottom:1px solid #20344c}.toolbar .search{flex:1 1 230px}.toolbar select{width:auto;min-width:145px}.visible-count{margin-left:auto;color:#7f94ad;font-size:12px}.table-wrap{overflow:auto;max-height:610px}table{width:100%;border-collapse:separate;border-spacing:0}th,td{text-align:left;padding:11px 12px;border-bottom:1px solid #1e3248;white-space:nowrap;vertical-align:middle}th{position:sticky;top:0;z-index:2;background:#102035;color:#8fa4bf;font-size:12px;font-weight:650}tbody tr:hover{background:#11243a}tbody tr[hidden]{display:none}.device-name-form{display:flex;gap:6px;min-width:225px}.device-name-form input{min-width:130px;min-height:34px}.device-name-form button{min-height:34px;padding:7px 9px}.code-cell{display:flex;align-items:center;gap:7px}.badge{display:inline-flex;align-items:center;gap:6px;padding:5px 9px;border-radius:99px;font-size:12px;font-weight:650;background:#182b41;color:#a9bdd3}.badge:before{content:"";width:7px;height:7px;border-radius:50%;background:currentColor}.badge.online,.badge.active,.badge.available{color:#6fe0a8;background:#12372f}.badge.connected{color:#ffd071;background:#3d3219}.badge.offline,.badge.used{color:#8da1b8;background:#172638}.badge.expired,.badge.unlicensed{color:#f0ba69;background:#3b2c17}.badge.revoked{color:#ff8d9a;background:#3d2029}.actions{display:flex;align-items:center;gap:6px}.actions form{margin:0}.actions button{min-height:32px;padding:6px 9px;font-size:12px}.empty{padding:36px!important;text-align:center;color:#71869f}.audit-action{color:#b7cbe1}.muted{color:#71869f}.detail-cell{max-width:360px;overflow:hidden;text-overflow:ellipsis}.footnote{margin-top:13px;color:#6f839b;font-size:12px}.no-results{display:none;padding:28px;text-align:center;color:#8297b0}.no-results.visible{display:block}.pagination{display:flex;align-items:center;justify-content:flex-end;gap:6px;flex-wrap:wrap;padding:13px 18px;border-top:1px solid #20344c}.pagination-info{margin-right:auto;color:#8297af;font-size:12px}.page-link{display:grid;place-items:center;min-width:34px;height:34px;padding:0 9px;border:1px solid #304963;border-radius:8px;background:#102238;color:#b8cbe0;text-decoration:none}.page-link:hover{border-color:#5799e6;color:#fff}.page-link.current{border-color:#2878e8;background:#2878e8;color:#fff}.page-link.disabled{opacity:.4;pointer-events:none}.setting-row{display:flex;align-items:center;gap:18px;justify-content:space-between}.setting-copy{max-width:800px}.setting-copy strong{display:block;font-size:16px;margin-bottom:4px}.setting-copy span{color:#8297b0}.setting-state{display:flex;align-items:center;gap:10px;white-space:nowrap}
@media(max-width:1250px){.stats{grid-template-columns:repeat(3,1fr)}}@media(max-width:900px){.grid{grid-template-columns:1fr}.generated-list{grid-template-columns:1fr}.form-grid,.form-grid.grant{grid-template-columns:1fr 1fr}.nav{display:none}}@media(max-width:620px){.topbar-inner{padding:9px 12px}.refresh-state{display:none}main{padding:20px 11px 55px}.hero,.setting-row{align-items:flex-start;flex-direction:column}.setting-state{white-space:normal}.hero h1{font-size:27px}.updated{text-align:left}.stats{grid-template-columns:1fr 1fr;gap:8px}.stat{min-height:96px;padding:13px}.stat-value{font-size:25px}.form-grid,.form-grid.grant{grid-template-columns:1fr}.panel-head{align-items:flex-start;flex-direction:column}.toolbar select{width:100%}.visible-count{margin-left:0}.pagination{justify-content:flex-start}.pagination-info{width:100%}}
</style></head>
<body data-generated="{{if .Generated}}1{{else}}0{{end}}" data-return="{{.ReturnURL}}"><header class="topbar"><div class="topbar-inner"><a class="brand" href="/admin"><span>Yu</span>Desk 管理</a><nav class="nav"><a href="#settings">服务器设置</a><a href="#devices">设备</a><a href="#licenses">授权码</a><a href="#audits">审计记录</a><a href="/">软件下载</a></nav><div class="top-actions"><span class="refresh-state" id="refresh-state">30 秒后异步刷新</span><a class="top-action" id="refresh-now" href="{{.ReturnURL}}">立即刷新</a></div></div></header>
<main><section class="hero"><div><h1>服务器管理中心</h1><p>管理在线设备、连接状态、使用授权和安全操作。所有时间均为北京时间（UTC+8），最后在线为设备最近一次已确认响应时间。</p></div><div class="updated">数据更新时间 · 北京时间<br><strong>{{.UpdatedAt}}</strong></div></section>
{{if .Message}}<div class="message" role="status">{{.Message}}</div>{{end}}
<section class="stats" aria-label="状态总览"><article class="stat"><span class="stat-label">设备总数</span><strong class="stat-value">{{.TotalCount}}</strong><span class="stat-note">已登记的被控端</span></article><article class="stat online-card"><span class="stat-label">当前在线</span><strong class="stat-value">{{.OnlineCount}}</strong><span class="stat-note">含连接中设备</span></article><article class="stat connected-card"><span class="stat-label">连接中</span><strong class="stat-value">{{.ConnectedCount}}</strong><span class="stat-note">正在远程控制</span></article><article class="stat"><span class="stat-label">在线待连接</span><strong class="stat-value">{{.IdleCount}}</strong><span class="stat-note">在线 · 未被连接</span></article><article class="stat"><span class="stat-label">离线</span><strong class="stat-value">{{.OfflineCount}}</strong><span class="stat-note">当前未连接服务器</span></article><article class="stat warning-card"><span class="stat-label">授权需处理</span><strong class="stat-value">{{.AttentionCount}}</strong><span class="stat-note">未授权 / 过期 / 禁用</span></article></section>
{{if .Generated}}<section class="panel generated" id="generated"><div class="panel-head"><div><h2>新授权码</h2><p>完整授权码只在本页显示一次，请现在复制并妥善保存。</p></div><button type="button" class="secondary" id="copy-all-keys">复制全部</button></div><div class="panel-body"><div class="generated-list">{{range .Generated}}<div class="generated-key"><code class="mono">{{.}}</code><button type="button" class="ghost" data-copy="{{.}}">复制</button></div>{{end}}</div></div></section>{{end}}
<section class="panel" id="settings"><div class="panel-head"><div><h2>服务器设置</h2><p>控制新被控端是否必须输入激活码。</p></div></div><div class="panel-body setting-row"><div class="setting-copy"><strong>新设备免激活</strong><span>{{if .AutoActivate}}当前已开启。未授权被控端连接服务器后会自动获得永久授权。{{else}}当前已关闭。未授权被控端必须输入授权码或由管理员直接授权。{{end}}</span></div><div class="setting-state"><span class="badge {{if .AutoActivate}}active{{else}}offline{{end}}">{{if .AutoActivate}}已开启{{else}}已关闭{{end}}</span><form method="post" action="/admin/settings/auto-activate" data-confirm="{{if .AutoActivate}}关闭后，尚未激活的新设备必须输入授权码。确定关闭吗？{{else}}开启后，新连接且未授权的设备将获得永久授权。确定开启吗？{{end}}"><input type="hidden" name="_csrf" value="{{.CSRF}}"><input type="hidden" name="_return" value="{{.ReturnURL}}"><input type="hidden" name="enabled" value="{{if .AutoActivate}}0{{else}}1{{end}}"><button type="submit" class="{{if .AutoActivate}}warning{{end}}">{{if .AutoActivate}}关闭免激活{{else}}开启免激活{{end}}</button></form></div></div></section>
<div class="grid"><section class="panel"><div class="panel-head"><div><h2>生成授权码</h2><p>用户可在被控端页面兑换，最长可生成 100 个。</p></div></div><div class="panel-body"><form method="post" action="/admin/generate" class="form-grid"><input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="_return" value="{{.ReturnURL}}"><label class="field">授权天数<input name="days" type="number" min="0" max="3650" value="30" required></label><label class="field">额外小时<input name="hours" type="number" min="0" max="23" value="0" required></label><label class="field">生成数量<input name="count" type="number" min="1" max="100" value="1" required></label><button type="submit">生成授权码</button></form></div></section>
<section class="panel"><div class="panel-head"><div><h2>直接授权设备</h2><p>按设备码授权或续期；续期会在原有效期上累加。</p></div></div><div class="panel-body"><form method="post" action="/admin/grant" class="form-grid grant"><input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="_return" value="{{.ReturnURL}}"><label class="field">设备码<input name="device_id" placeholder="输入完整设备码" autocomplete="off" required></label><label class="field">天数<input name="days" type="number" min="0" max="3650" value="30" required></label><label class="field">小时<input name="hours" type="number" min="0" max="23" value="0" required></label><button type="submit">授权 / 续期</button></form></div></section></div>
<section class="panel" id="devices"><div class="panel-head"><div><h2>设备管理</h2><p>每页 20 台，可按名称、设备码、PIN、连接状态和授权状态查询。</p></div><div class="count-pills"><span class="count-pill">有效授权 {{.ActiveLicenseCount}}</span><span class="count-pill">已过期 {{.ExpiredCount}}</span><span class="count-pill">已禁用 {{.RevokedCount}}</span><span class="count-pill">未授权 {{.UnlicensedCount}}</span></div></div><form class="toolbar" method="get" action="/admin"><input type="hidden" name="kq" value="{{.KeyQuery}}"><input type="hidden" name="ks" value="{{.KeyState}}"><input type="hidden" name="kp" value="{{.KeyPager.Page}}"><input type="hidden" name="aq" value="{{.AuditQuery}}"><input type="hidden" name="ap" value="{{.AuditPager.Page}}"><input class="search" id="device-search" name="dq" value="{{.DeviceQuery}}" type="search" placeholder="搜索设备名称、设备码或 PIN"><select id="device-state" name="ds"><option value="">全部连接状态</option><option value="connected" {{if eq .DeviceState "connected"}}selected{{end}}>连接中</option><option value="online" {{if eq .DeviceState "online"}}selected{{end}}>在线待连接</option><option value="offline" {{if eq .DeviceState "offline"}}selected{{end}}>离线</option></select><select id="device-license" name="dl"><option value="">全部授权状态</option><option value="active" {{if eq .DeviceLicense "active"}}selected{{end}}>授权有效</option><option value="unlicensed" {{if eq .DeviceLicense "unlicensed"}}selected{{end}}>未授权</option><option value="expired" {{if eq .DeviceLicense "expired"}}selected{{end}}>已过期</option><option value="revoked" {{if eq .DeviceLicense "revoked"}}selected{{end}}>已禁用</option></select><button type="submit">查询</button><a class="button secondary" href="{{.DeviceClearURL}}#devices">清空</a><span class="visible-count" id="device-visible">本页 {{len .Devices}} 台</span></form><div class="table-wrap"><table><thead><tr><th>设备名称</th><th>设备码</th><th>PIN</th><th>连接状态</th><th>授权状态</th><th>授权有效期</th><th>最后在线</th><th>管理操作</th></tr></thead><tbody id="device-rows">{{range .Devices}}<tr data-row data-state="{{.ConnectionClass}}" data-license="{{.LicenseClass}}"><td><form class="device-name-form" method="post" action="/admin/name"><input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="_return" value="{{$.ReturnURL}}"><input type="hidden" name="device_id" value="{{.ID}}"><input name="name" value="{{.Name}}" maxlength="80" required aria-label="设备名称"><button class="secondary" type="submit">保存</button></form></td><td><div class="code-cell"><code class="mono">{{if .ShortCode}}{{.ShortCode}}{{else}}{{.ID}}{{end}}</code><button type="button" class="ghost" data-copy="{{if .ShortCode}}{{.ShortCode}}{{else}}{{.ID}}{{end}}">复制</button></div></td><td>{{if .PINAvailable}}<div class="code-cell"><code class="mono">{{.PIN}}</code><button type="button" class="ghost" data-copy="{{.PIN}}">复制</button></div>{{else}}<span class="muted">未上报</span>{{end}}</td><td><span class="badge {{.ConnectionClass}}">{{.ConnectionLabel}}</span></td><td><span class="badge {{.LicenseClass}}">{{.Status}}</span></td><td>{{.ActiveUntil}}</td><td>{{.LastSeen}}</td><td><div class="actions">{{if .Permanent}}<span class="badge active">永久</span>{{else}}<form method="post" action="/admin/grant"><input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="_return" value="{{$.ReturnURL}}"><input type="hidden" name="device_id" value="{{.ID}}"><input type="hidden" name="days" value="30"><input type="hidden" name="hours" value="0"><button type="submit">授权30天</button></form>{{end}}<form method="post" action="/admin/disconnect" data-confirm="确定强制断开并退出设备 {{.Name}} 吗？"><input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="_return" value="{{$.ReturnURL}}"><input type="hidden" name="device_id" value="{{.ID}}"><button class="secondary" type="submit">断开并退出</button></form>{{if eq .LicenseClass "revoked"}}<form method="post" action="/admin/unrevoke"><input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="_return" value="{{$.ReturnURL}}"><input type="hidden" name="device_id" value="{{.ID}}"><button type="submit">解禁</button></form>{{else}}<form method="post" action="/admin/revoke" data-confirm="确定禁用 {{.Name}} 吗？设备会退出，并且下次无法连接。"><input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="_return" value="{{$.ReturnURL}}"><input type="hidden" name="device_id" value="{{.ID}}"><button class="warning" type="submit">禁用</button></form>{{end}}<form method="post" action="/admin/delete" data-confirm="确定永久删除 {{.Name}} 吗？删除后需要重新登记和授权。"><input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="_return" value="{{$.ReturnURL}}"><input type="hidden" name="device_id" value="{{.ID}}"><button class="danger" type="submit">删除</button></form></div></td></tr>{{else}}<tr><td colspan="8" class="empty">没有符合查询条件的设备。请调整条件，或先运行一次被控端。</td></tr>{{end}}</tbody></table><div class="no-results" id="device-empty">当前页没有符合即时筛选条件的设备</div></div>{{with .DevicePager}}<div class="pagination"><span class="pagination-info">第 {{.Start}}–{{.End}} 台，共 {{.Total}} 台 · 第 {{.Page}} / {{.TotalPages}} 页</span>{{if .HasPrev}}<a class="page-link" href="{{.PrevURL}}#devices">上一页</a>{{else}}<span class="page-link disabled">上一页</span>{{end}}{{range .Links}}<a class="page-link {{if .Current}}current{{end}}" href="{{.URL}}#devices">{{.Number}}</a>{{end}}{{if .HasNext}}<a class="page-link" href="{{.NextURL}}#devices">下一页</a>{{else}}<span class="page-link disabled">下一页</span>{{end}}</div>{{end}}<div class="panel-body footnote">“断开并退出”用于立即停止当前进程；“禁用”还会阻止设备下次连接；“解禁”恢复设备资格；“删除”会移除服务器记录。</div></section>
<section class="panel" id="licenses"><div class="panel-head"><div><h2>授权码管理</h2><p>每页 20 个，可查询最近 5000 个记录；完整密钥仅在刚生成时显示。</p></div><div class="count-pills"><span class="count-pill">未使用 {{.AvailableKeyCount}}</span><span class="count-pill">已使用 {{.UsedKeyCount}}</span><span class="count-pill">失效 {{.InvalidKeyCount}}</span></div></div><form class="toolbar" method="get" action="/admin"><input type="hidden" name="dq" value="{{.DeviceQuery}}"><input type="hidden" name="ds" value="{{.DeviceState}}"><input type="hidden" name="dl" value="{{.DeviceLicense}}"><input type="hidden" name="dp" value="{{.DevicePager.Page}}"><input type="hidden" name="aq" value="{{.AuditQuery}}"><input type="hidden" name="ap" value="{{.AuditPager.Page}}"><input class="search" id="key-search" name="kq" value="{{.KeyQuery}}" type="search" placeholder="搜索授权码指纹、状态或设备码"><select id="key-state" name="ks"><option value="">全部状态</option><option value="available" {{if eq .KeyState "available"}}selected{{end}}>未使用</option><option value="used" {{if eq .KeyState "used"}}selected{{end}}>已使用</option><option value="expired" {{if eq .KeyState "expired"}}selected{{end}}>兑换期已过</option><option value="revoked" {{if eq .KeyState "revoked"}}selected{{end}}>已吊销</option></select><button type="submit">查询</button><a class="button secondary" href="{{.KeyClearURL}}#licenses">清空</a><span class="visible-count" id="key-visible">本页 {{len .Keys}} 条</span></form><div class="table-wrap"><table><thead><tr><th>授权码指纹</th><th>授权时长</th><th>当前状态</th><th>兑换截止</th><th>管理操作</th></tr></thead><tbody id="key-rows">{{range .Keys}}<tr data-row data-state="{{.StatusClass}}"><td><div class="code-cell"><code class="mono">{{.Fingerprint}}</code><button type="button" class="ghost" data-copy="{{.Fingerprint}}">复制</button></div></td><td>{{.Duration}}</td><td><span class="badge {{.StatusClass}}">{{.Status}}</span></td><td>{{.RedeemBy}}</td><td>{{if .Revocable}}<form method="post" action="/admin/revoke-key" data-confirm="确定吊销这个未使用的授权码吗？"><input type="hidden" name="_csrf" value="{{$.CSRF}}"><input type="hidden" name="_return" value="{{$.ReturnURL}}"><input type="hidden" name="fingerprint" value="{{.Fingerprint}}"><button class="danger" type="submit">吊销授权码</button></form>{{else}}<span class="muted">不可操作</span>{{end}}</td></tr>{{else}}<tr><td colspan="5" class="empty">没有符合查询条件的授权码</td></tr>{{end}}</tbody></table><div class="no-results" id="key-empty">当前页没有符合即时筛选条件的授权码</div></div>{{with .KeyPager}}<div class="pagination"><span class="pagination-info">第 {{.Start}}–{{.End}} 条，共 {{.Total}} 条 · 第 {{.Page}} / {{.TotalPages}} 页</span>{{if .HasPrev}}<a class="page-link" href="{{.PrevURL}}#licenses">上一页</a>{{else}}<span class="page-link disabled">上一页</span>{{end}}{{range .Links}}<a class="page-link {{if .Current}}current{{end}}" href="{{.URL}}#licenses">{{.Number}}</a>{{end}}{{if .HasNext}}<a class="page-link" href="{{.NextURL}}#licenses">下一页</a>{{else}}<span class="page-link disabled">下一页</span>{{end}}</div>{{end}}</section>
<section class="panel" id="audits"><div class="panel-head"><div><h2>安全审计记录</h2><p>每页 30 条，可查询最近 1000 条设备、授权和连接操作。</p></div></div><form class="toolbar" method="get" action="/admin"><input type="hidden" name="dq" value="{{.DeviceQuery}}"><input type="hidden" name="ds" value="{{.DeviceState}}"><input type="hidden" name="dl" value="{{.DeviceLicense}}"><input type="hidden" name="dp" value="{{.DevicePager.Page}}"><input type="hidden" name="kq" value="{{.KeyQuery}}"><input type="hidden" name="ks" value="{{.KeyState}}"><input type="hidden" name="kp" value="{{.KeyPager.Page}}"><input class="search" id="audit-search" name="aq" value="{{.AuditQuery}}" type="search" placeholder="搜索操作、设备码、来源地址或详情"><button type="submit">查询</button><a class="button secondary" href="{{.AuditClearURL}}#audits">清空</a><span class="visible-count" id="audit-visible">本页 {{len .Audits}} 条</span></form><div class="table-wrap"><table><thead><tr><th>时间</th><th>操作</th><th>设备码</th><th>来源地址</th><th>详情</th></tr></thead><tbody id="audit-rows">{{range .Audits}}<tr data-row><td>{{.At}}</td><td><span class="audit-action" title="{{.Action}}">{{.ActionLabel}}</span></td><td><code class="mono">{{if .DeviceID}}{{.DeviceID}}{{else}}-{{end}}</code></td><td>{{if .RemoteAddr}}{{.RemoteAddr}}{{else}}-{{end}}</td><td class="detail-cell" title="{{.Detail}}">{{if .Detail}}{{.Detail}}{{else}}-{{end}}</td></tr>{{else}}<tr><td colspan="5" class="empty">没有符合查询条件的审计记录</td></tr>{{end}}</tbody></table><div class="no-results" id="audit-empty">当前页没有符合即时搜索条件的记录</div></div>{{with .AuditPager}}<div class="pagination"><span class="pagination-info">第 {{.Start}}–{{.End}} 条，共 {{.Total}} 条 · 第 {{.Page}} / {{.TotalPages}} 页</span>{{if .HasPrev}}<a class="page-link" href="{{.PrevURL}}#audits">上一页</a>{{else}}<span class="page-link disabled">上一页</span>{{end}}{{range .Links}}<a class="page-link {{if .Current}}current{{end}}" href="{{.URL}}#audits">{{.Number}}</a>{{end}}{{if .HasNext}}<a class="page-link" href="{{.NextURL}}#audits">下一页</a>{{else}}<span class="page-link disabled">下一页</span>{{end}}</div>{{end}}</section>
</main><script src="/admin/app.js" defer></script></body></html>`))

const adminScript = `(() => {
  const $ = (selector) => document.querySelector(selector);
  const rows = (selector) => Array.from(document.querySelectorAll(selector + ' tr[data-row]'));
  let dirty = false;
  let refreshing = false;
  let remaining = 30;

  const modalStyle=document.createElement('style');modalStyle.textContent='.admin-dialog{width:360px;max-width:calc(100vw - 32px);padding:24px;border:1px solid #304963;border-radius:14px;background:#101d2d;color:#eaf2ff;box-shadow:0 20px 80px #0008}.admin-dialog::backdrop{background:#02091699;backdrop-filter:blur(3px)}.admin-dialog h2{font-size:18px;margin:0 0 12px}.admin-dialog p{color:#9bb0c9;line-height:1.7;margin:0 0 16px}.admin-dialog textarea{box-sizing:border-box;width:100%;min-height:95px;padding:10px;border:1px solid #304963;background:#081422;color:#b4d6ff;border-radius:8px}.admin-dialog .actions{display:flex;justify-content:flex-end;gap:8px;margin-top:20px}';document.head.append(modalStyle);
  const modal=document.createElement('dialog');modal.className='admin-dialog';
  const heading=document.createElement('h2'),detail=document.createElement('p'),copyArea=document.createElement('textarea'),actions=document.createElement('div'),cancelButton=document.createElement('button'),acceptButton=document.createElement('button');
  copyArea.readOnly=true;actions.className='actions';cancelButton.textContent='取消';cancelButton.className='secondary';acceptButton.textContent='确认';actions.append(cancelButton,acceptButton);modal.append(heading,detail,copyArea,actions);document.body.append(modal);
  function showAdminDialog(message,copyValue){return new Promise(resolve=>{
    if(modal.open){resolve(false);return;}
    heading.textContent=copyValue===undefined?'确认管理操作':'手动复制';detail.textContent=message;copyArea.hidden=copyValue===undefined;copyArea.value=copyValue||'';
    cancelButton.hidden=copyValue!==undefined;acceptButton.textContent=copyValue===undefined?'确认执行':'完成';
    const previousDirty=dirty;dirty=true;
    const finish=value=>{modal.close();dirty=previousDirty;resolve(value);};
    cancelButton.onclick=()=>finish(false);acceptButton.onclick=()=>finish(true);modal.oncancel=e=>{e.preventDefault();finish(false);};
    modal.showModal();if(copyValue===undefined)cancelButton.focus();else{copyArea.focus();copyArea.select();}
  });}

  async function copyText(value, button) {
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(value);
      } else {
        const area = document.createElement('textarea');
        area.value = value;
        area.style.position = 'fixed';
        area.style.opacity = '0';
        document.body.appendChild(area);
        area.select();
        if(!document.execCommand('copy')){area.remove();throw Error('copy failed');}
        area.remove();
      }
      if (button) {
        const old = button.textContent;
        button.textContent = '已复制';
        setTimeout(() => { button.textContent = old; }, 1200);
      }
    } catch (_) {
      await showAdminDialog('自动复制不可用，请选择内容后复制。', value);
    }
  }

  function bindInteractive() {
    document.querySelectorAll('[data-copy]:not([data-copy-bound])').forEach((button) => {
      button.dataset.copyBound = '1';
      button.addEventListener('click', () => copyText(button.dataset.copy, button));
    });
    document.querySelectorAll('form[data-confirm]:not([data-confirm-bound])').forEach((form) => {
      form.dataset.confirmBound = '1';
      form.addEventListener('submit', async (event) => {
        event.preventDefault();
        if(await showAdminDialog(form.dataset.confirm) && form.isConnected) HTMLFormElement.prototype.submit.call(form);
      });
    });
    document.querySelectorAll('form[method="post"] input:not([type=hidden]):not([data-dirty-bound]), form[method="post"] select:not([data-dirty-bound])').forEach((input) => {
      input.dataset.dirtyBound = '1';
      input.addEventListener('input', () => { dirty = true; });
    });
  }
  bindInteractive();

  const copyAll = $('#copy-all-keys');
  if (copyAll) {
    copyAll.addEventListener('click', () => {
      const value = Array.from(document.querySelectorAll('.generated-key code')).map((node) => node.textContent.trim()).join('\n');
      copyText(value, copyAll);
    });
  }

  const filterDevices = () => {
    const deviceRows = rows('#device-rows');
    const query = ($('#device-search')?.value || '').trim().toLowerCase();
    const state = $('#device-state')?.value || '';
    const license = $('#device-license')?.value || '';
    let visible = 0;
    deviceRows.forEach((row) => {
      const show = (!query || row.textContent.toLowerCase().includes(query)) && (!state || row.dataset.state === state) && (!license || row.dataset.license === license);
      row.hidden = !show;
      if (show) visible++;
    });
    if ($('#device-visible')) $('#device-visible').textContent = '显示 ' + visible + ' 台';
    $('#device-empty')?.classList.toggle('visible', deviceRows.length > 0 && visible === 0);
  };
  ['device-search', 'device-state', 'device-license'].forEach((id) => $('#' + id)?.addEventListener('input', filterDevices));

  const filterKeys = () => {
    const keyRows = rows('#key-rows');
    const query = ($('#key-search')?.value || '').trim().toLowerCase();
    const state = $('#key-state')?.value || '';
    let visible = 0;
    keyRows.forEach((row) => {
      const show = (!query || row.textContent.toLowerCase().includes(query)) && (!state || row.dataset.state === state);
      row.hidden = !show;
      if (show) visible++;
    });
    if ($('#key-visible')) $('#key-visible').textContent = '显示 ' + visible + ' 条';
    $('#key-empty')?.classList.toggle('visible', keyRows.length > 0 && visible === 0);
  };
  ['key-search', 'key-state'].forEach((id) => $('#' + id)?.addEventListener('input', filterKeys));

  const filterAudits = () => {
    const auditRows = rows('#audit-rows');
    const query = ($('#audit-search')?.value || '').trim().toLowerCase();
    let visible = 0;
    auditRows.forEach((row) => {
      const show = !query || row.textContent.toLowerCase().includes(query);
      row.hidden = !show;
      if (show) visible++;
    });
    if ($('#audit-visible')) $('#audit-visible').textContent = '显示 ' + visible + ' 条';
    $('#audit-empty')?.classList.toggle('visible', auditRows.length > 0 && visible === 0);
  };
  $('#audit-search')?.addEventListener('input', filterAudits);

  function replaceOptionalPagination(sectionID, nextDocument) {
    const section = document.querySelector(sectionID);
    const current = section?.querySelector('.pagination');
    const next = nextDocument.querySelector(sectionID + ' .pagination');
    if (current && next) current.replaceWith(next.cloneNode(true));
    else if (current && !next) current.remove();
    else if (!current && next && section) {
      const footnote = section.querySelector('.footnote');
      section.insertBefore(next.cloneNode(true), footnote || null);
    }
  }

  async function asyncRefresh() {
    if (refreshing || dirty) return;
    const focused = document.activeElement && /^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName);
    if (focused) return;
    refreshing = true;
    const status = $('#refresh-state');
    if (status) status.textContent = '正在异步刷新…';
    const scroll = {x: window.scrollX, y: window.scrollY};
    const tableScroll = {};
    ['#devices', '#licenses', '#audits'].forEach((selector) => {
      const table = document.querySelector(selector + ' .table-wrap');
      if (table) tableScroll[selector] = {left: table.scrollLeft, top: table.scrollTop};
    });
    try {
      const target = new URL(document.body.dataset.return || location.href, location.origin);
      target.hash = '';
      const response = await fetch(target, {cache: 'no-store', credentials: 'same-origin', headers: {'X-YuDesk-Refresh': 'async'}});
      if (!response.ok) throw new Error('HTTP ' + response.status);
      const nextDocument = new DOMParser().parseFromString(await response.text(), 'text/html');
      const currentStats = Array.from(document.querySelectorAll('.stats .stat-value'));
      const nextStats = Array.from(nextDocument.querySelectorAll('.stats .stat-value'));
      if (currentStats.length !== nextStats.length || !nextDocument.querySelector('#device-rows')) throw new Error('invalid response');
      currentStats.forEach((node, index) => { node.textContent = nextStats[index].textContent; });
      const currentUpdated = document.querySelector('.updated strong');
      const nextUpdated = nextDocument.querySelector('.updated strong');
      if (currentUpdated && nextUpdated) currentUpdated.textContent = nextUpdated.textContent;
      for (const selector of ['#devices .count-pills', '#licenses .count-pills', '#device-rows', '#key-rows', '#audit-rows']) {
        const current = document.querySelector(selector);
        const next = nextDocument.querySelector(selector);
        if (current && next) current.replaceChildren(...Array.from(next.childNodes).map((node) => node.cloneNode(true)));
      }
      replaceOptionalPagination('#devices', nextDocument);
      replaceOptionalPagination('#licenses', nextDocument);
      replaceOptionalPagination('#audits', nextDocument);
      bindInteractive();
      filterDevices();
      filterKeys();
      filterAudits();
      for (const [selector, position] of Object.entries(tableScroll)) {
        const table = document.querySelector(selector + ' .table-wrap');
        if (table) { table.scrollLeft = position.left; table.scrollTop = position.top; }
      }
      window.scrollTo(scroll.x, scroll.y);
      remaining = 30;
      if (status) status.textContent = '数据已更新 · 30 秒后异步刷新';
    } catch (_) {
      remaining = 30;
      if (status) status.textContent = '异步刷新失败 · 30 秒后重试';
    } finally {
      refreshing = false;
    }
  }

  if (document.body.dataset.generated !== '1') {
    setInterval(() => {
      const focused = document.activeElement && /^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName);
      if (!document.hidden && !dirty && !focused) remaining--;
      const status = $('#refresh-state');
      if (status && !refreshing) status.textContent = dirty || focused ? '编辑时已暂停异步刷新' : remaining + ' 秒后异步刷新';
      if (remaining <= 0) asyncRefresh();
    }, 1000);
    $('#refresh-now')?.addEventListener('click', (event) => {
      event.preventDefault();
      if (!dirty) { remaining = 0; asyncRefresh(); }
    });
  } else if ($('#refresh-state')) {
    $('#refresh-state').textContent = '复制授权码后手动刷新';
  }
})();`

var guidePage = template.Must(template.New("guide").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>YuDesk 使用教程</title>
<style>*{box-sizing:border-box}body{margin:0;background:#07111f;color:#eaf2ff;font:15px/1.7 system-ui;letter-spacing:.1px}main{max-width:900px;margin:auto;padding:42px 22px 80px}a{color:#67a7ff;text-decoration:none}h1{font-size:38px;margin:12px 0}h2{margin-top:8px;color:#91bdff}.lead,.note{color:#9db0c8}.step{background:#101d2d;border:1px solid #243953;border-radius:14px;padding:20px 24px;margin:15px 0}pre{overflow:auto;background:#07111f;border:1px solid #20344c;border-radius:9px;padding:14px;color:#cbe1ff}code{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}.warn{border-left:3px solid #e3a839;padding-left:12px;color:#eacb91}.ok{color:#78dba9}</style></head>
<body><main><a href="/">← 返回下载主页</a><h1>YuDesk 使用教程</h1>{{if .DeviceMode}}<p class="lead">无需注册账号、无需输入命令，下载后双击运行即可。</p>
<section class="step"><h2>1. 下载统一版</h2><p>每个平台只需要一个 <strong>YuDesk</strong>。控制电脑与被控电脑都下载对应系统的同一个程序，双击运行，无需账号。</p></section>
<section class="step"><h2>2. 查看本机信息</h2><p>首页显示服务器分配的 <strong>9 位数字设备码</strong>与 <strong>6 位 PIN</strong>，可一键复制。保留“允许连接本机”开关开启。授权码输入默认收起，点击“输入授权码”后展开填写。</p></section>
<section class="step"><h2>3. 发放授权</h2><p>管理员访问官网的“授权管理”，可以按天或小时生成授权码，也可以根据设备码直接授权或续期。普通用户无需登录账号。</p></section>
<section class="step"><h2>4. 连接与设备列表</h2><p>在“连接远程设备”输入对方设备码和六位 PIN，选择控制或仅观看，可选声音。设备列表显示本机和已保存设备，可添加、查询、移除及再次连接，状态每 30 秒异步更新。</p><p class="ok">“结束控制”返回同一个首页，本机仍可接收连接。输入与画面使用端到端加密；短设备码通过已验证的 TLS 中转解析。</p></section>
<section class="step"><h2>5. 授权与退出</h2><p>文件传输在“文件传输”页面开启本机接收目录权限。macOS 需授予屏幕录制与辅助功能权限；Linux 需桌面与输入工具。直接关闭窗口或点“退出”会停止整个程序；点“隐藏”后可关闭窗口并继续在线，重复双击重新显示。管理员断开、禁用、删除或授权到期会停止整个程序。</p></section>
<p class="warn">官网使用普通 HTTP；远程桌面内容仍使用端到端加密。授权码通过 HTTP 提交时可能被同一网络中的攻击者截获，请仅在可信网络使用。</p>
{{else}}<p class="lead">服务器地址和证书指纹已经填好；只需替换用户名、激活密钥、DEVICE_ID 和 PIN。</p>
<section class="step"><h2>1. 下载对应平台文件</h2><p>从下载主页获取账号工具、受控端 Agent 和控制端 Viewer。Linux/macOS 下载后先执行：</p><pre><code>chmod +x yudesk-account yudesk-agent yudesk-viewer</code></pre><p>Windows 直接使用对应的 .exe 文件。</p></section>
<section class="step"><h2>2. 注册、登录并激活</h2><pre><code>yudesk-account health   -server {{.AccountServer}} -server-fingerprint {{.Fingerprint}}
yudesk-account register -server {{.AccountServer}} -server-fingerprint {{.Fingerprint}} -username alice
yudesk-account login    -server {{.AccountServer}} -server-fingerprint {{.Fingerprint}} -username alice
yudesk-account activate -server {{.AccountServer}} -server-fingerprint {{.Fingerprint}} -key YU-XXXX-XXXX-...
yudesk-account status   -server {{.AccountServer}} -server-fingerprint {{.Fingerprint}}</code></pre><p class="note">密码会通过隐藏输入读取。受控端和控制端需要登录同一个账号。</p></section>
<section class="step"><h2>3. 在被控电脑启动 Agent</h2><pre><code>yudesk-agent -relay {{.RelayServer}} -relay-fingerprint {{.Fingerprint}}</code></pre><p>记下窗口中显示的 Device ID 和 8 位 Pairing PIN。需要文件传输时增加 <code>-share-dir 路径</code>。</p></section>
<section class="step"><h2>4. 在控制电脑连接</h2><pre><code>yudesk-viewer -relay {{.RelayServer}} -relay-fingerprint {{.Fingerprint}} -device-id DEVICE_ID -pin 12345678</code></pre><p class="ok">连接成功后会自动打开可视化远程桌面，支持鼠标、键盘、滚轮、组合键、剪贴板和授权目录文件传输。</p></section>
<section class="step"><h2>5. 平台权限</h2><p><strong>Windows：</strong>在已登录用户会话中运行。<br><strong>Linux：</strong>安装 grim/import/maim/gnome-screenshot 之一和 xdotool；X11/XWayland 控制最完整。<br><strong>macOS：</strong>安装 cliclick，并在“系统设置 → 隐私与安全性”中授予屏幕录制和辅助功能权限。</p></section>
<p class="warn">不要使用 -relay-insecure 部署公网服务。授权到期或设备被账号吊销时，服务器会断开远程会话。</p>{{end}}</main></body></html>`))
