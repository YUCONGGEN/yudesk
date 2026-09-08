package peerpath

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
)

type instrumentedConn struct {
	net.Conn
	reads        atomic.Int32
	rx, tx       atomic.Int64
	drop         atomic.Bool
	mu           sync.Mutex
	readDeadline time.Time
}

func (c *instrumentedConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	defer c.reads.Add(-1)
	n, err := c.Conn.Read(p)
	c.rx.Add(int64(n))
	return n, err
}

func (c *instrumentedConn) Write(p []byte) (int, error) {
	if c.drop.Load() {
		return len(p), nil
	}
	n, err := c.Conn.Write(p)
	c.tx.Add(int64(n))
	return n, err
}

func (c *instrumentedConn) SetDeadline(d time.Time) error {
	c.mu.Lock()
	c.readDeadline = d
	c.mu.Unlock()
	return c.Conn.SetDeadline(d)
}

func (c *instrumentedConn) SetReadDeadline(d time.Time) error {
	c.mu.Lock()
	c.readDeadline = d
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(d)
}

func packet(t *testing.T, m protocol.Message) []byte {
	t.Helper()
	h, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := make([]byte, 8)
	binary.BigEndian.PutUint32(p, uint32(len(h)))
	binary.BigEndian.PutUint32(p[4:], uint32(len(m.Data)))
	p = append(p, h...)
	return append(p, m.Data...)
}

func TestAuthenticateLegacyAndExplicitDisablePreserveBufferedMessages(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "disabled"}[disabled], func(t *testing.T) {
			a, b := encryptedPair(t)
			base := protocol.NewConn(b)
			var wrapper *instrumentedConn
			o := hostOptions()
			o.Wrap = func(c net.Conn) net.Conn { wrapper = &instrumentedConn{Conn: c}; return wrapper }
			params := map[string]any{"pin": "123456"}
			if disabled {
				params["p2pV1"] = false
			}
			done := make(chan error, 1)
			go func() {
				req, err := base.ReadMessage()
				if err != nil {
					done <- err
					return
				}
				var offered map[string]any
				_ = json.Unmarshal(req.Params, &offered)
				if offered["p2pV1"] != !disabled || offered["interleaveV1"] != true || offered["pin"] != "123456" {
					done <- errors.New("incorrect auth params")
					return
				}
				// Even a peer advertising p2pV1 must respect a viewer's explicit opt-out.
				response := protocol.Response(req.ID, nil, map[string]any{"p2pV1": disabled, "interleaveV1": true, "platform": "old-agent"})
				wire := append(packet(t, response), packet(t, protocol.Message{Kind: "event", Method: "prefetched", Data: []byte{1, 2, 3}})...)
				_, err = b.Write(wire)
				done <- err
			}()
			pc, auth, info, err := Authenticate(context.Background(), a, params, o)
			if err != nil {
				t.Fatal(err)
			}
			defer pc.Close()
			if info.Mode != "relay" || auth.Meta["platform"] != "old-agent" || pc.Conn != wrapper {
				t.Fatalf("auth handoff: %+v %+v", info, auth)
			}
			if err = await(t, done); err != nil {
				t.Fatal(err)
			}
			m, err := pc.ReadMessage()
			if err != nil || m.Method != "prefetched" || len(m.Data) != 3 {
				t.Fatalf("lost buffered message: %+v %v", m, err)
			}
			if wrapper.rx.Load() == 0 || wrapper.tx.Load() == 0 {
				t.Fatal("Wrap did not measure relay")
			}
			if _, exists := params["interleaveV1"]; exists {
				t.Fatal("mutated caller params")
			}
			if !disabled {
				if _, exists := params["p2pV1"]; exists {
					t.Fatal("mutated caller params")
				}
			}
		})
	}
}

