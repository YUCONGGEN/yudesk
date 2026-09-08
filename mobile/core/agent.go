package core

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/yudesk/yudesk/internal/peerpath"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
	"github.com/yudesk/yudesk/internal/stream"
)

func (e *Engine) agentLoop(ctx context.Context, generation uint64) {
	defer func() {
		e.mu.Lock()
		if generation == e.agentGeneration {
			e.agentCancel = nil
			e.state.Connected = false
			e.streaming = false
			e.canInput = false
			e.releaseLocked()
		}
		e.mu.Unlock()
	}()
	backoff := time.Second
	for ctx.Err() == nil {
		raw, err := relay.DialWithContext(ctx, relayAddress, relayOptions, relay.Hello{Role: "agent", ID: e.identity.ID, Name: e.name, PublicKey: e.identity.PrivateKey.Public().(ed25519.PublicKey)})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if relay.RejectionCode(err) != "" {
				e.stop("接收已停止：" + err.Error())
				return
			}
			if !pause(ctx, backoff) {
				return
			}
			backoff = min(30*time.Second, backoff*2)
			continue
		}
		backoff = time.Second
		e.serveAgent(ctx, raw)
		if !pause(ctx, 250*time.Millisecond) {
			return
		}
	}
}

type readResult struct {
	message protocol.Message
	err     error
}

