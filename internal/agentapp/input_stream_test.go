package agentapp

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestSlowOSInputDoesNotBlockPingOrCaptureGeometry(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	blocked, resume := make(chan struct{}), make(chan struct{})
	var entered, released sync.Once
	s := newInputSession()
	s.apply = func([]desktop.InputEvent) error { entered.Do(func() { close(blocked); <-resume }); return nil }
	a := &agent{id: secureconn.DeviceID(pub), privateKey: key, pin: "fixture-pin", allowControl: true, inputFactory: func() *inputSession { return s }}
	local, remote := net.Pipe()
	done := make(chan struct{})
	go func() { a.handle(local); close(done) }()
	defer func() {
		released.Do(func() { close(resume) })
		remote.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("input worker not joined")
		}
	}()
	_ = remote.SetDeadline(time.Now().Add(3 * time.Second))
	secured, err := secureconn.Connect(remote, a.id)
	if err != nil {
		t.Fatal(err)
	}
	c := protocol.NewConn(secured)
	if err = c.WriteMessage(protocol.Message{Kind: "request", Method: "auth", ID: "auth", Params: []byte(`{"pin":"fixture-pin"}`)}); err != nil {
		t.Fatal(err)
	}
	if m, err := c.ReadMessage(); err != nil || !m.OK {
		t.Fatal("auth", err)
	}
	if err = c.WriteMessage(inputMove(1)); err != nil {
		t.Fatal(err)
	}
	<-blocked
	geometry := make(chan struct{})
	go func() { s.geometry(1920, 1080); close(geometry) }()
	select {
	case <-geometry:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("input driver blocks capture geometry")
	}
	start := time.Now()
	_ = remote.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if err = c.WriteMessage(protocol.Message{Kind: "request", Method: "ping", ID: "ping"}); err != nil {
		t.Fatal("input driver blocks protocol reader", err)
	}
	if m, err := c.ReadMessage(); err != nil || m.ID != "ping" || !m.OK {
		t.Fatal("input driver blocks ping", err)
	}
	t.Logf("ping completed in %v while fake OS input was blocked; geometry remains independent", time.Since(start))
}

func TestInputContinuesWhileDownstreamResponseIsBlocked(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	observed := make(chan string, 8)
	a := &agent{id: secureconn.DeviceID(pub), privateKey: key, pin: "12345678", allowControl: true, inputFactory: func() *inputSession {
		s := newInputSession()
		s.apply = func(events []desktop.InputEvent) error {
			for _, e := range events {
				observed <- e.Type
			}
			return nil
		}
		return s
	}}
	local, remote := net.Pipe()
	defer remote.Close()
	done := make(chan struct{})
	go func() { a.handle(local); close(done) }()
	secured, err := secureconn.Connect(remote, a.id)
	if err != nil {
		t.Fatal(err)
	}
	c := protocol.NewConn(secured)
	if err := c.WriteMessage(protocol.Message{Kind: "request", Method: "auth", ID: "auth", Params: []byte(`{"pin":"12345678","mode":"control"}`)}); err != nil {
		t.Fatal(err)
	}
	if response, err := c.ReadMessage(); err != nil || !response.OK {
		t.Fatal("auth failed", err)
	}
	// Do not read the ping response: net.Pipe has no downstream buffer and
	// forces the Agent's writer to block. Input must nevertheless be applied.
	if err := c.WriteMessage(protocol.Message{Kind: "request", Method: "ping", ID: "ping"}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := c.WriteMessage(protocol.Message{Kind: "event", Method: "input", Params: []byte(`{"events":[{"type":"down","button":1},{"type":"move","x":20,"y":30},{"type":"up","button":1}]}`)}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"down", "move", "up"} {
		select {
		case got := <-observed:
			if got != want {
				t.Fatalf("order: %s != %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("input stalled behind downstream")
		}
	}
	t.Logf("ordered input applied with blocked downstream in %v (local in-memory link)", time.Since(start))
	_ = remote.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session failed to exit")
	}
}

func TestInputContinuesWhileFileWorkerWaitsForPermissionLock(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	observed := make(chan string, 8)
	a := &agent{id: secureconn.DeviceID(pub), privateKey: key, pin: "test-pin", allowControl: true, shareDir: t.TempDir(), inputFactory: func() *inputSession {
		s := newInputSession()
		s.apply = func(events []desktop.InputEvent) error { observed <- events[0].Type; return nil }
		return s
	}}
	local, remote := net.Pipe()
	defer remote.Close()
	_ = remote.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan struct{})
	go func() { a.handle(local); close(done) }()
	secured, err := secureconn.Connect(remote, a.id)
	if err != nil {
		t.Fatal(err)
	}
	c := protocol.NewConn(secured)
	if err := c.WriteMessage(protocol.Message{Kind: "request", ID: "auth", Method: "auth", Params: []byte(`{"pin":"test-pin","mode":"control"}`)}); err != nil {
		t.Fatal(err)
	}
	if m, err := c.ReadMessage(); err != nil || !m.OK {
		t.Fatal("auth", err)
	}
	// Deterministically stall file work (rather than hoping the test disk is
	// slow). The network reader must still execute ordered mouse input.
	a.fileMu.Lock()
	locked := true
	defer func() {
		if locked {
			a.fileMu.Unlock()
		}
	}()
	params, _ := json.Marshal(map[string]any{"path": "pending.bin", "size": 1})
	if err := c.WriteMessage(protocol.Message{Kind: "request", ID: "file", Method: "file_upload_begin", Params: params}); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(protocol.Message{Kind: "event", Method: "input", Params: []byte(`{"events":[{"type":"down","button":1},{"type":"move","x":32,"y":24},{"type":"up","button":1}]}`)}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"down", "move", "up"} {
		select {
		case got := <-observed:
			if got != want {
				t.Fatal("input order changed")
			}
		case <-time.After(time.Second):
			t.Fatal("file work blocked mouse")
		}
	}
	a.fileMu.Unlock()
	locked = false
	if m, err := c.ReadMessage(); err != nil || m.ID != "file" || !m.OK {
		t.Fatal("file did not resume", err)
	}
	_ = remote.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session did not exit")
	}
}
