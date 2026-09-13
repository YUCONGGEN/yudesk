package agentapp

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	meetingMu   sync.Mutex
	context     context.Context
	relayAddr   string
	transport   relay.DialOptions
	meetingOpen func(context.Context) (relay.MeetingInfo, error)
	meetingEnd  func(context.Context) error
}

type DeviceStatus struct {
	Pending          *approval.Pending `json:"pending,omitempty"`
	ID               string            `json:"id"`
	Code             string            `json:"code"`
	PIN              string            `json:"pin"`
	PINSynced        bool              `json:"pinSynced"`
	PINRevision      uint64            `json:"pinRevision"`
	Name             string            `json:"name"`
	Status           string            `json:"status"`
	Message          string            `json:"message"`
	Online           bool              `json:"online"`
	Connected        bool              `json:"connected"`
	Receiving        bool              `json:"receiving"`
	Active           bool              `json:"active"`
	ActiveUntil      string            `json:"activeUntil"`
	Files            bool              `json:"files"`
	FileDirectory    string            `json:"fileDirectory"`
	Meeting          bool              `json:"meeting"`
	MeetingCode      string            `json:"meetingCode,omitempty"`
	MeetingUntil     time.Time         `json:"meetingUntil,omitempty"`
	PortMapAvailable bool              `json:"portMapAvailable"`
	PortMaps         []relay.PortMap   `json:"portMaps,omitempty"`
	PortMapError     string            `json:"portMapError,omitempty"`
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
	a.portMaps = newManagedPortMaps(o.Directory)
	if err := a.configureFilePermission(o.Directory); err != nil {
		a.portMaps.close()
		lock.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	d := &Device{a: a, cancel: cancel, done: make(chan struct{}), server: o.Server, identityDir: o.Directory, context: ctx, relayAddr: o.Relay, transport: o.Transport}
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
	if a.managementOnline && a.meetingUntil.After(time.Now()) && relay.IsMeetingCode(a.meetingPIN) {
		s.Meeting = true
		s.MeetingCode = a.meetingPIN
		s.MeetingUntil = a.meetingUntil
	}
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
	if a.portMaps != nil {
		s.PortMapAvailable, s.PortMaps, s.PortMapError = a.portMaps.status()
	}
	if !s.Receiving {
		s.Status = "已暂停被远程连接"
	}
	return s
}

func (d *Device) CreatePortMap(ctx context.Context, localPort int, name string) error {
	requestContext, cancel := portMapRequestContext(ctx)
	defer cancel()
	return d.a.createPortMap(requestContext, localPort, name)
}

func (d *Device) DeletePortMap(ctx context.Context, mapID string) error {
	requestContext, cancel := portMapRequestContext(ctx)
	defer cancel()
	return d.a.deletePortMap(requestContext, mapID)
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

// StartMeeting publishes only a short-lived lookup to the authenticated relay.
// The same random code is then checked again inside the end-to-end channel.
func (d *Device) StartMeeting(duration time.Duration) (string, error) {
	if duration <= 0 || duration > 8*time.Hour {
		return "", errors.New("会议时长无效")
	}
	d.meetingMu.Lock()
	defer d.meetingMu.Unlock()
	d.SetReceiving(true)
	a := d.a
	a.statusMu.Lock()
	now := time.Now()
	if a.quitting() {
		a.statusMu.Unlock()
		return "", errors.New("设备已停止，无法创建会议")
	}
	if !a.activeUntil.After(now) {
		a.statusMu.Unlock()
		return "", errors.New("设备尚未激活，无法创建会议")
	}
	if !a.managementOnline {
		a.statusMu.Unlock()
		return "", errors.New("服务器尚未连接，请稍后再创建会议")
	}
	if a.connected {
		a.statusMu.Unlock()
		return "", errors.New("当前正在远程连接，请结束连接后再创建会议")
	}
	deviceID, key := a.id, a.privateKey
	a.statusMu.Unlock()
	baseContext := d.context
	if baseContext == nil {
		baseContext = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(baseContext, 10*time.Second)
	defer cancel()
	var info relay.MeetingInfo
	var err error
	if d.meetingOpen != nil {
		info, err = d.meetingOpen(requestCtx)
	} else {
		info, err = relay.OpenMeeting(requestCtx, d.relayAddr, d.transport, deviceID, key)
	}
	if err != nil {
		return "", err
	}
	if !info.Active || !relay.IsMeetingCode(info.Code) || !info.ExpiresAt.After(time.Now()) {
		return "", errors.New("服务器未能创建有效会议")
	}
	a.statusMu.Lock()
	if a.quitting() || a.connected || !a.activeUntil.After(time.Now()) {
		a.statusMu.Unlock()
		d.closeMeetingDirectory()
		return "", errors.New("设备状态已改变，请稍后重新创建会议")
	}
	a.meetingGeneration++
	generation := a.meetingGeneration
	a.meetingPIN = info.Code
	a.meetingUntil = info.ExpiresAt
	a.statusMu.Unlock()
	time.AfterFunc(time.Until(info.ExpiresAt), func() { d.expireMeeting(generation) })
	return info.Code, nil
}

func (d *Device) EndMeeting() {
	d.meetingMu.Lock()
	wasOpen := d.clearMeetingLocked(0)
	d.meetingMu.Unlock()
	if wasOpen {
		d.closeMeetingDirectory()
	}
}

// DialConference opens only the small TLS-protected WebRTC signaling tunnel.
// Media remains peer-to-peer; the private identity key never leaves Device.
func (d *Device) DialConference(ctx context.Context, code, name string, host bool) (net.Conn, error) {
	code, name = strings.TrimSpace(code), strings.TrimSpace(name)
	if host {
		status := d.Status()
		if !status.Meeting || status.MeetingCode != code || !status.MeetingUntil.After(time.Now()) {
			return nil, errors.New("本机没有有效的主持会议")
		}
	}
	return relay.DialConference(ctx, d.relayAddr, d.transport, d.a.id, d.a.privateKey, code, name, host)
}

func (d *Device) expireMeeting(generation uint64) {
	d.meetingMu.Lock()
	wasOpen := d.clearMeetingLocked(generation)
	d.meetingMu.Unlock()
	if wasOpen {
		d.closeMeetingDirectory()
	}
}

// A zero generation means an explicit stop; otherwise only the timer that
// created the current invitation may expire it.
func (d *Device) clearMeetingLocked(generation uint64) bool {
	a := d.a
	a.statusMu.Lock()
	if generation != 0 && (a.meetingGeneration != generation || a.meetingUntil.After(time.Now())) {
		a.statusMu.Unlock()
		return false
	}
	wasOpen := a.meetingUntil.After(time.Time{}) || a.meetingPIN != ""
	meetingSession := a.meetingSession
	a.meetingGeneration++
	a.meetingPIN = ""
	a.meetingUntil = time.Time{}
	a.statusMu.Unlock()
	if !wasOpen {
		return false
	}
	if meetingSession {
		a.connectionMu.Lock()
		if a.relayConnection != nil {
			_ = a.relayConnection.Close()
		}
		a.connectionMu.Unlock()
	}
	return true
}

func (d *Device) closeMeetingDirectory() {
	requestCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if d.meetingEnd != nil {
		_ = d.meetingEnd(requestCtx)
		return
	}
	_ = relay.CloseMeeting(requestCtx, d.relayAddr, d.transport, d.a.id, d.a.privateKey)
}

func (a *agent) meetingAllowed(pin string) bool {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	return a.meetingUntil.After(time.Now()) && a.meetingPIN != "" && len(pin) == len(a.meetingPIN) && subtleConstantTime(pin, a.meetingPIN)
}

func subtleConstantTime(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for i := range len(left) {
		different |= left[i] ^ right[i]
	}
	return different == 0
}
func (d *Device) Activate(key string) error { _, err := d.a.activate(d.server, key); return err }

func (d *Device) ApprovalEvents() <-chan struct{} { return d.a.approvals.Events() }
func (d *Device) ResolveApproval(id string, accept bool) error {
	return d.a.approvals.Resolve(id, accept)
}
