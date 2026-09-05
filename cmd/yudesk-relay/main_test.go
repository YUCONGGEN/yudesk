package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	clientrelay "github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestBrokerPairsOwnedDevice(t *testing.T) {
	store, err := account.Open(t.TempDir() + "/relay.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Register("alice", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	key, err := store.GenerateActivationKey(24*time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RedeemActivationKey("alice", key); err != nil {
		t.Fatal(err)
	}
	token, err := store.Login("alice", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := secureconn.DeviceID(publicKey)
	broker := &broker{devices: map[string]waiting{}, accounts: store}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for i := 0; i < 2; i++ {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go broker.handle(conn)
		}
	}()
	type result struct {
		conn net.Conn
		err  error
	}
	agentReady := make(chan result, 1)
	go func() {
		conn, dialErr := clientrelay.Dial(listener.Addr().String(), false, clientrelay.Hello{Role: "agent", ID: deviceID, Auth: token, PublicKey: publicKey})
		agentReady <- result{conn: conn, err: dialErr}
	}()
	time.Sleep(50 * time.Millisecond)
	viewer, err := clientrelay.Dial(listener.Addr().String(), false, clientrelay.Hello{Role: "viewer", ID: deviceID, Auth: token})
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	agent := <-agentReady
	if agent.err != nil {
		t.Fatal(agent.err)
	}
	defer agent.conn.Close()
	want := []byte("encrypted-session-will-use-this-pipe")
	writeDone := make(chan error, 1)
	go func() { _, err := viewer.Write(want); writeDone <- err }()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(agent.conn, got); err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("proxy mismatch: %q", got)
	}
	broker.Lock()
	_, active := broker.active[deviceID]
	broker.Unlock()
	if !active {
		t.Fatal("paired device was not marked active")
	}
	broker.disconnectDevice("alice", deviceID)
	_ = viewer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := viewer.Read(make([]byte, 1)); err == nil {
		t.Fatal("revoked active device connection remained open")
	}
}

func TestBrokerPairsLicensedDeviceWithoutAccount(t *testing.T) {
	store, err := account.Open(t.TempDir() + "/relay.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := secureconn.DeviceID(publicKey)
	if err := store.RegisterLicensedDevice(deviceID, publicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantDeviceLicense(deviceID, time.Hour); err != nil {
		t.Fatal(err)
	}
	broker := &broker{devices: map[string]waiting{}, active: map[string]activeSession{}, accounts: store, deviceLicenses: true}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for i := 0; i < 2; i++ {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go broker.handle(conn)
		}
	}()
	type result struct {
		conn net.Conn
		err  error
	}
	agentReady := make(chan result, 1)
	go func() {
		conn, dialErr := clientrelay.Dial(listener.Addr().String(), false, clientrelay.Hello{Role: "agent", ID: deviceID, PublicKey: publicKey})
		agentReady <- result{conn: conn, err: dialErr}
	}()
	time.Sleep(50 * time.Millisecond)
	viewer, err := clientrelay.Dial(listener.Addr().String(), false, clientrelay.Hello{Role: "viewer", ID: deviceID})
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	agent := <-agentReady
	if agent.err != nil {
		t.Fatal(agent.err)
	}
	defer agent.conn.Close()
	want := []byte("standalone-device-session")
	go func() { _, _ = viewer.Write(want) }()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(agent.conn, got); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("standalone proxy mismatch: got=%q err=%v", got, err)
	}
}

func TestTerminateDeviceSignalsWaitingAgentAndStopsReconnect(t *testing.T) {
	const deviceID = "ABCDEF0123456789ABCDEF01"
	agentServer, agentClient := net.Pipe()
	defer agentClient.Close()
	ready := make(chan net.Conn, 1)
	broker := &broker{
		devices:      map[string]waiting{deviceID: {conn: agentServer, role: "agent", owner: "device:" + deviceID, ready: ready}},
		active:       map[string]activeSession{},
		stopRequests: map[string]string{},
	}
	lineReady := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(agentClient).ReadString('\n')
		lineReady <- strings.TrimSpace(line)
	}()
	if !broker.terminateDevice("device:"+deviceID, deviceID, "administrator disconnected this device") {
		t.Fatal("waiting agent was not found")
	}
	select {
	case line := <-lineReady:
		if line != "ERR STOP administrator disconnected this device" {
			t.Fatalf("unexpected stop signal: %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting agent did not receive stop signal")
	}
	if peer := <-ready; peer != nil {
		t.Fatal("waiting handler was not released")
	}
	if _, pending := broker.takeStopRequest(deviceID); pending {
		t.Fatal("successfully delivered stop signal was left pending")
	}
}

