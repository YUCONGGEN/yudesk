// Package core is the platform-neutral, gomobile-compatible YuDesk transport.
// Android owns capture consent, rendering and Accessibility permission. No Go
// code can silently enable these permissions or start Android capture.
package core

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/yudesk/yudesk/internal/approval"
	"github.com/yudesk/yudesk/internal/identity"
	"github.com/yudesk/yudesk/internal/relay"
)

const Version = "2.0.0-preview.2"
const relayAddress = "www.yucg.cn:8233"
const fingerprint = "20FC953E48B6BEED7FB3A5F73BF177CC4E2557CF274C409FF4F0179E5FA0F836"

var relayOptions = relay.DialOptions{TLS: true, Fingerprint: fingerprint}

type status struct {
	Running       bool      `json:"running"`
	Online        bool      `json:"online"`
	Active        bool      `json:"active"`
	ActiveUntil   time.Time `json:"activeUntil"`
	DeviceCode    string    `json:"deviceCode"`
	PIN           string    `json:"pin"`
	PINSynced     bool      `json:"pinSynced"`
	Sharing       bool      `json:"sharing"`
	Accessibility bool      `json:"accessibility"`
	Connected     bool      `json:"connected"`
	Message       string    `json:"message"`
}

// Engine has one management loop and, while sharing is explicitly enabled,
// one sequential agent connection. At most one incoming and outgoing session.
type Engine struct {
	mu              sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	identity        identity.Identity
	dir, name       string
	state           status
	agentCancel     context.CancelFunc
	agentGeneration uint64
	incomingSession uint64
	controller      *Session
	connecting      bool
	connectCancel   context.CancelFunc
	approvals       *approval.Broker
	frame           *Frame
	frameSequence   int64
	frameWake       chan struct{}
	input           chan string
	inputErrors     chan string
	canInput        bool
	streaming       bool
	failures        []time.Time
	changed         chan struct{}
}

// NewEngine must receive Android's private files directory, never shared storage.
func NewEngine(privateDirectory, deviceName string) (*Engine, error) {
	if privateDirectory == "" {
		return nil, errors.New("需要应用私有存储目录")
	}
	id, err := identity.Load(privateDirectory, "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{ctx: ctx, cancel: cancel, identity: id, dir: privateDirectory, name: strings.TrimSpace(deviceName), frameWake: make(chan struct{}, 1), input: make(chan string, 64), inputErrors: make(chan string, 8), changed: make(chan struct{}), approvals: approval.New()}
	if e.name == "" {
		e.name = "Android"
	}
	if len(e.name) > 128 {
		e.name = e.name[:128]
	}
	e.state = status{Running: true, PIN: id.PIN, Message: "连接加密管理通道…"}
	go e.management()
	go e.enforceExpiry()
	return e, nil
}

func (e *Engine) StatusJSON() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, _ := json.Marshal(e.state)
	return string(b)
}
func (e *Engine) notifyLocked() { close(e.changed); e.changed = make(chan struct{}) }
func (e *Engine) permittedLocked() bool {
	return e.state.Running && e.state.Online && e.state.Active && (e.state.ActiveUntil.IsZero() || time.Now().Before(e.state.ActiveUntil))
}
func (e *Engine) pin() string { e.mu.Lock(); defer e.mu.Unlock(); return e.identity.PIN }

func (e *Engine) RotatePIN() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := identity.RotatePIN(e.dir, e.identity.PIN)
	if err != nil {
		return err
	}
	e.identity.PIN = p
	e.state.PIN = p
	e.state.PINSynced = false
	e.notifyLocked()
	return nil
}

// SetSharing is called only after MediaProjection system approval and a running
// mediaProjection foreground service. False immediately cancels every receiver.
func (e *Engine) SetSharing(enabled bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.Sharing = enabled && e.state.Running
	if !e.state.Sharing {
		e.stopAgentLocked()
		e.frame = nil
	}
	e.reconcileLocked()
	e.notifyLocked()
}
func (e *Engine) SetAccessibility(enabled bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.Accessibility = enabled
	if !enabled {
		e.releaseLocked()
	}
	e.notifyLocked()
}
func (e *Engine) CanInput() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.permittedLocked() && e.state.Sharing && e.state.Accessibility && e.canInput
}
func (e *Engine) WantsFrame() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.permittedLocked() && e.state.Sharing && e.streaming
}

