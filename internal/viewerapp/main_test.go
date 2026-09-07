package viewerapp

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/stream"
)

func TestViewerLauncherExitEndpoint(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	directory, token, err := loadViewerState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	type launcherResult struct {
		request viewerConnectRequest
		connect bool
		err     error
	}
	done := make(chan launcherResult, 1)
	go func() {
		request, connect, err := runViewerLauncher(addr, false, directory, token)
		done <- launcherResult{request: request, connect: connect, err: err}
	}()
	target := viewerUIURL(addr, token)
	deadline := time.Now().Add(2 * time.Second)
	for !viewerUIAvailable(target) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !viewerUIAvailable(target) {
		t.Fatal("viewer launcher did not start")
	}
	exitURL := strings.Replace(target, "/?", "/exit?", 1)
	response, err := http.Post(exitURL, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("viewer exit endpoint returned %d", response.StatusCode)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.connect {
			t.Fatal("exit unexpectedly requested a connection")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("viewer launcher did not exit")
	}
	if _, err := os.Stat(viewerLauncherPath(directory)); !os.IsNotExist(err) {
		t.Fatalf("viewer launcher file was not removed: %v", err)
	}
}

func TestViewerLauncherDoesNotCreateSecondInstanceWhenPortIsBusy(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	blocker := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})}
	go func() { _ = blocker.Serve(occupied) }()
	defer blocker.Close()

	directory, token, err := loadViewerState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, connect, err := runViewerLauncher(occupied.Addr().String(), false, directory, token)
	if err == nil {
		t.Fatal("second viewer launcher unexpectedly started")
	}
	if connect {
		t.Fatal("busy viewer launcher unexpectedly requested a connection")
	}
}