func TestTerminateActiveDeviceLeavesOneShotStopForReconnect(t *testing.T) {
	const deviceID = "ABCDEF0123456789ABCDEF01"
	agentServer, agentClient := net.Pipe()
	viewerServer, viewerClient := net.Pipe()
	defer agentClient.Close()
	defer viewerClient.Close()
	broker := &broker{
		devices:      map[string]waiting{},
		active:       map[string]activeSession{deviceID: {agent: agentServer, viewer: viewerServer, owner: "device:" + deviceID}},
		stopRequests: map[string]string{},
	}
	if !broker.terminateDevice("device:"+deviceID, deviceID, "administrator disconnected this device") {
		t.Fatal("active agent was not found")
	}
	_ = agentClient.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := agentClient.Read(make([]byte, 1)); err == nil {
		t.Fatal("active agent connection remained open")
	}
	reason, pending := broker.takeStopRequest(deviceID)
	if !pending || reason != "administrator disconnected this device" {
		t.Fatalf("missing one-shot reconnect stop: pending=%v reason=%q", pending, reason)
	}
	if _, pending := broker.takeStopRequest(deviceID); pending {
		t.Fatal("one-shot stop request was consumed more than once")
	}
}

func TestTerminateOfflineUnlicensedDeviceLeavesOneShotStop(t *testing.T) {
	const deviceID = "ABCDEF0123456789ABCDEF01"
	broker := &broker{
		devices:      map[string]waiting{},
		active:       map[string]activeSession{},
		controls:     map[string]*deviceControl{},
		stopRequests: map[string]string{},
	}
	if !broker.terminateDevice("device:"+deviceID, deviceID, "administrator disconnected this device") {
		t.Fatal("offline device stop was not queued")
	}
	reason, pending := broker.takeStopRequest(deviceID)
	if !pending || reason != "administrator disconnected this device" {
		t.Fatalf("missing offline reconnect stop: pending=%v reason=%q", pending, reason)
	}
	if _, pending := broker.takeStopRequest(deviceID); pending {
		t.Fatal("offline reconnect stop was not one-shot")
	}
}

