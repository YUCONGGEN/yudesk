package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	portmapclient "github.com/yudesk/yudesk/internal/portmap"
	"github.com/yudesk/yudesk/internal/relay"
)

// This test is opt-in because it requires the pinned FRPS executable. Release
// verification sets YUDESK_FRPS_BIN and exercises the real FRP plugin protocol,
// TLS, embedded client, TCP listener and local forwarding end to end.
func TestManagedPortMapWithRealFRPS(t *testing.T) {
	frpsBinary := strings.TrimSpace(os.Getenv("YUDESK_FRPS_BIN"))
	if frpsBinary == "" {
		t.Skip("YUDESK_FRPS_BIN is not set")
	}
	frpsBinary, err := filepath.Abs(frpsBinary)
	if err != nil {
		t.Fatal(err)
	}

	b, ids := portMapFixture(t, 1)
	plugin := httptest.NewServer(http.HandlerFunc(b.serveFRPPlugin))
	defer plugin.Close()
	pluginURL, err := url.Parse(plugin.URL)
	if err != nil {
		t.Fatal(err)
	}

	controlPort := reserveTCPPort(t, 10000, 19000)
	publicPort := reserveTCPPort(t, portMapMin, portMapMax)
	for port := portMapMin; port <= portMapMax; port++ {
		if port != publicPort {
			b.portByNumber[port] = "reserved-by-integration-test"
		}
	}
	b.portMapServer = net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort))

	directory := t.TempDir()
	certificate, key := createPortMapTestCertificate(t)
	certificatePath := filepath.Join(directory, "frps.crt")
	keyPath := filepath.Join(directory, "frps.key")
	if err := os.WriteFile(certificatePath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	b.portMapCertificate = string(certificate)
	configPath := filepath.Join(directory, "frps.toml")
	config := fmt.Sprintf(`bindAddr = "127.0.0.1"
proxyBindAddr = "127.0.0.1"
bindPort = %d
transport.tcpMux = false
transport.heartbeatTimeout = 15
transport.tls.force = true
transport.tls.certFile = %q
transport.tls.keyFile = %q
allowPorts = [{ start = 9000, end = 9500 }]
maxPortsPerClient = 5
auth.method = "token"
auth.token = ""
log.to = "console"
log.level = "warn"
[[httpPlugins]]
name = "yudesk-manager"
addr = %q
path = "/"
ops = ["Login", "NewProxy", "CloseProxy", "Ping", "NewWorkConn", "NewUserConn"]
`, controlPort, filepath.ToSlash(certificatePath), filepath.ToSlash(keyPath), pluginURL.Host)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	frps := exec.Command(frpsBinary, "-c", configPath)
	frpsOutput := &strings.Builder{}
	frps.Stdout, frps.Stderr = frpsOutput, frpsOutput
	if err := frps.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = frps.Process.Kill()
		_ = frps.Wait()
	})
	waitForTCP(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort)), 8*time.Second, func() string { return frpsOutput.String() })

	localListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer localListener.Close()
	go func() {
		for {
			connection, acceptErr := localListener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()
	localPort := localListener.Addr().(*net.TCPAddr).Port
	deviceID := ids[0]
	result := b.createPortMap(deviceID, "session-0", relay.PortMapCommand{RequestID: "real-frp", LocalPort: localPort})
	if !result.OK {
		t.Fatal(result.Message)
	}
	mappings := b.portMapStatus(deviceID, "session-0")
	if len(mappings) != 1 || mappings[0].RemotePort != publicPort {
		t.Fatalf("unexpected issued mapping: %+v", mappings)
	}

	manager := portmapclient.New(filepath.Join(directory, "client"))
	defer manager.Close()
	manager.Update(portmapclient.Configuration{
		DeviceID: deviceID, Server: b.portMapServer, Session: "session-0",
		Certificate: string(certificate), Mappings: mappings,
	})
	deadline := time.Now().Add(10 * time.Second)
	online := false
	for time.Now().Before(deadline) {
		b.Lock()
		online = b.portMaps[mappings[0].ID] != nil && b.portMaps[mappings[0].ID].Online
		b.Unlock()
		if online {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !online {
		t.Fatalf("FRP proxy did not become online: client=%s server=%s", manager.LastError(), frpsOutput.String())
	}

	publicAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(publicPort))
	connection, err := net.DialTimeout("tcp", publicAddress, 3*time.Second)
	if err != nil {
		t.Fatalf("dial managed public port: %v; client=%s server=%s", err, manager.LastError(), frpsOutput.String())
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	payload := []byte("YuDesk managed FRP end-to-end")
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, received); err != nil {
		t.Fatal(err)
	}
	if string(received) != string(payload) {
		t.Fatalf("forwarded payload changed: %q", received)
	}

	b.closeDevicePortMaps(deviceID)
	if ok, _ := b.authorizeFRPRequest(newUserConnectionRequest(t, deviceID, "session-0", mappings[0].ID)); ok {
		t.Fatal("closed mapping remained authorized")
	}
	manager.Update(portmapclient.Configuration{DeviceID: deviceID})
}

func reserveTCPPort(t *testing.T, minimum, maximum int) int {
	t.Helper()
	for port := minimum; port <= maximum; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			_ = listener.Close()
			return port
		}
	}
	t.Fatalf("no free TCP port in %d-%d", minimum, maximum)
	return 0
}

func waitForTCP(t *testing.T, address string, timeout time.Duration, diagnostic func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 150*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("TCP service %s did not start: %s", address, diagnostic())
}

func createPortMapTestCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func newUserConnectionRequest(t *testing.T, deviceID, session, mapID string) frpPluginRequest {
	t.Helper()
	content, err := json.Marshal(frpPluginUserConnection{
		User:      frpPluginUser{User: deviceID, Metas: map[string]string{"yudesk_session": session}},
		ProxyName: expectedFRPProxyName(deviceID, mapID), ProxyType: "tcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	return frpPluginRequest{Version: "0.1.0", Op: "NewUserConn", Content: content}
}
