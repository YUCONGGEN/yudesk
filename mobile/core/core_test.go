package core

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/approval"
	"github.com/yudesk/yudesk/internal/identity"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	id, err := identity.Load(t.TempDir(), "123456")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{ctx: ctx, cancel: cancel, identity: id, name: "test Android", state: status{Running: true, Online: true, Active: true, Sharing: true, Accessibility: true}, frameWake: make(chan struct{}, 1), input: make(chan string, 64), inputErrors: make(chan string, 8), changed: make(chan struct{}), approvals: approval.New()}
	t.Cleanup(e.Close)
	return e
}
func jpegData(t *testing.T, w, h int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	im.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func until(t *testing.T, condition func() bool) {
	t.Helper()
	end := time.Now().Add(2 * time.Second)
	for time.Now().Before(end) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func rawPeer(t *testing.T, e *Engine) (*protocol.Conn, <-chan struct{}) {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { e.serveAgent(e.ctx, a); close(done) }()
	_ = b.SetDeadline(time.Now().Add(5 * time.Second))
	c, err := secureconn.Connect(b, e.identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("agent goroutine leaked")
		}
	})
	return protocol.NewConn(c), done
}
func authMessage(pin, mode string) protocol.Message {
	p, _ := json.Marshal(map[string]any{"pin": pin, "mode": mode})
	return protocol.Message{Kind: "request", ID: "auth1", Method: "auth", Params: p}
}