func TestDownloadHomeAndAllowList(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "windows-amd64")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	want := []byte("viewer-release")
	if err := os.WriteFile(filepath.Join(directory, "yudesk-viewer.exe"), want, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SHA256SUMS.txt"), []byte("abc  viewer\n"), 0644); err != nil {
		t.Fatal(err)
	}
	home := httptest.NewRecorder()
	serveDownloadHome(home, root)
	if home.Code != http.StatusOK || !strings.Contains(home.Body.String(), "/download/windows-amd64/yudesk-viewer.exe?v=") || strings.Contains(home.Body.String(), "yudesk-account") || strings.Contains(home.Body.String(), "yudesk-update") || !strings.Contains(home.Body.String(), "macOS Apple Silicon") || !strings.Contains(home.Body.String(), "SHA-256 校验和") || !strings.Contains(home.Body.String(), `href="/guide"`) {
		t.Fatalf("unexpected download home: status=%d body=%s", home.Code, home.Body.String())
	}
	if !strings.Contains(home.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("download home is cacheable: %q", home.Header().Get("Cache-Control"))
	}
	guide := httptest.NewRecorder()
	serveGuide(guide, guideConfig{AccountServer: "https://desk.example.com:8234", RelayServer: "desk.example.com:8233", Fingerprint: strings.Repeat("A", 64)})
	if guide.Code != http.StatusOK || !strings.Contains(guide.Body.String(), "YuDesk 使用教程") || !strings.Contains(guide.Body.String(), "server-fingerprint") || !strings.Contains(guide.Body.String(), "desk.example.com:8233") || !strings.Contains(guide.Body.String(), "desk.example.com:8234") || !strings.Contains(guide.Body.String(), "Windows") || !strings.Contains(guide.Body.String(), "Linux") || !strings.Contains(guide.Body.String(), "macOS") {
		t.Fatalf("unexpected guide: status=%d body=%s", guide.Code, guide.Body.String())
	}
	deviceGuide := httptest.NewRecorder()
	serveGuide(deviceGuide, guideConfig{DeviceMode: true})
	if deviceGuide.Code != http.StatusOK || !strings.Contains(deviceGuide.Body.String(), "无需注册账号") || !strings.Contains(deviceGuide.Body.String(), "双击") || strings.Contains(deviceGuide.Body.String(), "yudesk-account login") {
		t.Fatalf("unexpected standalone device guide: status=%d body=%s", deviceGuide.Code, deviceGuide.Body.String())
	}
	download := httptest.NewRecorder()
	serveDownload(download, httptest.NewRequest(http.MethodGet, "/download/windows-amd64/yudesk-viewer.exe", nil), root)
	if download.Code != http.StatusOK || download.Body.String() != string(want) {
		t.Fatalf("unexpected download: status=%d body=%q", download.Code, download.Body.String())
	}
	if !strings.Contains(download.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("download is cacheable: %q", download.Header().Get("Cache-Control"))
	}
	checksums := httptest.NewRecorder()
	serveNamedDownloadFile(checksums, httptest.NewRequest(http.MethodGet, "/SHA256SUMS.txt", nil), root, "SHA256SUMS.txt", "text/plain")
	if checksums.Code != http.StatusOK || checksums.Body.String() != "abc  viewer\n" {
		t.Fatalf("unexpected checksums response: status=%d body=%q", checksums.Code, checksums.Body.String())
	}
	if !strings.Contains(checksums.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("checksums are cacheable: %q", checksums.Header().Get("Cache-Control"))
	}
	for _, path := range []string{"/download/../../yudesk.db", "/download/linux-amd64/yudesk-relay"} {
		denied := httptest.NewRecorder()
		serveDownload(denied, httptest.NewRequest(http.MethodGet, path, nil), root)
		if denied.Code != http.StatusNotFound {
			t.Fatalf("disallowed path %q returned %d", path, denied.Code)
		}
	}
}