func TestAuthenticateRejectsInvalidResponse(t *testing.T) {
	for _, field := range []string{"kind", "id", "ok"} {
		t.Run(field, func(t *testing.T) {
			a, b := encryptedPair(t)
			base := protocol.NewConn(b)
			done := make(chan error, 1)
			go func() {
				m, err := base.ReadMessage()
				if err != nil {
					done <- err
					return
				}
				m = protocol.Response(m.ID, nil, map[string]any{"p2pV1": true})
				switch field {
				case "kind":
					m.Kind = "event"
				case "id":
					m.ID = "wrong"
				case "ok":
					m.OK = false
					m.Error = "bad PIN"
				}
				if err = base.WriteMessage(m); err == nil {
					_, err = base.ReadMessage()
				}
				done <- err
			}()
			pc, _, info, err := Authenticate(context.Background(), a, nil, hostOptions())
			if err == nil || pc != nil || info != (Info{}) {
				t.Fatalf("accepted invalid auth: %v %+v", err, info)
			}
			if err = await(t, done); err == nil {
				t.Fatal("invalid auth did not close base")
			}
		})
	}
}

func TestAuthenticateCancellationHasNoReaderLeft(t *testing.T) {
	a, b := encryptedPair(t)
	tracked := &instrumentedConn{Conn: a}
	base := protocol.NewConn(b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, _, err := Authenticate(ctx, tracked, nil, hostOptions()); done <- err }()
	if _, err := base.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := await(t, done); err == nil {
		t.Fatal("cancelled auth succeeded")
	}
	if n := tracked.reads.Load(); n != 0 {
		t.Fatalf("%d auth readers leaked", n)
	}
	if _, err := base.ReadMessage(); err == nil {
		t.Fatal("cancelled auth left relay open")
	}
}

func TestAuthenticateDirectLifetimeWrapAndSilentTether(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises real tether timeout")
	}
	a, b := encryptedPair(t)
	viewerBase, agentBase := &instrumentedConn{Conn: a}, &instrumentedConn{Conn: b}
	agent := protocol.NewConn(agentBase)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan outcome, 1)
	go func() {
		req, err := agent.ReadMessage()
		if err != nil {
			done <- outcome{err: err}
			return
		}
		agent.EnableInterleaving()
		if err = agent.WriteMessage(protocol.Response(req.ID, nil, map[string]any{"p2pV1": true, "interleaveV1": true})); err != nil {
			done <- outcome{err: err}
			return
		}
		pc, info, err := Negotiate(ctx, agent, "agent", nil, hostOptions())
		if pc != nil {
			pc.EnableInterleaving()
		}
		done <- outcome{pc, info, err}
	}()
	var wrappers []*instrumentedConn
	o := hostOptions()
	o.Wrap = func(c net.Conn) net.Conn {
		w := &instrumentedConn{Conn: c}
		wrappers = append(wrappers, w)
		return w
	}
	pc, _, info, err := Authenticate(ctx, viewerBase, nil, o)
	if err != nil || info.Mode != "p2p" {
		t.Fatalf("auth direct: %+v %v", info, err)
	}
	defer pc.Close()
	peer := await(t, done)
	if peer.err != nil || peer.info != info {
		t.Fatalf("agent: %+v", peer)
	}
	defer peer.c.Close()
	if len(wrappers) != 2 || pc.Conn != wrappers[1] {
		t.Fatal("Wrap must wrap relay and final direct once each")
	}
	session := wrappers[1].Conn.(*sessionConn)
	// Authenticate has already canceled its private auth context; now exceed
	// both the test ICE budget and the production 3-second budget as well.
	select {
	case <-session.done:
		t.Fatalf("bootstrap context leaked into lifetime: %v", session.terminal())
	case <-time.After(3200 * time.Millisecond):
	}
	roundTrip(t, pc, peer.c, 200_007)
	roundTrip(t, peer.c, pc, 19_999)
	for i, wrapper := range wrappers {
		if wrapper.rx.Load() == 0 || wrapper.tx.Load() == 0 {
			t.Fatalf("path %d not counted", i)
		}
	}
	viewerBase.mu.Lock()
	deadline := viewerBase.readDeadline
	viewerBase.mu.Unlock()
	if deadline.IsZero() {
		t.Fatal("auth cleanup cleared live tether deadline")
	}
	// Simulate a silently blackholed relay while UDP remains healthy. Successful
	// local writes cannot mask lack of authenticated inbound management traffic.
	viewerBase.drop.Store(true)
	agentBase.drop.Store(true)
	await(t, session.closed)
	if _, err := pc.Conn.Read(make([]byte, 1)); !errors.Is(err, ErrRelayLost) {
		t.Fatalf("silent tether not terminal: %v", err)
	}
}