func (e *Engine) stopAgentLocked() {
	if e.agentCancel != nil {
		e.agentCancel()
		e.agentCancel = nil
	}
	e.agentGeneration++
	e.incomingSession++
	e.state.Connected = false
	e.streaming = false
	e.canInput = false
	e.approvals.Cancel()
	e.releaseLocked()
}
func (e *Engine) releaseLocked() {
	for {
		select {
		case <-e.input:
		default:
			select {
			case e.input <- `{"release":true}`:
			default:
			}
			return
		}
	}
}
func (e *Engine) reconcileLocked() {
	if !e.permittedLocked() || !e.state.Sharing {
		e.stopAgentLocked()
		return
	}
	if e.agentCancel == nil {
		ctx, cancel := context.WithCancel(e.ctx)
		e.agentCancel = cancel
		e.agentGeneration++
		generation := e.agentGeneration
		go e.agentLoop(ctx, generation)
	}
}
func (e *Engine) stop(reason string) {
	e.mu.Lock()
	if !e.state.Running {
		e.mu.Unlock()
		return
	}
	e.state.Running = false
	e.state.Online = false
	e.state.Sharing = false
	e.state.Message = reason
	e.stopAgentLocked()
	if e.connectCancel != nil {
		e.connectCancel()
	}
	s := e.controller
	e.notifyLocked()
	e.mu.Unlock()
	e.cancel()
	if s != nil {
		s.Close()
	}
}
func (e *Engine) Close() { e.stop("应用已停止") }

func (e *Engine) management() {
	backoff := time.Second
	for e.ctx.Err() == nil {
		err := relay.WatchDeviceWithPIN(e.ctx, relayAddress, relayOptions, relay.Hello{ID: e.identity.ID, Name: e.name, PIN: e.pin(), PublicKey: e.identity.PrivateKey.Public().(ed25519.PublicKey)}, e.identity.PrivateKey, e.pin, func(m relay.ControlMessage) {
			e.mu.Lock()
			defer e.mu.Unlock()
			if !e.state.Running {
				return
			}
			backoff = time.Second
			e.state.Online = true
			e.state.Active = m.Active
			e.state.ActiveUntil = m.ActiveUntil
			if relay.IsDeviceCode(m.DeviceCode) {
				e.state.DeviceCode = m.DeviceCode
			}
			e.state.PINSynced = m.PIN == e.identity.PIN
			if m.Active {
				e.state.Message = "管理通道已验证"
			} else {
				e.state.Message = "等待管理员激活设备"
				if e.controller != nil {
					e.controller.Close()
				}
				if e.connectCancel != nil {
					e.connectCancel()
				}
			}
			e.reconcileLocked()
			e.notifyLocked()
		})
		if e.ctx.Err() != nil {
			return
		}
		if relay.RejectionCode(err) != "" {
			e.stop("服务器已停止设备：" + err.Error())
			return
		}
		e.mu.Lock()
		e.state.Online = false
		e.state.Message = "网络中断，接收已暂停"
		e.stopAgentLocked()
		s := e.controller
		if e.connectCancel != nil {
			e.connectCancel()
		}
		e.notifyLocked()
		e.mu.Unlock()
		if s != nil {
			s.Close()
		}
		if !pause(e.ctx, backoff) {
			return
		}
		backoff = min(30*time.Second, backoff*2)
	}
}

// ReportInputError is called by the native Accessibility adapter when Android
// actually rejects an operation; the controller sees a real error, not fake ACKs.
func (e *Engine) ReportInputError(message string) {
	if len(message) > 512 {
		message = message[:512]
	}
	select {
	case e.inputErrors <- message:
	default:
	}
}
func (e *Engine) enforceExpiry() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.mu.Lock()
			expired := !e.state.ActiveUntil.IsZero() && !time.Now().Before(e.state.ActiveUntil)
			e.mu.Unlock()
			if expired {
				e.stop("设备授权已到期，请联系管理员续期")
				return
			}
		}
	}
}
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