func TestPINApprovalNotFallback(t *testing.T) {
	e := testEngine(t)
	c, _ := rawPeer(t, e)
	if err := c.WriteMessage(authMessage("654321", "control")); err != nil {
		t.Fatal(err)
	}
	r, err := c.ReadMessage()
	if err != nil || r.OK {
		t.Fatalf("wrong PIN allowed: %v %+v", err, r)
	}
	if e.approvals.Pending() != nil {
		t.Fatal("wrong PIN became local approval")
	}
}
func TestPINRateLimit(t *testing.T) {
	e := testEngine(t)
	for i := 0; i < 5; i++ {
		if e.checkPIN("000000") {
			t.Fatal("bad PIN accepted")
		}
	}
	if e.checkPIN("123456") {
		t.Fatal("rate limiter bypassed")
	}
}
func TestEmptyPINApprovalAndOneUse(t *testing.T) {
	e := testEngine(t)
	c, _ := rawPeer(t, e)
	if err := c.WriteMessage(authMessage("", "view")); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { return e.approvals.Pending() != nil })
	p := e.approvals.Pending()
	if p.Mode != "view" || time.Until(p.Deadline) < 58*time.Second {
		t.Fatal("missing sixty second view approval")
	}
	if e.WantsFrame() || e.CanInput() {
		t.Fatal("capability before approval")
	}
	if err := e.ResolveApproval("wrong", true); err == nil {
		t.Fatal("wrong request accepted")
	}
	if err := e.ResolveApproval(p.ID, true); err != nil {
		t.Fatal(err)
	}
	r, err := c.ReadMessage()
	if err != nil || !r.OK {
		t.Fatalf("approval failed: %v %+v", err, r)
	}
	if r.Meta["control"] != false {
		t.Fatal("view mode got control")
	}
	if e.ResolveApproval(p.ID, true) == nil {
		t.Fatal("approval replayed")
	}
}
func TestDeclineAndDisconnectCancelApproval(t *testing.T) {
	for _, closePeer := range []bool{false, true} {
		t.Run(map[bool]string{false: "deny", true: "disconnect"}[closePeer], func(t *testing.T) {
			e := testEngine(t)
			c, done := rawPeer(t, e)
			if err := c.WriteMessage(authMessage("", "control")); err != nil {
				t.Fatal(err)
			}
			until(t, func() bool { return e.approvals.Pending() != nil })
			if closePeer {
				c.Close()
			} else {
				_ = e.ResolveApproval(e.approvals.Pending().ID, false)
				r, err := c.ReadMessage()
				if err != nil || r.OK {
					t.Fatalf("denial failed %v", err)
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("approval did not cancel")
			}
			if e.approvals.Pending() != nil || e.CanInput() {
				t.Fatal("stale permission")
			}
		})
	}
}
func TestSyntheticTwoWaySession(t *testing.T) {
	e := testEngine(t)
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { e.serveAgent(e.ctx, a); close(done) }()
	secured, err := secureconn.Connect(b, e.identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSession(e.ctx, secured, "123456", true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !e.WantsFrame() {
		t.Fatal("stream not enabled")
	}
	for i := 0; i < 3; i++ {
		if err = e.SubmitJPEG(jpegData(t, 320, 240), 320, 240); err != nil {
			t.Fatal(err)
		}
	}
	f, err := s.NextFrame(0, 1000)
	if err != nil || f == nil || f.Width != 320 {
		t.Fatalf("frame failed %v %+v", err, f)
	}
	input := `{"events":[{"type":"down","x":100,"y":50,"button":1},{"type":"up","x":100,"y":50,"button":1}],"width":320,"height":240}`
	if err = s.SendInputJSON(input); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { return len(e.input) > 0 })
	if got := e.NextInputJSON(10); got != input {
		t.Fatalf("input altered: %s", got)
	}
	e.SetAccessibility(false)
	if e.CanInput() {
		t.Fatal("permission revocation ignored")
	}
	if e.queueInput([]byte(input), true) == nil {
		t.Fatal("input allowed without accessibility")
	}
	e.stop("DISABLED")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled receiver remained connected")
	}
	if e.WantsFrame() {
		t.Fatal("capture after disable")
	}
}
func TestFrameSafetyAndLatestOnly(t *testing.T) {
	e := testEngine(t)
	data := jpegData(t, 64, 48)
	if validateJPEG(data, 65, 48) == nil || validateJPEG(data, 9000, 1) == nil || validateJPEG([]byte("garbage"), 1, 1) == nil {
		t.Fatal("unsafe image accepted")
	}
	if err := e.SubmitJPEG(data, 64, 48); err != nil {
		t.Fatal(err)
	}
	if e.frame != nil {
		t.Fatal("cached image before approval/stream")
	}
	e.streaming = true
	for i := 0; i < 100; i++ {
		if err := e.SubmitJPEG(data, 64, 48); err != nil {
			t.Fatal(err)
		}
	}
	if e.frame.Revision != 100 || len(e.frameWake) != 1 {
		t.Fatal("unbounded frame queue")
	}
	data[0] = 0
	if e.frame.Data[0] == 0 {
		t.Fatal("caller mutated live frame")
	}
}
func TestInputBoundsAndViewMode(t *testing.T) {
	e := testEngine(t)
	good := []byte(`{"events":[{"type":"down","x":1,"y":1,"button":1}],"width":2,"height":2}`)
	if err := validateInput(good); err != nil {
		t.Fatal(err)
	}
	if e.queueInput(good, false) == nil {
		t.Fatal("view input permitted")
	}
	for _, s := range []string{`{"events":[{"type":"down","x":9,"y":1,"button":1}],"width":2,"height":2}`, `{"events":[{"type":"shell"}],"width":2,"height":2}`, `{"events":[],"width":2,"height":2}`} {
		if validateInput([]byte(s)) == nil {
			t.Fatal("unsafe input accepted")
		}
	}
}
func TestLicenseExpiryWhileOffline(t *testing.T) {
	e := testEngine(t)
	e.mu.Lock()
	e.state.Online = false
	e.state.ActiveUntil = time.Now().Add(30 * time.Millisecond)
	e.mu.Unlock()
	go e.enforceExpiry()
	select {
	case <-e.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("offline license did not expire")
	}
	if e.CanInput() || e.WantsFrame() {
		t.Fatal("expiry retained capability")
	}
}
func TestManagementOfflineStopsCapabilities(t *testing.T) {
	e := testEngine(t)
	e.canInput = true
	e.streaming = true
	e.mu.Lock()
	e.state.Online = false
	e.reconcileLocked()
	e.mu.Unlock()
	if e.CanInput() || e.WantsFrame() {
		t.Fatal("unverified license remained usable")
	}
}

func TestStaleSessionCannotInject(t *testing.T) {
	e := testEngine(t)
	e.canInput = true
	e.incomingSession = 2
	raw := []byte(`{"events":[{"type":"down","x":1,"y":1,"button":1}],"width":2,"height":2}`)
	if e.queueInputForSession(raw, true, 1) == nil {
		t.Fatal("previous session injected into new session")
	}
	if err := e.queueInputForSession(raw, true, 2); err != nil {
		t.Fatal(err)
	}
}

func TestSlowRendererDoesNotBlockIncomingInput(t *testing.T) {
	e := testEngine(t)
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { e.serveAgent(e.ctx, a); close(done) }()
	secured, err := secureconn.Connect(b, e.identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSession(e.ctx, secured, "123456", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		s.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("stream did not stop")
		}
	}()
	data := jpegData(t, 320, 240)
	for i := 0; i < 10; i++ {
		if err = e.SubmitJPEG(data, 320, 240); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	until(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.frame != nil })
	// Do not call NextFrame: the receiver stops at its bounded ACK window.
	raw := `{"events":[{"type":"move","x":5,"y":5}],"width":320,"height":240}`
	if err = s.SendInputJSON(raw); err != nil {
		t.Fatal(err)
	}
	until(t, func() bool { return len(e.input) > 0 })
	if e.NextInputJSON(10) != raw {
		t.Fatal("input blocked behind unacknowledged video")
	}
}

type delayedCloseConn struct {
	net.Conn
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *delayedCloseConn) Close() error {
	c.once.Do(func() { close(c.called) })
	<-c.release
	return c.Conn.Close()
}
func TestCloseNeverBlocksUICallerOnTransportShutdown(t *testing.T) {
	e := testEngine(t)
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { e.serveAgent(e.ctx, a); close(done) }()
	secured, err := secureconn.Connect(b, e.identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	transport := &delayedCloseConn{Conn: secured, called: make(chan struct{}), release: make(chan struct{})}
	s, err := openSession(e.ctx, transport, "123456", true)
	if err != nil {
		close(transport.release)
		t.Fatal(err)
	}
	returned := make(chan struct{})
	go func() { s.Close(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(300 * time.Millisecond):
		close(transport.release)
		t.Fatal("Close blocked the UI caller on network shutdown")
	}
	select {
	case <-transport.called:
	case <-time.After(time.Second):
		close(transport.release)
		t.Fatal("transport cancellation was not scheduled")
	}
	close(transport.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transport did not finish shutting down")
	}
}