func readMessages(ctx context.Context, c *protocol.Conn) <-chan readResult {
	ch := make(chan readResult, 8)
	go func() {
		defer close(ch)
		for {
			m, err := c.ReadMessage()
			select {
			case ch <- readResult{m, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}
func write(ctx context.Context, c *protocol.Conn, m protocol.Message) error {
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.WriteMessageContext(deadline, m)
}

func (e *Engine) checkPIN(pin string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	kept := e.failures[:0]
	for _, at := range e.failures {
		if now.Sub(at) < time.Minute {
			kept = append(kept, at)
		}
	}
	e.failures = kept
	if len(kept) >= 5 {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(pin), []byte(e.identity.PIN)) != 1 {
		e.failures = append(e.failures, now)
		return false
	}
	return true
}

func (e *Engine) serveAgent(parent context.Context, raw net.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	secured, err := secureconn.Accept(raw, e.identity.PrivateKey)
	if err != nil {
		return
	}
	c := protocol.NewConn(secured)
	first, err := c.ReadMessage()
	if err != nil {
		return
	}
	var auth struct {
		PIN          string `json:"pin"`
		Mode         string `json:"mode"`
		InterleaveV1 bool   `json:"interleaveV1"`
		P2PV1        bool   `json:"p2pV1"`
	}
	if first.Kind != "request" || first.Method != "auth" || json.Unmarshal(first.Params, &auth) != nil || (auth.Mode != "control" && auth.Mode != "view") {
		_ = write(ctx, c, protocol.Failure(first.ID, "无效认证请求"))
		return
	}
	_ = raw.SetDeadline(time.Time{})
	incoming := readMessages(ctx, c)
	if auth.PIN == "" {
		approvalCtx, stopApproval := context.WithCancel(ctx)
		defer stopApproval()
		decision := make(chan error, 1)
		go func() { decision <- e.approvals.Request(approvalCtx, auth.Mode) }()
		select {
		case err = <-decision:
		case <-incoming:
			stopApproval()
			<-decision
			return
		case <-ctx.Done():
			stopApproval()
			<-decision
			return
		}
		if err != nil {
			_ = write(ctx, c, protocol.Failure(first.ID, err.Error()))
			return
		}
	} else if !e.checkPIN(auth.PIN) {
		_ = write(ctx, c, protocol.Failure(first.ID, "PIN 错误或尝试过于频繁"))
		return
	}
	e.mu.Lock()
	allowed := e.permittedLocked() && e.state.Sharing
	control := auth.Mode == "control" && e.state.Accessibility
	var epoch uint64
	if allowed {
		e.incomingSession++
		epoch = e.incomingSession
		e.state.Connected = true
		e.canInput = control
		e.frame = nil
	}
	e.mu.Unlock()
	if !allowed {
		_ = write(ctx, c, protocol.Failure(first.ID, "设备未授权或已停止共享"))
		return
	}
	defer func() {
		e.mu.Lock()
		if e.incomingSession == epoch {
			e.state.Connected = false
			e.canInput = false
			e.streaming = false
			e.frame = nil
			e.releaseLocked()
		}
		e.mu.Unlock()
	}()
	if auth.InterleaveV1 {
		c.EnableInterleaving()
	}
	if write(ctx, c, protocol.Response(first.ID, nil, map[string]any{"id": e.identity.ID, "platform": "android", "control": control, "audio": false, "audioOnDemand": true, "audioReason": "Android 预览版暂不支持系统声音", "tileDeltaV1": false, "inputEventsV1": true, "fileTransferV2": false, "interleaveV1": auth.InterleaveV1, "p2pV1": auth.P2PV1})) != nil {
		return
	}
	if auth.P2PV1 {
		base, messages := c, incoming
		read := func() (protocol.Message, error) {
			select {
			case r, ok := <-messages:
				if !ok {
					return protocol.Message{}, net.ErrClosed
				}
				return r.message, r.err
			case <-ctx.Done():
				return protocol.Message{}, ctx.Err()
			}
		}
		options := peerpath.DefaultOptions()
		if e.peerOptions != nil {
			options = *e.peerOptions
		}
		next, _, err := peerpath.Negotiate(ctx, base, "agent", read, options)
		if err != nil {
			return
		}
		c = next
		if auth.InterleaveV1 {
			c.EnableInterleaving()
		}
		if next != base {
			incoming = readMessages(ctx, next)
		}
	}
	defer c.Close()
	responses := make(chan protocol.Message, 16)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-responses:
				if write(ctx, c, m) != nil {
					cancel()
					return
				}
			}
		}
	}()
	reply := func(m protocol.Message) bool {
		select {
		case responses <- m:
			return true
		default:
			cancel()
			return false
		}
	}
	acks := make(chan string, 8)
	var stopStream context.CancelFunc
	var streamDone chan struct{}
	finishStream := func() {
		if stopStream != nil {
			stopStream()
			<-streamDone
			stopStream = nil
		}
		e.mu.Lock()
		if e.incomingSession == epoch {
			e.streaming = false
			e.frame = nil
		}
		e.mu.Unlock()
	}
	defer finishStream()
	for {
		var r readResult
		select {
		case <-ctx.Done():
			return
		case message := <-e.inputErrors:
			if !reply(protocol.Message{Kind: "event", Method: "input_error", Error: message}) {
				return
			}
			continue
		case r = <-incoming:
		}
		if r.err != nil || r.message.Kind == "" {
			return
		}
		m := r.message
		e.mu.Lock()
		permitted := ctx.Err() == nil && e.incomingSession == epoch && e.permittedLocked() && e.state.Sharing
		e.mu.Unlock()
		if !permitted {
			return
		}
		if m.Kind == "event" && m.Method == "frame_ack" {
			select {
			case acks <- m.ID:
			default:
			}
			continue
		}
		if m.Method == "input_release" {
			e.mu.Lock()
			e.releaseLocked()
			e.mu.Unlock()
			if m.Kind == "request" && !reply(protocol.Response(m.ID, nil, nil)) {
				return
			}
			continue
		}
		if m.Method == "input" {
			err = e.queueInputForSession(m.Params, control, epoch)
			if m.Kind == "request" {
				if err != nil {
					if !reply(protocol.Failure(m.ID, err.Error())) {
						return
					}
				} else if !reply(protocol.Response(m.ID, nil, nil)) {
					return
				}
			} else if err != nil {
				if !reply(protocol.Message{Kind: "event", Method: "input_error", Error: err.Error()}) {
					return
				}
			}
			continue
		}
		if m.Kind != "request" {
			continue
		}
		switch m.Method {
		case "ping":
			if !reply(protocol.Response(m.ID, nil, nil)) {
				return
			}
		case "info":
			if !reply(protocol.Response(m.ID, nil, map[string]any{"name": e.name, "platform": "android"})) {
				return
			}
		case "stream_start":
			var opts stream.Options
			if json.Unmarshal(m.Params, &opts) != nil {
				if !reply(protocol.Failure(m.ID, "无效画面设置")) {
					return
				}
				continue
			}
			opts = opts.Normalized()
			opts.FPS = min(opts.FPS, 30)
			opts.TileDelta = false
			finishStream()
			c.SetBulkRate(opts.MaxMbps * 1_000_000 / 8)
			streamCtx, sc := context.WithCancel(ctx)
			stopStream = sc
			streamDone = make(chan struct{})
			e.mu.Lock()
			e.streaming = true
			e.mu.Unlock()
			if !reply(protocol.Response(m.ID, nil, map[string]any{"fps": opts.FPS, "quality": 75})) {
				close(streamDone)
				return
			}
			go func(done chan struct{}, options stream.Options, generation string) {
				defer close(done)
				e.sendFrames(streamCtx, ctx, c, acks, options, generation)
			}(streamDone, opts, m.ID)
		case "stream_stop":
			finishStream()
			if !reply(protocol.Response(m.ID, nil, nil)) {
				return
			}
		default:
			if !reply(protocol.Failure(m.ID, "Android 预览版暂不支持此功能")) {
				return
			}
		}
	}
}

func (e *Engine) sendFrames(ctx, writeCtx context.Context, c *protocol.Conn, acks <-chan string, options stream.Options, generation string) {
	var last int64
	pending := map[string]bool{}
	next := time.Now()
	for ctx.Err() == nil {
		if len(pending) >= 2 {
			timer := time.NewTimer(10 * time.Second)
			for len(pending) >= 2 {
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
					_ = c.Close()
					return
				case id := <-acks:
					delete(pending, id)
				}
			}
			timer.Stop()
		}
		draining := true
		for draining {
			select {
			case id := <-acks:
				delete(pending, id)
			default:
				draining = false
			}
		}
		if !pause(ctx, max(time.Duration(0), time.Until(next))) {
			return
		}
		e.mu.Lock()
		f := e.frame
		e.mu.Unlock()
		if f == nil || f.Revision == last {
			select {
			case <-ctx.Done():
				return
			case <-e.frameWake:
			}
			continue
		}
		id := fmt.Sprintf("%s:%d", generation, f.Revision)
		started := time.Now()
		// Stream settings may change while a paced frame is in flight. Finish
		// that bounded write using the session context, rather than leaving a
		// partial encrypted message and breaking the still-authorized session.
		if write(writeCtx, c, protocol.Message{Kind: "event", Method: "frame", ID: id, Data: f.Data, Meta: map[string]any{"width": f.Width, "height": f.Height, "sourceWidth": f.Width, "sourceHeight": f.Height, "frameAck": options.FrameAck, "targetFPS": options.FPS, "quality": 75}}) != nil {
			return
		}
		last = f.Revision
		if options.FrameAck {
			pending[id] = true
		}
		interval := time.Second / time.Duration(options.FPS)
		if options.MaxMbps > 0 {
			interval = max(interval, time.Duration(float64(len(f.Data)*8)/float64(options.MaxMbps*1000000)*float64(time.Second)))
		}
		next = started.Add(interval)
	}
}

