package viewerapp

import (
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yudesk/yudesk/internal/agentapp"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/winhost"
)

const unifiedVersion = "desktop-unified-v5"

//go:embed ui/dashboard.html
var dashboardHTML string

//go:embed ui/dashboard.css
var dashboardCSS string

//go:embed ui/dashboard.js
var dashboardJS string

var dashboardPage = template.Must(template.New("dashboard").Parse(dashboardHTML + footerHTML))

type unifiedDesk struct {
	device           *agentapp.Device
	host             *viewerHost
	config           viewerConfig
	directory, token string
	hidden, stopped  atomic.Bool
}

func enableUnified(host *viewerHost, config viewerConfig, directory, token string) error {
	device, err := agentapp.StartEmbedded(host.ctx, agentapp.EmbeddedOptions{Directory: config.deviceDir, Server: config.statusServer, Relay: config.relayAddr, Transport: relay.DialOptions{TLS: config.relayTLS, CAFile: config.relayCA, Fingerprint: config.relayFingerprint, Insecure: config.relayInsecure}})
	if err != nil {
		return err
	}
	d := &unifiedDesk{device: device, host: host, config: config, directory: directory, token: token}
	host.mu.Lock()
	host.desk = d
	host.mu.Unlock()
	host.version.Store(unifiedVersion)
	go func() {
		select {
		case <-host.ctx.Done():
			device.Close()
			return
		case <-device.Done():
		}
		if host.ctx.Err() != nil {
			return
		}
		d.stopped.Store(true)
		if d.hidden.Load() && config.openUI {
			_ = openBrowser(viewerUIURL(host.listener.Addr().String(), token), false)
		}
		time.AfterFunc(1500*time.Millisecond, host.cancel)
	}()
	return nil
}

func (d *unifiedDesk) Close() { d.device.Close() }
func (d *unifiedDesk) render(w http.ResponseWriter, id, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = dashboardPage.Execute(w, map[string]any{"Token": d.token, "DeviceID": id, "Message": message})
}

func (d *unifiedDesk) serve(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path == "/api/local/service/status" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(winhost.Status())
		return true
	}
	if d.stopped.Load() && path == "/" {
		s := d.device.Status()
		serveViewerExitPage(w, "YuDesk 已停止："+s.Status)
		return true
	}
	if path == "/assets/dashboard.css" || path == "/assets/dashboard.js" {
		w.Header().Set("Cache-Control", "no-store")
		if path == "/assets/dashboard.css" {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			_, _ = w.Write([]byte(dashboardCSS))
		} else {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write([]byte(dashboardJS))
		}
		return true
	}
	if path != "/api/devices" && !strings.HasPrefix(path, "/api/local/") {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if path == "/api/local/status" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.device.Status())
		return true
	}
	if path == "/api/devices" && r.Method == http.MethodGet {
		records, err := loadHistory(d.directory)
		if err != nil {
			http.Error(w, "读取设备列表失败", 500)
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"local": d.device.Status(), "devices": records})
		return true
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return true
	}
	var p struct {
		Confirmed bool   `json:"confirmed"`
		Enabled   bool   `json:"enabled"`
		Key       string `json:"key"`
		Code      string `json:"code"`
		Name      string `json:"name"`
	}
	if !decodeJSON(w, r, &p, 8192) {
		return true
	}
	var err error
	switch path {
	case "/api/local/service/install", "/api/local/service/remove":
		if !p.Confirmed {
			err = errors.New("请确认管理员授权安装或卸载服务")
		} else {
			err = winhost.ElevateInstall(path == "/api/local/service/remove")
		}
	case "/api/local/service/restart":
		err = winhost.RestartInstalled()
		if err == nil {
			time.AfterFunc(200*time.Millisecond, d.host.cancel)
		}
	case "/api/local/receiving":
		d.device.SetReceiving(p.Enabled)
	case "/api/local/files":
		err = d.device.SetFiles(p.Enabled)
	case "/api/local/pin/rotate":
		_, err = d.device.RotatePIN()
	case "/api/local/activate":
		err = d.device.Activate(strings.TrimSpace(p.Key))
	case "/api/local/device/add":
		p.Code = strings.ReplaceAll(strings.TrimSpace(p.Code), " ", "")
		if !relay.IsDeviceCode(p.Code) || len([]rune(p.Name)) > 80 {
			err = errors.New("设备码应为 9 位数字，名称不超过 80 字")
		} else if p.Code == d.device.Status().Code {
			err = errors.New("本机已在设备列表中")
		} else {
			err = updateHistory(d.directory, connectionRecord{DeviceID: p.Code, Name: strings.TrimSpace(p.Name)}, false)
		}
	case "/api/local/device/remove":
		err = updateHistory(d.directory, connectionRecord{DeviceID: p.Code}, true)
	case "/api/local/hide":
		d.hidden.Store(true)
		d.host.tracker.Suspend()
		err = d.host.window.Hide()
	default:
		http.NotFound(w, r)
		return true
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	return true
}

func dashboardURL(token string) string { return "/?access_token=" + url.QueryEscape(token) }
