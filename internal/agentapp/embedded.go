package agentapp

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yudesk/yudesk/internal/approval"
	"github.com/yudesk/yudesk/internal/identity"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/singleinstance"
)

type EmbeddedOptions struct {
	Directory, Name, Server, Relay string
	Transport                      relay.DialOptions
}

type Device struct {
	a           *agent
	cancel      context.CancelFunc
	done        chan struct{}
	receiving   atomic.Bool
	server      string
	identityDir string
	dialMu      sync.Mutex
	dialCancel  context.CancelFunc
}

type DeviceStatus struct {
	Pending       *approval.Pending `json:"pending,omitempty"`
	ID            string            `json:"id"`
	Code          string            `json:"code"`
	PIN           string            `json:"pin"`
	PINSynced     bool              `json:"pinSynced"`
	PINRevision   uint64            `json:"pinRevision"`
	Name          string            `json:"name"`
	Status        string            `json:"status"`
	Message       string            `json:"message"`
	Online        bool              `json:"online"`
	Connected     bool              `json:"connected"`
	Receiving     bool              `json:"receiving"`
	Active        bool              `json:"active"`
	ActiveUntil   string            `json:"activeUntil"`
	Files         bool              `json:"files"`
	FileDirectory string            `json:"fileDirectory"`
}

func StartEmbedded(parent context.Context, o EmbeddedOptions) (*Device, error) {
	if o.Directory == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		o.Directory = filepath.Join(root, "yudesk")
	}
	if err := os.MkdirAll(o.Directory, 0700); err != nil {
		return nil, err
	}
	lock, owned, err := singleinstance.Acquire(filepath.Join(o.Directory, "agent.lock"))
	if err != nil {
		return nil, err
	}
	if !owned {
		if old := activeAgentUI(o.Directory); old != "" {
			_ = requestExistingAgentExit(old)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !owned && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			lock, owned, err = singleinstance.Acquire(filepath.Join(o.Directory, "agent.lock"))
			if err != nil {
				return nil, err
			}
		}
		if !owned {
			return nil, errors.New("旧被控端仍在运行，请先退出后再打开 YuDesk")
		}
	}
	id, err := identity.Load(o.Directory, "")
	if err != nil {
		lock.Close()
		return nil, err
	}
	if o.Name == "" {
		o.Name, _ = os.Hostname()
	}
	if o.Server == "" {
		o.Server = defaultWebServer
	}
	if o.Relay == "" {
		o.Relay = defaultRelayAddress
	}
	a := &agent{id: id.ID, pin: id.PIN, name: o.Name, privateKey: id.PrivateKey, allowControl: true, quit: make(chan struct{}), managed: true, relayStatus: "正在连接服务器", approvals: approval.New()}
	if err := a.configureFilePermission(o.Directory); err != nil {
		lock.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	d := &Device{a: a, cancel: cancel, done: make(chan struct{}), server: o.Server, identityDir: o.Directory}
	d.receiving.Store(true)
	go func() {
		select {
		case <-ctx.Done():
			a.requestExit()
		case <-a.quit:
			cancel()
		}
	}()
	go a.monitorManagement(ctx, o.Relay, o.Transport)
	go a.enforceLicenseDeadline()
	go func() {
		defer close(d.done)
		defer lock.Close()
		defer cancel()
		defer a.requestExit()
		for !a.quitting() {
			if !a.waitForActiveLicense(o.Server) {
				return
			}
			if !d.receiving.Load() {
				if !a.waitOrQuit(300 * time.Millisecond) {
					return
				}
				continue
			}
			a.setRelayStatus("在线，等待连接", false, "")
			dialCtx, dialCancel := context.WithCancel(ctx)
			d.dialMu.Lock()
			d.dialCancel = dialCancel
			if !d.receiving.Load() {
				dialCancel()
			}
			d.dialMu.Unlock()
			conn, err := relay.DialWithContext(dialCtx, o.Relay, o.Transport, relay.Hello{Role: "agent", ID: id.ID, Name: a.name, PIN: a.currentPIN(), PublicKey: id.PrivateKey.Public().(ed25519.PublicKey)})
			d.dialMu.Lock()
			d.dialCancel = nil
			d.dialMu.Unlock()
			dialCancel()
			if err != nil {
				if a.quitting() {
					return
				}
				if code := relay.RejectionCode(err); code != "" && code != "LICENSE_REQUIRED" {
					a.terminateWithNotice("服务器已终止运行", err.Error())
					return
				}
				a.setRelayStatus("连接暂时中断，正在恢复", false, "")
				if !a.waitOrQuit(3 * time.Second) {
					return
				}
				continue
			}
			if !a.setRelayConnection(conn) {
				conn.Close()
				return
			}
			if !d.receiving.Load() {
				conn.Close()
				a.setRelayConnection(nil)
				continue
			}
			a.setRelayStatus("正在被远程连接", true, "")
			a.handle(conn)
			a.setRelayConnection(nil)
			a.setRelayStatus("在线，等待连接", false, "")
			if !a.waitOrQuit(300 * time.Millisecond) {
				return
			}
		}
	}()
	return d, nil
}

func (d *Device) Done() <-chan struct{} { return d.done }
func (d *Device) Close()                { d.cancel(); d.a.requestExit() }
func (d *Device) Status() DeviceStatus {
	a := d.a
	a.statusMu.RLock()
	s := DeviceStatus{ID: a.id, Code: a.deviceCode, PIN: a.pin, Name: a.name, Status: a.relayStatus, Message: a.uiMessage, Online: a.managementOnline, Connected: a.connected, Active: a.activeUntil.After(time.Now()), ActiveUntil: "未激活"}
	s.PINSynced = a.managementOnline && a.reportedPIN == a.pin
	s.PINRevision = a.pinRevision
	if s.Active {
		s.ActiveUntil = displayLicenseExpiry(a.activeUntil)
	}
	a.statusMu.RUnlock()
	s.Receiving = d.receiving.Load()
	s.Files = a.fileRoot() != ""
	s.FileDirectory = a.shareDir
	if a.approvals != nil {
		s.Pending = a.approvals.Pending()
	}
	if !s.Receiving {
		s.Status = "已暂停被远程连接"
	}
	return s
}
func (d *Device) SetReceiving(enabled bool) {
	d.receiving.Store(enabled)
	if !enabled {
		d.a.approvals.Cancel()
		d.dialMu.Lock()
		if d.dialCancel != nil {
			d.dialCancel()
		}
		d.dialMu.Unlock()
		d.a.connectionMu.Lock()
		if d.a.relayConnection != nil {
			_ = d.a.relayConnection.Close()
		}
		d.a.connectionMu.Unlock()
	}
}
func (d *Device) SetFiles(enabled bool) error { return d.a.setFilePermission(enabled) }
func (d *Device) RotatePIN() (string, error) {
	a := d.a
	a.statusMu.Lock()
	if a.quitting() {
		a.statusMu.Unlock()
		return "", errors.New("设备已停止，无法更换 PIN")
	}
	pin, err := identity.RotatePIN(d.identityDir, a.pin)
	if err == nil {
		a.pin = pin
		a.pinRevision++
	}
	a.statusMu.Unlock()
	if err == nil {
		a.clearAuthenticationFailures()
	}
	return pin, err
}
func (d *Device) Activate(key string) error { _, err := d.a.activate(d.server, key); return err }

func (d *Device) ApprovalEvents() <-chan struct{} { return d.a.approvals.Events() }
func (d *Device) ResolveApproval(id string, accept bool) error {
	return d.a.approvals.Resolve(id, accept)
}
