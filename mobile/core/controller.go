package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
	"github.com/yudesk/yudesk/internal/stream"
)

type Session struct {
	ctx      context.Context
	cancel   context.CancelFunc
	conn     *protocol.Conn
	mu       sync.Mutex
	pending  map[string]chan readResult
	sequence atomic.Uint64
	out      chan protocol.Message
	frame    *Frame
	acks     map[int64]string
	changed  chan struct{}
	revision int64
	control  bool
	platform string
	message  string
	rtt      int64
	closed   bool
}

// Connect blocks only its caller's worker thread. Empty PIN waits for the
// remote user's explicit decision, up to 60 seconds plus protocol overhead.
func (e *Engine) Connect(deviceCode, pin string, control bool) (*Session, error) {
	code := strings.ReplaceAll(strings.TrimSpace(deviceCode), " ", "")
	if !relay.IsDeviceCode(code) {
		return nil, errors.New("设备码必须是 9 位数字")
	}
	if pin != "" && (len(pin) != 6 || strings.Trim(pin, "0123456789") != "") {
		return nil, errors.New("PIN 应为 6 位数字，也可以留空请求同意")
	}
	e.mu.Lock()
	if !e.permittedLocked() {
		e.mu.Unlock()
		return nil, errors.New("管理通道未连接或设备未激活")
	}
	if e.connecting {
		e.mu.Unlock()
		return nil, errors.New("已有连接正在进行")
	}
	if e.controller != nil {
		e.controller.Close()
		e.controller = nil
	}
	ctx, cancel := context.WithCancel(e.ctx)
	e.connectCancel = cancel
	e.connecting = true
	e.mu.Unlock()
	ok := false
	defer func() {
		e.mu.Lock()
		e.connecting = false
		e.connectCancel = nil
		e.mu.Unlock()
		if !ok {
			cancel()
		}
	}()
	r, err := relay.ResolveDevice(ctx, relayAddress, relayOptions, code)
	if err != nil {
		return nil, err
	}
	if r.ID == e.identity.ID {
		return nil, errors.New("不能连接本机")
	}
	raw, err := relay.DialWithContext(ctx, relayAddress, relayOptions, relay.Hello{Role: "viewer", ID: r.ID})
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	secured, err := secureconn.Connect(raw, r.ID)
	if err != nil {
		raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	s, err := openSession(ctx, secured, pin, control)
	if err != nil {
		return nil, err
	}
	context.AfterFunc(s.ctx, cancel)
	e.mu.Lock()
	if !e.permittedLocked() || ctx.Err() != nil {
		e.mu.Unlock()
		s.Close()
		return nil, errors.New("设备状态已改变，连接取消")
	}
	e.controller = s
	e.mu.Unlock()
	ok = true
	return s, nil
}
func (e *Engine) CancelConnect() {
	e.mu.Lock()
	if e.connectCancel != nil {
		e.connectCancel()
	}
	e.mu.Unlock()
}

func openSession(parent context.Context, conn net.Conn, pin string, control bool) (*Session, error) {
	ctx, cancel := context.WithCancel(parent)
	s := &Session{ctx: ctx, cancel: cancel, conn: protocol.NewConn(conn), pending: map[string]chan readResult{}, out: make(chan protocol.Message, 64), acks: map[int64]string{}, changed: make(chan struct{}), message: "正在连接…"}
	go s.writer()
	go s.reader()
	go func() { <-ctx.Done(); _ = conn.Close() }()
	mode := "view"
	if control {
		mode = "control"
	}
	auth, err := s.request("auth", map[string]any{"pin": pin, "mode": mode, "audio": false, "audioOnDemand": true}, 75*time.Second)
	if err != nil {
		s.Close()
		return nil, err
	}
	allowed, _ := auth.Meta["control"].(bool)
	s.mu.Lock()
	s.control = control && allowed
	s.platform, _ = auth.Meta["platform"].(string)
	s.mu.Unlock()
	info, err := s.request("info", nil, 10*time.Second)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.mu.Lock()
	if s.platform == "" {
		s.platform, _ = info.Meta["platform"].(string)
	}
	s.mu.Unlock()
	options := stream.Options{ProfileVersion: 1, FPS: 30, Quality: 70, Mode: "adaptive", MaxWidth: 1280, MaxMbps: 8, SaveIdle: true, FrameAck: true, TileDelta: false}
	if _, err = s.request("stream_start", options, 15*time.Second); err != nil {
		s.Close()
		return nil, err
	}
	s.mu.Lock()
	s.message = "已连接"
	if control && !allowed {
		s.message = "已连接，仅观看：对方未授权远程操作"
	}
	s.mu.Unlock()
	go s.heartbeat()
	return s, nil
}
func (s *Session) signalLocked() { close(s.changed); s.changed = make(chan struct{}) }
func (s *Session) fail(message string) {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.message = message
		s.frame = nil
		s.signalLocked()
	}
	s.mu.Unlock()
	s.cancel()
	// openSession owns a cancellation watcher that closes the transport on a
	// worker goroutine. TLS Close can wait for close_notify on a congested link;
	// never make the Android UI or the management-state lock wait for that I/O.
}
func (s *Session) Close() { s.fail("远程连接已结束") }
func (s *Session) StatusJSON() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"closed": s.closed, "message": s.message, "control": s.control, "platform": s.platform, "rttMs": s.rtt, "ready": s.frame != nil})
	return string(b)
}