type inputEvent struct {
	Type   string `json:"type"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Button int    `json:"button"`
	Key    string `json:"key,omitempty"`
	Code   string `json:"code,omitempty"`
	Text   string `json:"text,omitempty"`
	DeltaY int    `json:"deltaY,omitempty"`
}
type inputBatch struct {
	Events []inputEvent `json:"events"`
	Width  int          `json:"width"`
	Height int          `json:"height"`
}

func validateInput(raw []byte) error {
	if len(raw) > 16<<10 {
		return errors.New("输入事件过大")
	}
	var b inputBatch
	if json.Unmarshal(raw, &b) != nil || len(b.Events) == 0 || len(b.Events) > 64 || b.Width < 1 || b.Height < 1 || b.Width > 8192 || b.Height > 8192 {
		return errors.New("无效输入事件")
	}
	for _, ev := range b.Events {
		switch ev.Type {
		case "move", "down", "up":
			if ev.X < 0 || ev.Y < 0 || ev.X >= b.Width || ev.Y >= b.Height {
				return errors.New("输入坐标超出画面")
			}
			if ev.Type != "move" && (ev.Button < 1 || ev.Button > 3) {
				return errors.New("无效鼠标按钮")
			}
		case "key_down", "key_up", "text", "wheel":
			if len(ev.Text) > 4096 || len(ev.Key) > 64 || len(ev.Code) > 64 {
				return errors.New("输入文本过长")
			}
		default:
			return errors.New("不支持的输入事件")
		}
	}
	return nil
}
func (e *Engine) queueInput(raw []byte, control bool) error {
	return e.queueInputForSession(raw, control, 0)
}
func (e *Engine) queueInputForSession(raw []byte, control bool, epoch uint64) error {
	if err := validateInput(raw); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if (epoch != 0 && epoch != e.incomingSession) || !control || !e.canInput || !e.state.Accessibility || !e.permittedLocked() || !e.state.Sharing {
		return errors.New("仅观看或未开启无障碍控制权限")
	}
	select {
	case e.input <- string(raw):
		return nil
	default:
		e.releaseLocked()
		return errors.New("输入队列已满，操作已释放，请重试")
	}
}

// NextInputJSON waits on a dedicated native input thread; never blocks capture.
func (e *Engine) NextInputJSON(timeoutMillis int) string {
	t := time.NewTimer(time.Duration(min(1000, max(1, timeoutMillis))) * time.Millisecond)
	defer t.Stop()
	select {
	case s := <-e.input:
		return s
	case <-t.C:
		return ""
	case <-e.ctx.Done():
		return `{"release":true}`
	}
}
