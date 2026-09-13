package agentapp

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
	"github.com/yudesk/yudesk/internal/uilifecycle"
)

func TestAgentAccessTokenAllowsExistingUIToBeReopened(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	token := agentAccessToken(privateKey)
	if token == "" || token != agentAccessToken(privateKey) {
		t.Fatal("agent UI token is empty or not stable for the same device identity")
	}
	server := httptest.NewServer(requireAgentToken(token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer server.Close()
	if !agentUIAvailable(server.URL + "/?access_token=" + token) {
		t.Fatal("running agent UI was not detected with the device token")
	}
	if agentUIAvailable(server.URL + "/?access_token=wrong") {
		t.Fatal("agent UI was accepted with the wrong token")
	}
	if got := agentUIURL(":9350", token); !strings.HasPrefix(got, "http://127.0.0.1:9350/") {
		t.Fatalf("wildcard UI address was not converted to loopback: %s", got)
	}
}

func TestAgentUIStateReopensOnlyLocalPage(t *testing.T) {
	directory := t.TempDir()
	token := "local-test-token"
	server := httptest.NewServer(requireAgentToken(token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer server.Close()
	target := server.URL + "/?access_token=" + token
	if err := writeAgentUIState(directory, target); err != nil {
		t.Fatal(err)
	}
	if got := waitForAgentUI(directory, time.Second); got != target {
		t.Fatalf("active agent UI was not recovered: %q", got)
	}
	clearAgentUIState(directory, target)
	if _, err := os.Stat(agentUIStatePath(directory)); !os.IsNotExist(err) {
		t.Fatalf("agent UI state file was not removed: %v", err)
	}
	if err := writeAgentUIState(directory, "http://example.com:9350/?access_token=x"); err == nil {
		t.Fatal("non-local agent UI URL was accepted")
	}
}

func TestAgentExitClosesRelayConnection(t *testing.T) {
	agentSide, relaySide := net.Pipe()
	defer relaySide.Close()
	a := &agent{quit: make(chan struct{})}
	if !a.setRelayConnection(agentSide) {
		t.Fatal("relay connection was rejected before exit")
	}
	a.requestExit()
	select {
	case <-a.quit:
	default:
		t.Fatal("exit did not close the agent quit signal")
	}
	_ = relaySide.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := relaySide.Read(make([]byte, 1)); err == nil {
		t.Fatal("exit left the relay connection open")
	}
}

func TestAgentExitEndpointStopsTheProcessLoop(t *testing.T) {
	a := &agent{id: "ABCDEF0123456789ABCDEF01", pin: "12345678", name: "test", quit: make(chan struct{})}
	token := "local-test-token"
	uiURL, err := a.startUI("127.0.0.1:0", "http://127.0.0.1:1", token, uilifecycle.New(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	exitURL := strings.Replace(uiURL, "/?", "/exit?", 1)
	response, err := http.Post(exitURL, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("exit endpoint returned %d", response.StatusCode)
	}
	select {
	case <-a.quit:
	case <-time.After(2 * time.Second):
		t.Fatal("exit endpoint did not stop the agent")
	}
}

func TestAgentHideEndpointKeepsProcessRunning(t *testing.T) {
	a := &agent{id: "ABCDEF0123456789ABCDEF01", pin: "12345678", name: "test", quit: make(chan struct{})}
	token := "local-test-token"
	tracker := uilifecycle.New(100 * time.Millisecond)
	uiURL, err := a.startUI("127.0.0.1:0", "http://127.0.0.1:1", token, tracker)
	if err != nil {
		t.Fatal(err)
	}
	hideURL := strings.Replace(uiURL, "/?", "/hide?", 1)
	response, err := http.Post(hideURL, "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("hide endpoint returned %d", response.StatusCode)
	}
	select {
	case <-a.quit:
		t.Fatal("hide endpoint stopped the agent")
	case <-time.After(250 * time.Millisecond):
	}
	a.requestExit()
}

func TestActiveLicenseExpiryStopsAgentProcess(t *testing.T) {
	expires := time.Now().Add(500 * time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"active": time.Now().Before(expires), "activeUntil": expires})
	}))
	defer server.Close()
	a := &agent{id: "ABCDEF0123456789ABCDEF01", quit: make(chan struct{})}
	go a.monitorLicense(server.URL)
	select {
	case <-a.quit:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not exit after its active license expired")
	}
}

func TestExpiredLicenseOnFreshLaunchStillAllowsReactivation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"active": false, "activeUntil": time.Now().Add(-time.Hour)})
	}))
	defer server.Close()
	a := &agent{id: "ABCDEF0123456789ABCDEF01", quit: make(chan struct{})}
	go a.monitorLicense(server.URL)
	select {
	case <-a.quit:
		t.Fatal("fresh launch with an expired license exited before reactivation was possible")
	case <-time.After(300 * time.Millisecond):
	}
	a.requestExit()
}