func TestViewerLauncherReturnsConnectionInSameProcess(t *testing.T) {
	status := onlineTestServer(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	directory, token, err := loadViewerState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	type launcherResult struct {
		request viewerConnectRequest
		connect bool
		err     error
	}
	done := make(chan launcherResult, 1)
	go func() {
		request, connect, err := runViewerLauncherWithMessage(addr, false, directory, token, "", status.URL)
		done <- launcherResult{request: request, connect: connect, err: err}
	}()
	target := viewerUIURL(addr, token)
	deadline := time.Now().Add(2 * time.Second)
	for !viewerUIAvailable(target) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	connectURL := strings.Replace(target, "/?", "/connect?", 1)
	response, err := http.Post(connectURL, "application/x-www-form-urlencoded", strings.NewReader("device_id=ABCDEF0123456789ABCDEF01&pin=12345678"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if !result.connect || result.request.deviceID != "ABCDEF0123456789ABCDEF01" || result.request.pin != "12345678" || !result.request.control || result.request.audio {
			t.Fatalf("unexpected connection request: %+v connect=%v", result.request, result.connect)
		}
		if result.request.webAddr != addr {
			t.Fatalf("launcher did not preserve its local address: got %q want %q", result.request.webAddr, addr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("viewer launcher did not hand off to the same process")
	}
}

func TestLocalAccessToken(t *testing.T) {
	token, err := newAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 30 {
		t.Fatalf("access token is too short: %d", len(token))
	}
	handler := requireAccessToken(token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/api/info", nil))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("request without token returned %d", denied.Code)
	}
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, httptest.NewRequest(http.MethodGet, "/api/info?access_token="+token, nil))
	if allowed.Code != http.StatusNoContent {
		t.Fatalf("request with token returned %d", allowed.Code)
	}
}

func TestViewerStateReopensCurrentSession(t *testing.T) {
	directory, token, err := loadViewerState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, secondToken, err := loadViewerState(directory)
	if err != nil || secondToken != token {
		t.Fatalf("viewer token was not stable: first=%q second=%q err=%v", token, secondToken, err)
	}
	server := httptest.NewServer(requireAccessToken(token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer server.Close()
	target := server.URL + "/?access_token=" + token
	if err := writeViewerSession(directory, target); err != nil {
		t.Fatal(err)
	}
	if got := activeViewerSession(directory, token); got != target {
		t.Fatalf("active viewer session was not recovered: %q", got)
	}
	clearViewerSession(directory, target)
	if _, err := os.Stat(viewerSessionPath(directory)); !os.IsNotExist(err) {
		t.Fatalf("viewer session file was not removed: %v", err)
	}
	if err := writeViewerLauncher(directory, target); err != nil {
		t.Fatal(err)
	}
	if got := activeViewerLauncher(directory, token); got != target {
		t.Fatalf("active viewer launcher was not recovered: %q", got)
	}
	clearViewerLauncher(directory, target)
	if _, err := os.Stat(viewerLauncherPath(directory)); !os.IsNotExist(err) {
		t.Fatalf("viewer launcher file was not removed: %v", err)
	}
	if got := viewerUIURL(":9348", token); !strings.HasPrefix(got, "http://127.0.0.1:9348/") {
		t.Fatalf("wildcard viewer address was not converted to loopback: %s", got)
	}
}

func TestViewerLauncherOffersReopenAndExitControls(t *testing.T) {
	var output bytes.Buffer
	if err := viewerLauncherPage.Execute(&output, map[string]any{"Token": "test-token", "History": []connectionRecord{{DeviceID: "ABCDEF0123456789ABCDEF01", Name: "会议室"}}}); err != nil {
		t.Fatal(err)
	}
	page := output.String()
	for _, expected := range []string{"退出控制端", "关闭本窗口会自动结束控制端进程", "仅观看", "听取被控端声音", "/api/ui/watch", "/api/device/status", "/exit?access_token=test-token", "自动检测在线状态", `data-device-id="ABCDEF0123456789ABCDEF01"`, "在线 · 可以连接", "连接中 · 暂不可连接"} {
		if !strings.Contains(page, expected) {
			t.Errorf("viewer launcher does not contain %q", expected)
		}
	}
}

func TestViewerDeviceStatusProxy(t *testing.T) {
	const deviceID = "ABCDEF0123456789ABCDEF01"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/device/status" || r.URL.Query().Get("ids") != deviceID {
			t.Fatalf("unexpected status request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"devices":[{"id":"` + deviceID + `","online":true,"connected":false,"active":true}]}`))
	}))
	defer remote.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/device/status?ids="+deviceID, nil)
	serveViewerDeviceStatus(remote.URL).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"online":true`) || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected proxied status: code=%d body=%s", recorder.Code, recorder.Body.String())
	}

	invalid := httptest.NewRecorder()
	serveViewerDeviceStatus(remote.URL).ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/device/status?ids=bad", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid device ID returned %d", invalid.Code)
	}
}

func TestViewerLauncherReturnsViewOnlyAudioSelection(t *testing.T) {
	status := onlineTestServer(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	directory, token, err := loadViewerState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan viewerConnectRequest, 1)
	go func() {
		request, _, _ := runViewerLauncherWithMessage(addr, false, directory, token, "", status.URL)
		done <- request
	}()
	target := viewerUIURL(addr, token)
	deadline := time.Now().Add(2 * time.Second)
	for !viewerUIAvailable(target) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	connectURL := strings.Replace(target, "/?", "/connect?", 1)
	response, err := http.Post(connectURL, "application/x-www-form-urlencoded", strings.NewReader("device_id=ABCDEF0123456789ABCDEF01&pin=12345678&mode=view&audio=1"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	select {
	case request := <-done:
		if request.control || !request.audio {
			t.Fatalf("unexpected connection capabilities: %+v", request)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("viewer launcher did not return the selected capabilities")
	}
}

func TestIndexUsesRequestedStreamDefaults(t *testing.T) {
	recorder := httptest.NewRecorder()
	serveIndex(17, 63, "test-token").ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String() + sessionJS + sessionCSS
	for _, expected := range []string{`value="17"`, `value="63"`, `access_token=test-token`, `requestFullscreen()`, `max-height:100%`, `整屏显示`, `结束控制`, `/api/disconnect?access_token=test-token`, `keyboardCapture`, `/assets/files.js`} {
		if !strings.Contains(body, expected) {
			t.Errorf("index does not contain %q", expected)
		}
	}
	if strings.Contains(body, "退出控制端") || strings.Contains(body, "/api/exit?") {
		t.Fatal("session still exposes process exit button")
	}
}

func TestViewerTransitionPageFollowsLocalMode(t *testing.T) {
	recorder := httptest.NewRecorder()
	serveViewerTransitionPage(recorder, "正在连接远程电脑", "请稍候。", "test-token", "session")
	body := recorder.Body.String()
	for _, expected := range []string{"/api/ui/mode?access_token=", "location.replace(target)", `data-mode="session"`, "test-token"} {
		if !strings.Contains(body, expected) {
			t.Errorf("transition page does not contain %q", expected)
		}
	}
}

func TestStreamDefaultsPrioritizeLatencyAtThirtyFPS(t *testing.T) {
	directory := t.TempDir()
	options := loadStreamOptions(directory, 30, 82)
	if options.FPS != 30 || options.Quality != 82 || options.Mode != "adaptive" || options.MaxWidth != 1280 || options.MaxMbps != 8 || !options.FrameAck {
		t.Fatalf("unexpected source-quality defaults: %+v", options)
	}
	legacy := stream.Options{FPS: 30, Quality: 60, Mode: "adaptive", MaxWidth: 960, MaxMbps: 12, SaveIdle: true, FrameAck: true}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(directory, "stream-options.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	options = loadStreamOptions(directory, 30, 82)
	if options.Quality != 70 || options.Mode != "adaptive" || options.MaxWidth != 1280 {
		t.Fatalf("former factory preset was not migrated: %+v", options)
	}
	chosen := stream.Options{FPS: 30, Quality: 82, Mode: "fixed", MaxWidth: 0, SaveIdle: true}
	if err := saveStreamOptions(directory, chosen); err != nil {
		t.Fatal(err)
	}
	options = loadStreamOptions(directory, 30, 70)
	if options.Mode != "fixed" || options.MaxWidth != 0 || options.Quality != 82 {
		t.Fatal("explicit source-quality preference was overwritten")
	}
}
