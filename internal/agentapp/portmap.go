package agentapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/yudesk/yudesk/internal/portmap"
	"github.com/yudesk/yudesk/internal/relay"
)

type managedPortMaps struct {
	mu          sync.Mutex
	manager     *portmap.Manager
	updates     chan portmap.Configuration
	stop        chan struct{}
	commands    []relay.PortMapCommand
	waiters     map[string]chan relay.PortMapResult
	mappings    []relay.PortMap
	server      string
	session     string
	certificate string
	closed      bool
}

func newManagedPortMaps(directory string) *managedPortMaps {
	p := &managedPortMaps{
		manager: portmap.New(directory), updates: make(chan portmap.Configuration, 1), stop: make(chan struct{}),
		waiters: make(map[string]chan relay.PortMapResult),
	}
	go func() {
		for {
			select {
			case configuration := <-p.updates:
				p.manager.Update(configuration)
			case <-p.stop:
				return
			}
		}
	}()
	return p
}

func (p *managedPortMaps) apply(deviceID string, message relay.ControlMessage) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.server, p.session, p.certificate = message.PortMapServer, message.PortMapSession, message.PortMapCertificate
	p.mappings = append(p.mappings[:0], message.PortMaps...)
	if result := message.PortMapResult; result != nil {
		if waiter := p.waiters[result.RequestID]; waiter != nil {
			delete(p.waiters, result.RequestID)
			waiter <- *result
		}
	}
	configuration := portmap.Configuration{
		DeviceID: deviceID, Server: p.server, Session: p.session,
		Certificate: p.certificate, Mappings: append([]relay.PortMap(nil), p.mappings...),
	}
	p.mu.Unlock()
	p.queueUpdate(configuration)
}

func (p *managedPortMaps) offline(deviceID string) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.server, p.session, p.certificate = "", "", ""
	p.mappings = nil
	configuration := portmap.Configuration{DeviceID: deviceID}
	p.mu.Unlock()
	p.queueUpdate(configuration)
}

func (p *managedPortMaps) queueUpdate(configuration portmap.Configuration) {
	select {
	case <-p.stop:
		return
	case p.updates <- configuration:
	default:
		select {
		case <-p.updates:
		default:
		}
		select {
		case <-p.stop:
			return
		case p.updates <- configuration:
		default:
		}
	}
}

func (p *managedPortMaps) nextCommand() *relay.PortMapCommand {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.commands) == 0 {
		return nil
	}
	command := p.commands[0]
	p.commands = p.commands[1:]
	return &command
}

func (p *managedPortMaps) request(ctx context.Context, command relay.PortMapCommand) error {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	command.RequestID = base64.RawURLEncoding.EncodeToString(raw)
	waiter := make(chan relay.PortMapResult, 1)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("端口映射已停止")
	}
	if p.server == "" || p.session == "" {
		p.mu.Unlock()
		return errors.New("端口映射服务器尚未连接")
	}
	if len(p.commands) >= 8 {
		p.mu.Unlock()
		return errors.New("操作过于频繁，请稍后重试")
	}
	p.waiters[command.RequestID] = waiter
	p.commands = append(p.commands, command)
	p.mu.Unlock()
	select {
	case result := <-waiter:
		if !result.OK {
			return errors.New(result.Message)
		}
		return nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.waiters, command.RequestID)
		p.mu.Unlock()
		return errors.New("服务器处理端口映射超时，请重试")
	}
}

func (p *managedPortMaps) status() (bool, []relay.PortMap, string) {
	p.mu.Lock()
	available := !p.closed && p.server != "" && p.session != ""
	mappings := append([]relay.PortMap(nil), p.mappings...)
	p.mu.Unlock()
	for i := range mappings {
		mappings[i].Secret = ""
	}
	return available, mappings, p.manager.LastError()
}

func (p *managedPortMaps) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	for requestID, waiter := range p.waiters {
		delete(p.waiters, requestID)
		waiter <- relay.PortMapResult{RequestID: requestID, Message: "YuDesk 正在退出"}
	}
	close(p.stop)
	p.mu.Unlock()
	p.manager.Close()
}

func (a *agent) managementReply() relay.ControlMessage {
	reply := relay.ControlMessage{Type: "pong", PIN: a.currentPIN()}
	if a.portMaps != nil {
		reply.PortMapCommand = a.portMaps.nextCommand()
	}
	return reply
}

func (a *agent) createPortMap(ctx context.Context, localPort int, name string) error {
	if a.portMaps == nil {
		return errors.New("当前运行模式不支持端口映射")
	}
	if localPort < 1 || localPort > 65535 {
		return errors.New("本地端口必须在 1–65535 之间")
	}
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 40 {
		return errors.New("备注不能超过 40 个字符")
	}
	return a.portMaps.request(ctx, relay.PortMapCommand{Action: "create", LocalPort: localPort, Name: name})
}

func (a *agent) deletePortMap(ctx context.Context, mapID string) error {
	if a.portMaps == nil {
		return errors.New("当前运行模式不支持端口映射")
	}
	mapID = strings.TrimSpace(mapID)
	if mapID == "" || len(mapID) > 64 {
		return errors.New("映射编号无效")
	}
	return a.portMaps.request(ctx, relay.PortMapCommand{Action: "delete", MapID: mapID})
}

func portMapRequestContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 12*time.Second)
}