func TestRelayRejectionCodesSeparateTerminationFromActivationWait(t *testing.T) {
	for _, code := range []string{"STOP", "DISABLED", "DELETED", "DENIED", "BUSY"} {
		if got := relay.RejectionCode(&relay.RejectionError{Code: code, Message: "test"}); got != code {
			t.Fatalf("rejection code=%q want=%q", got, code)
		}
	}
	if got := relay.RejectionCode(errors.New("temporary network failure")); got != "" {
		t.Fatalf("network error was classified as rejection: %q", got)
	}
	for _, code := range []string{"STOP", "DISABLED", "DELETED", "DENIED", "EXPIRED"} {
		if !terminalRelayRejection(code) {
			t.Fatalf("%q must terminate a managed device", code)
		}
	}
	for _, code := range []string{"", "BUSY", "LICENSE_REQUIRED"} {
		if terminalRelayRejection(code) {
			t.Fatalf("%q must be retried without terminating the process", code)
		}
	}
}

func TestAgentPageOffersOneClickCopy(t *testing.T) {
	var output bytes.Buffer
	data := map[string]any{
		"ID":          "ABCDEF0123456789ABCDEF01",
		"PIN":         "12345678",
		"Name":        "测试电脑",
		"Status":      "等待连接",
		"ActiveUntil": "2026-12-31 12:00:00",
		"Token":       "test-token",
	}
	if err := agentPage.Execute(&output, data); err != nil {
		t.Fatal(err)
	}
	page := output.String()
	for _, expected := range []string{"复制设备码", "复制 PIN", "一键复制设备码和 PIN", "隐藏到后台", "退出被控端", "直接关闭窗口", "/api/ui/watch", `data-device-id="ABCDEF0123456789ABCDEF01"`, `data-pin="12345678"`, "navigator.clipboard", "设备码：", `id="legacy-exit-dialog"`, "确认退出 YuDesk", "showModal"} {
		if !strings.Contains(page, expected) {
			t.Errorf("agent page does not contain %q", expected)
		}
	}
	if strings.Contains(page, "confirm(") || strings.Contains(page, "alert(") || strings.Contains(page, "prompt(") {
		t.Fatal("agent page must not use browser-native dialogs")
	}
}

func TestViewOnlySessionRejectsRemoteInput(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{id: secureconn.DeviceID(publicKey), pin: "12345678", allowControl: true, privateKey: privateKey}
	agentRaw, viewerRaw := net.Pipe()
	go a.handle(agentRaw)
	secured, err := secureconn.Connect(viewerRaw, a.id)
	if err != nil {
		t.Fatal(err)
	}
	defer secured.Close()
	connection := protocol.NewConn(secured)
	if err := connection.WriteMessage(protocol.Message{Kind: "request", ID: "1", Method: "auth", Params: []byte(`{"pin":"12345678","mode":"view"}`)}); err != nil {
		t.Fatal(err)
	}
	auth, err := connection.ReadMessage()
	if err != nil || !auth.OK || auth.Meta["control"] != false {
		t.Fatalf("view-only authentication failed: response=%+v err=%v", auth, err)
	}
	if err := connection.WriteMessage(protocol.Message{Kind: "request", ID: "2", Method: "input", Params: []byte(`{"events":[{"type":"move","x":1,"y":1}]}`)}); err != nil {
		t.Fatal(err)
	}
	response, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || !strings.Contains(response.Error, "view-only") {
		t.Fatalf("view-only session accepted input: %+v", response)
	}
}

func TestPINIsCheckedInsideEncryptedChannelAndRateLimited(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{id: secureconn.DeviceID(publicKey), pin: "12345678", privateKey: privateKey}
	attempt := func(pin string) protocol.Message {
		agentRaw, viewerRaw := net.Pipe()
		go a.handle(agentRaw)
		secured, err := secureconn.Connect(viewerRaw, a.id)
		if err != nil {
			t.Fatal(err)
		}
		defer secured.Close()
		connection := protocol.NewConn(secured)
		if err := connection.WriteMessage(protocol.Message{Kind: "request", ID: "1", Method: "auth", Params: []byte(`{"pin":"` + pin + `"}`)}); err != nil {
			t.Fatal(err)
		}
		response, err := connection.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	if response := attempt("wrong-pin"); response.OK || response.Error != "invalid PIN" {
		t.Fatalf("wrong PIN response: %+v", response)
	}
	if response := attempt("12345678"); !response.OK {
		t.Fatalf("correct PIN rejected: %+v", response)
	}
	d := &Device{a: a, identityDir: t.TempDir()}
	newPIN, err := d.RotatePIN()
	if err != nil {
		t.Fatal(err)
	}
	if response := attempt("12345678"); response.OK {
		t.Fatal("rotated old PIN still authenticated")
	}
	if response := attempt(newPIN); !response.OK {
		t.Fatal("new PIN rejected")
	}
	for i := 0; i < 5; i++ {
		_ = attempt("wrong-pin")
	}
	if a.authenticationAllowed() {
		t.Fatal("agent was not locked after repeated PIN failures")
	}
}