func (s *Session) enqueue(m protocol.Message) error {
	select {
	case <-s.ctx.Done():
		return errors.New("连接已结束")
	default:
	}
	select {
	case s.out <- m:
		return nil
	default:
		s.fail("发送队列已满，已安全断开，请重新连接")
		return errors.New("发送队列已满")
	}
}
func (s *Session) writer() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case m := <-s.out:
			if err := write(s.ctx, s.conn, m); err != nil {
				s.fail("网络写入失败：" + err.Error())
				return
			}
		}
	}
}
func (s *Session) request(method string, params any, timeout time.Duration) (protocol.Message, error) {
	id := strconv.FormatUint(s.sequence.Add(1), 10)
	ch := make(chan readResult, 1)
	raw, err := json.Marshal(params)
	if err != nil {
		return protocol.Message{}, err
	}
	s.mu.Lock()
	if len(s.pending) >= 8 {
		s.mu.Unlock()
		return protocol.Message{}, errors.New("请求过多")
	}
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	if err = s.enqueue(protocol.Message{Kind: "request", ID: id, Method: method, Params: raw}); err != nil {
		return protocol.Message{}, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-s.ctx.Done():
		return protocol.Message{}, errors.New("连接已结束")
	case <-t.C:
		return protocol.Message{}, fmt.Errorf("%s 请求超时", method)
	case r := <-ch:
		if !r.message.OK {
			return r.message, errors.New(r.message.Error)
		}
		return r.message, r.err
	}
}
func (s *Session) reader() {
	for {
		m, err := s.conn.ReadMessage()
		if err != nil {
			s.fail("连接中断：" + err.Error())
			return
		}
		if m.Kind == "response" {
			s.mu.Lock()
			ch := s.pending[m.ID]
			s.mu.Unlock()
			if ch != nil {
				select {
				case ch <- readResult{message: m}:
				default:
				}
			}
			continue
		}
		if m.Kind != "event" {
			continue
		}
		switch m.Method {
		case "frame":
			w, _ := m.Meta["width"].(float64)
			h, _ := m.Meta["height"].(float64)
			if err = validateJPEG(m.Data, int(w), int(h)); err != nil {
				s.fail(err.Error())
				return
			}
			s.mu.Lock()
			s.revision++
			s.frame = &Frame{Data: m.Data, Width: int(w), Height: int(h), Revision: s.revision}
			if ack, _ := m.Meta["frameAck"].(bool); ack {
				s.acks[s.revision] = m.ID
			}
			tooMany := len(s.acks) > 4
			s.signalLocked()
			s.mu.Unlock()
			if tooMany {
				s.fail("对方超出画面流量窗口")
				return
			}
		case "stream_waiting", "stream_resumed":
			s.mu.Lock()
			s.frame = nil
			s.message = "等待远端新画面：" + m.Error
			s.signalLocked()
			s.mu.Unlock()
		case "input_error":
			s.mu.Lock()
			s.message = m.Error
			s.mu.Unlock()
		case "tiles":
			s.fail("对方未遵守完整帧协商，请升级软件")
			return
		}
	}
}

// NextFrame is pulled by a single decoding worker. Acknowledging only here
// bounds desktop capture by native consumer progress, not by UI event backlog.
func (s *Session) NextFrame(afterRevision int64, timeoutMillis int) (*Frame, error) {
	t := time.NewTimer(time.Duration(min(1000, max(1, timeoutMillis))) * time.Millisecond)
	defer t.Stop()
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, errors.New("连接已结束")
		}
		f := s.frame
		changed := s.changed
		var ids []string
		if f != nil && f.Revision > afterRevision {
			for rev, id := range s.acks {
				if rev <= f.Revision {
					ids = append(ids, id)
					delete(s.acks, rev)
				}
			}
			s.mu.Unlock()
			for _, id := range ids {
				if err := s.enqueue(protocol.Message{Kind: "event", Method: "frame_ack", ID: id}); err != nil {
					return nil, err
				}
			}
			return f, nil
		}
		s.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return nil, errors.New("连接已结束")
		case <-t.C:
			return nil, nil
		case <-changed:
		}
	}
}
func (s *Session) SendInputJSON(raw string) error {
	if err := validateInput([]byte(raw)); err != nil {
		return err
	}
	s.mu.Lock()
	allowed := s.control && !s.closed && s.frame != nil
	s.mu.Unlock()
	if !allowed {
		return errors.New("未允许远程操作或尚无画面")
	}
	return s.enqueue(protocol.Message{Kind: "event", Method: "input", Params: json.RawMessage(raw)})
}
func (s *Session) ReleaseInput() {
	_ = s.enqueue(protocol.Message{Kind: "event", Method: "input_release"})
}
func (s *Session) heartbeat() {
	for pause(s.ctx, 2*time.Second) {
		start := time.Now()
		if _, err := s.request("ping", nil, 8*time.Second); err != nil {
			s.fail("远端无响应，已断开")
			return
		}
		s.mu.Lock()
		s.rtt = time.Since(start).Milliseconds()
		s.mu.Unlock()
	}
}