func TestPlainHTTPDownloadMuxDoesNotExposeAccountAPI(t *testing.T) {
	root := t.TempDir()
	mux := http.NewServeMux()
	registerDownloadRoutes(mux, root, func(r *http.Request) guideConfig {
		return makeGuideConfig(r, "http", strings.Repeat("A", 64), "desk.example.com:8233", "https://desk.example.com:8234", false)
	})

	health := httptest.NewRecorder()
	mux.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "http://desk.example.com:8235/healthz", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"ok":true`) {
		t.Fatalf("unexpected HTTP download health response: status=%d body=%s", health.Code, health.Body.String())
	}
	guide := httptest.NewRecorder()
	mux.ServeHTTP(guide, httptest.NewRequest(http.MethodGet, "http://desk.example.com:8235/guide", nil))
	if guide.Code != http.StatusOK || !strings.Contains(guide.Body.String(), "https://desk.example.com:8234") || strings.Contains(guide.Body.String(), "http://desk.example.com:8235 -server-fingerprint") {
		t.Fatalf("HTTP guide did not retain the HTTPS account API: status=%d body=%s", guide.Code, guide.Body.String())
	}
	register := httptest.NewRecorder()
	mux.ServeHTTP(register, httptest.NewRequest(http.MethodPost, "http://desk.example.com:8235/api/register", strings.NewReader(`{}`)))
	if register.Code != http.StatusNotFound {
		t.Fatalf("account registration was exposed over HTTP: status=%d", register.Code)
	}
}

func TestStandaloneActivationAndAdminWeb(t *testing.T) {
	store, err := account.Open(t.TempDir() + "/relay.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broker := &broker{devices: map[string]waiting{}, active: map[string]activeSession{}, accounts: store, deviceLicenses: true}
	mux := http.NewServeMux()
	registerStandaloneDeviceRoutes(mux, broker, "correct-admin-password")
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := secureconn.DeviceID(publicKey)
	key, err := store.GenerateActivationKey(time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"id": deviceID, "name": "前台电脑", "key": key, "publicKey": base64.StdEncoding.EncodeToString(publicKey)})
	activation := httptest.NewRecorder()
	mux.ServeHTTP(activation, httptest.NewRequest(http.MethodPost, "/api/device/activate", bytes.NewReader(payload)))
	if activation.Code != http.StatusOK || !store.HasActiveDeviceLicense(deviceID) {
		t.Fatalf("device activation failed: status=%d body=%s", activation.Code, activation.Body.String())
	}
	broker.devices[deviceID] = waiting{role: "agent"}
	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("admin page without password returned %d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin", nil)
	request.SetBasicAuth("admin", "correct-admin-password")
	authorized := httptest.NewRecorder()
	mux.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK || !strings.Contains(authorized.Body.String(), deviceID) || !strings.Contains(authorized.Body.String(), "前台电脑") || !strings.Contains(authorized.Body.String(), `<span class="stat-label">当前在线</span><strong class="stat-value">1</strong>`) || !strings.Contains(authorized.Body.String(), `<span class="stat-label">连接中</span><strong class="stat-value">0</strong>`) || !strings.Contains(authorized.Body.String(), "在线 · 未被连接") || !strings.Contains(authorized.Body.String(), "30 秒后异步刷新") || !strings.Contains(authorized.Body.String(), "搜索设备名称、设备码或 PIN") {
		t.Fatalf("authorized admin page failed: status=%d body=%s", authorized.Code, authorized.Body.String())
	}
	csrfMarker := `name="_csrf" value="`
	csrfStart := strings.Index(authorized.Body.String(), csrfMarker)
	if csrfStart < 0 {
		t.Fatal("admin page did not contain a CSRF token")
	}
	csrfStart += len(csrfMarker)
	csrfEnd := strings.Index(authorized.Body.String()[csrfStart:], `"`)
	if csrfEnd < 0 {
		t.Fatal("admin page contained a malformed CSRF token")
	}
	csrfToken := authorized.Body.String()[csrfStart : csrfStart+csrfEnd]
	scriptRequest := httptest.NewRequest(http.MethodGet, "/admin/app.js", nil)
	scriptRequest.SetBasicAuth("admin", "correct-admin-password")
	scriptResponse := httptest.NewRecorder()
	mux.ServeHTTP(scriptResponse, scriptRequest)
	if scriptResponse.Code != http.StatusOK || !strings.Contains(scriptResponse.Body.String(), "filterDevices") || !strings.Contains(scriptResponse.Body.String(), "window.confirm") || !strings.Contains(scriptResponse.Body.String(), "asyncRefresh") || strings.Contains(scriptResponse.Body.String(), "window.location.replace") {
		t.Fatalf("admin application script failed: status=%d body=%s", scriptResponse.Code, scriptResponse.Body.String())
	}
	delete(broker.devices, deviceID)
	broker.active[deviceID] = activeSession{}
	connectedRequest := httptest.NewRequest(http.MethodGet, "/admin", nil)
	connectedRequest.SetBasicAuth("admin", "correct-admin-password")
	connected := httptest.NewRecorder()
	mux.ServeHTTP(connected, connectedRequest)
	if connected.Code != http.StatusOK || !strings.Contains(connected.Body.String(), `<span class="stat-label">连接中</span><strong class="stat-value">1</strong>`) || !strings.Contains(connected.Body.String(), `<span class="badge connected">连接中</span>`) {
		t.Fatalf("connected admin status failed: status=%d body=%s", connected.Code, connected.Body.String())
	}
	delete(broker.active, deviceID)
	missingCSRF := httptest.NewRequest(http.MethodPost, "/admin/disconnect", strings.NewReader("device_id="+deviceID))
	missingCSRF.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	missingCSRF.SetBasicAuth("admin", "correct-admin-password")
	missingCSRFResponse := httptest.NewRecorder()
	mux.ServeHTTP(missingCSRFResponse, missingCSRF)
	if missingCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("admin operation without CSRF token returned %d", missingCSRFResponse.Code)
	}
	postAdmin := func(path, form string) *httptest.ResponseRecorder {
		t.Helper()
		form += "&_csrf=" + csrfToken
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.SetBasicAuth("admin", "correct-admin-password")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		expectedStatus := http.StatusSeeOther
		if response.Code != expectedStatus {
			t.Fatalf("admin operation %s failed: status=%d body=%s", path, response.Code, response.Body.String())
		}
		if !strings.HasPrefix(response.Header().Get("Location"), "/admin") {
			t.Fatalf("admin operation %s did not redirect to dashboard: %q", path, response.Header().Get("Location"))
		}
		return response
	}
	directAction := httptest.NewRequest(http.MethodGet, "/admin/disconnect", nil)
	directAction.SetBasicAuth("admin", "correct-admin-password")
	directResponse := httptest.NewRecorder()
	mux.ServeHTTP(directResponse, directAction)
	if directResponse.Code != http.StatusSeeOther || directResponse.Header().Get("Location") != "/admin" {
		t.Fatalf("direct admin action URL did not recover to dashboard: status=%d location=%q", directResponse.Code, directResponse.Header().Get("Location"))
	}
	postAdmin("/admin/name", "device_id="+deviceID+"&name=%E4%BC%9A%E8%AE%AE%E5%AE%A4")
	devices, err := store.ListLicensedDevices(10)
	if err != nil || len(devices) != 1 || devices[0].Name != "会议室" {
		t.Fatalf("admin rename was not applied: %+v err=%v", devices, err)
	}
	generateResponse := postAdmin("/admin/generate", "days=1&hours=0&count=2")
	resultRequest := httptest.NewRequest(http.MethodGet, generateResponse.Header().Get("Location"), nil)
	resultRequest.SetBasicAuth("admin", "correct-admin-password")
	resultResponse := httptest.NewRecorder()
	mux.ServeHTTP(resultResponse, resultRequest)
	if resultResponse.Code != http.StatusOK || strings.Count(resultResponse.Body.String(), `<div class="generated-key">`) != 2 || !strings.Contains(resultResponse.Body.String(), "复制全部") {
		t.Fatalf("admin did not return two generated keys after redirect: status=%d body=%s", resultResponse.Code, resultResponse.Body.String())
	}
	postAdmin("/admin/revoke", "device_id="+deviceID)
	if store.HasActiveDeviceLicense(deviceID) {
		t.Fatal("admin revoke left the device active")
	}
	postAdmin("/admin/unrevoke", "device_id="+deviceID)
	if !store.HasActiveDeviceLicense(deviceID) {
		t.Fatal("admin unban did not restore the device")
	}
	postAdmin("/admin/revoke", "device_id="+deviceID)
	postAdmin("/admin/grant", "device_id="+deviceID+"&days=0&hours=2")
	if !store.HasActiveDeviceLicense(deviceID) {
		t.Fatal("admin grant did not restore the device")
	}
	postAdmin("/admin/disconnect", "device_id="+deviceID)
	postAdmin("/admin/settings/auto-activate", "enabled=1")
	if enabled, err := store.DeviceAutoActivation(); err != nil || !enabled {
		t.Fatalf("admin did not enable automatic activation: enabled=%v err=%v", enabled, err)
	}
	postAdmin("/admin/settings/auto-activate", "enabled=0")
	postAdmin("/admin/delete", "device_id="+deviceID)
	devices, err = store.ListLicensedDevices(10)
	if err != nil || len(devices) != 0 {
		t.Fatalf("admin delete did not remove the device: %+v err=%v", devices, err)
	}
	audits, err := store.RecentAuditAll(20)
	if err != nil || len(audits) < 5 {
		t.Fatalf("admin operations were not audited: count=%d err=%v", len(audits), err)
	}
}

func TestAdminPaginationPreservesQuery(t *testing.T) {
	items := make([]int, 55)
	query := url.Values{"dq": {"meeting room"}, "ks": {"available"}}
	page, pager := paginateAdmin(items, 2, 20, "dp", query)
	if len(page) != 20 || pager.Page != 2 || pager.TotalPages != 3 || pager.Start != 21 || pager.End != 40 || !pager.HasPrev || !pager.HasNext {
		t.Fatalf("unexpected pager: pageItems=%d pager=%+v", len(page), pager)
	}
	if !strings.Contains(pager.PrevURL, "dq=meeting+room") || !strings.Contains(pager.NextURL, "ks=available") || !strings.Contains(pager.NextURL, "dp=3") {
		t.Fatalf("pager did not preserve query: prev=%q next=%q", pager.PrevURL, pager.NextURL)
	}
	last, lastPager := paginateAdmin(items, 99, 20, "dp", query)
	if len(last) != 15 || lastPager.Page != 3 || lastPager.HasNext {
		t.Fatalf("out-of-range page was not clamped: pageItems=%d pager=%+v", len(last), lastPager)
	}
}
