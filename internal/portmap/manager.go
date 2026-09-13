// Package portmap runs the FRP client inside the YuDesk process. Closing the
// manager therefore closes every public mapping without leaving a helper
// process behind.
package portmap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	frpclient "github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/yudesk/yudesk/internal/relay"
)

type Configuration struct {
	DeviceID    string
	Server      string
	Session     string
	Certificate string
	Mappings    []relay.PortMap
}

type Manager struct {
	mu        sync.Mutex
	directory string
	cancel    context.CancelFunc
	done      chan struct{}
	signature string
	lastError string
	closed    bool
}

func New(directory string) *Manager {
	return &Manager{directory: directory}
}

func (m *Manager) LastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastError
}

func (m *Manager) Update(configuration Configuration) {
	normalize(&configuration)
	signature := configSignature(configuration)
	m.mu.Lock()
	if m.closed || signature == m.signature {
		m.mu.Unlock()
		return
	}
	m.signature = signature
	oldCancel, oldDone := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.lastError = ""
	m.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
	if oldDone != nil {
		select {
		case <-oldDone:
		case <-time.After(3 * time.Second):
		}
	}
	if len(configuration.Mappings) == 0 || configuration.Server == "" || configuration.Session == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.mu.Lock()
	if m.closed || m.signature != signature {
		m.mu.Unlock()
		cancel()
		return
	}
	m.cancel, m.done = cancel, done
	m.mu.Unlock()
	go func() {
		defer close(done)
		if err := m.run(ctx, configuration); err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			m.mu.Lock()
			if m.signature == signature && !m.closed {
				m.lastError = "FRP 映射连接失败：" + err.Error()
			}
			m.mu.Unlock()
		}
	}()
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.signature = ""
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
}

func normalize(configuration *Configuration) {
	configuration.DeviceID = strings.ToUpper(strings.TrimSpace(configuration.DeviceID))
	configuration.Server = strings.TrimSpace(configuration.Server)
	configuration.Session = strings.TrimSpace(configuration.Session)
	sort.Slice(configuration.Mappings, func(i, j int) bool {
		return configuration.Mappings[i].RemotePort < configuration.Mappings[j].RemotePort
	})
}

func configSignature(configuration Configuration) string {
	type mappingSignature struct {
		ID         string
		Protocol   string
		LocalPort  int
		RemotePort int
		Secret     string
	}
	value := struct {
		DeviceID    string
		Server      string
		Session     string
		Certificate string
		Mappings    []mappingSignature
	}{
		DeviceID: configuration.DeviceID, Server: configuration.Server,
		Session: configuration.Session, Certificate: configuration.Certificate,
		Mappings: make([]mappingSignature, 0, len(configuration.Mappings)),
	}
	for _, mapping := range configuration.Mappings {
		value.Mappings = append(value.Mappings, mappingSignature{
			ID: mapping.ID, Protocol: mapping.Protocol, LocalPort: mapping.LocalPort,
			RemotePort: mapping.RemotePort, Secret: mapping.Secret,
		})
	}
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (m *Manager) run(ctx context.Context, configuration Configuration) error {
	host, rawPort, err := net.SplitHostPort(configuration.Server)
	if err != nil {
		return fmt.Errorf("服务器地址无效: %w", err)
	}
	serverPort, err := strconv.Atoi(rawPort)
	if err != nil || serverPort < 1 || serverPort > 65535 {
		return errors.New("服务器端口无效")
	}
	if configuration.Certificate == "" {
		return errors.New("服务器未提供 FRP 证书")
	}
	if err := os.MkdirAll(m.directory, 0700); err != nil {
		return err
	}
	certificatePath := filepath.Join(m.directory, "frp-server.crt")
	if err := os.WriteFile(certificatePath, []byte(configuration.Certificate), 0600); err != nil {
		return fmt.Errorf("保存 FRP 证书: %w", err)
	}

	loginFailExit := false
	tcpMux := false
	tlsEnabled := true
	common := &v1.ClientCommonConfig{
		User:          configuration.DeviceID,
		ClientID:      "yudesk-" + strings.ToLower(configuration.DeviceID),
		ServerAddr:    host,
		ServerPort:    serverPort,
		LoginFailExit: &loginFailExit,
		Metadatas: map[string]string{
			"yudesk_session": configuration.Session,
			"yudesk_client":  "desktop",
		},
		Log: v1.LogConfig{To: filepath.Join(m.directory, "frpc.log"), Level: "warn", MaxDays: 3, DisablePrintColor: true},
		Transport: v1.ClientTransportConfig{
			Protocol: "tcp", WireProtocol: "v2", DialServerTimeout: 8,
			DialServerKeepAlive: 15, TCPMux: &tcpMux, HeartbeatInterval: 5, HeartbeatTimeout: 15,
			TLS: v1.TLSClientConfig{Enable: &tlsEnabled, TLSConfig: v1.TLSConfig{TrustedCaFile: certificatePath, ServerName: host}},
		},
	}
	proxies := make([]v1.ProxyConfigurer, 0, len(configuration.Mappings))
	for _, mapping := range configuration.Mappings {
		if mapping.Protocol != "tcp" || mapping.LocalPort < 1 || mapping.LocalPort > 65535 || mapping.RemotePort < 9000 || mapping.RemotePort > 9500 || mapping.ID == "" || mapping.Secret == "" {
			return errors.New("服务器下发了无效的端口映射")
		}
		proxy := &v1.TCPProxyConfig{
			ProxyBaseConfig: v1.ProxyBaseConfig{
				Name: "yudesk-" + mapping.ID, Type: "tcp",
				Metadatas:    map[string]string{"yudesk_map_id": mapping.ID, "yudesk_map_secret": mapping.Secret},
				ProxyBackend: v1.ProxyBackend{LocalIP: "127.0.0.1", LocalPort: mapping.LocalPort},
			},
			RemotePort: mapping.RemotePort,
		}
		proxies = append(proxies, proxy)
	}
	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll(proxies, nil); err != nil {
		return err
	}
	service, err := frpclient.NewService(frpclient.ServiceOptions{Common: common, ConfigSourceAggregator: source.NewAggregator(configSource)})
	if err != nil {
		return err
	}
	return service.Run(ctx)
}
