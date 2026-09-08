package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/peerpath"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
	"github.com/yudesk/yudesk/internal/security"
)

// This fixture runs the real broker and encrypted wire protocols. Only the
// application auth decision is fake; no Agent/Viewer or desktop APIs are started.
type brokerPeerpathFixture struct {
	b       *broker
	ctx     context.Context
	address string
	id      string
	key     ed25519.PrivateKey
	dial    relay.DialOptions
	mu      sync.Mutex
	conns   []net.Conn
}

// Delay each broker-to-client write. Small application pings use one write per
// direction, adding approximately 2*delay to relay RTT on this machine only.
type brokerPeerpathDelayConn struct {
	net.Conn
	delay time.Duration
}

func (c *brokerPeerpathDelayConn) Write(p []byte) (int, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return c.Conn.Write(p)
}

func newBrokerPeerpathFixture(t *testing.T, useTLS bool, delay time.Duration, licensePeriod ...time.Duration) *brokerPeerpathFixture {
	t.Helper()
	database := filepath.Join(t.TempDir(), "peerpath.db")
	store, err := account.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := secureconn.DeviceID(pub)
	if err := store.RegisterLicensedDevice(id, pub); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantDeviceLicense(id, time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(licensePeriod) > 0 {
		// Only this disposable fixture gets a short deadline. The real grant
		// API correctly enforces its one-hour minimum and remains unchanged.
		db, err := sql.Open("sqlite", database)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec("UPDATE licensed_devices SET active_until=? WHERE device_id=?", time.Now().Add(licensePeriod[0]).Unix(), id)
		_ = db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f := &brokerPeerpathFixture{
		b: &broker{
			accounts: store, deviceLicenses: true,
			devices: map[string]waiting{}, active: map[string]activeSession{},
			stopRequests: map[string]string{},
		},
		ctx: ctx, id: id, key: key,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if useTLS {
		config, err := security.SelfSignedConfig("broker-peerpath-test")
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(config.Certificates[0].Certificate[0])
		f.dial = relay.DialOptions{TLS: true, Fingerprint: hex.EncodeToString(sum[:])}
		ln = tls.NewListener(ln, config)
	}
	f.address = ln.Addr().String()
	acceptDone := make(chan struct{})
	var handlers sync.WaitGroup
	go func() {
		defer close(acceptDone)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c = &brokerPeerpathDelayConn{Conn: c, delay: delay}
			f.track(c)
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				f.b.handle(c)
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		<-acceptDone
		f.mu.Lock()
		conns := append([]net.Conn(nil), f.conns...)
		f.mu.Unlock()
		for _, c := range conns {
			_ = c.SetDeadline(time.Now())
			_ = c.Close()
		}
		done := make(chan struct{})
		go func() { handlers.Wait(); close(done) }()
		brokerPeerpathAwait(t, "broker handler cleanup", done)
	})
	return f
}

func (f *brokerPeerpathFixture) track(c net.Conn) {
	f.mu.Lock()
	f.conns = append(f.conns, c)
	f.mu.Unlock()
}

func brokerPeerpathAwait[T any](t *testing.T, what string, done <-chan T) T {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(12 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

type brokerPeerpathResult struct {
	conn *protocol.Conn
	info peerpath.Info
	err  error
}

func (f *brokerPeerpathFixture) hello(role string) relay.Hello {
	h := relay.Hello{Role: role, ID: f.id}
	if role == "agent" {
		h.PublicKey = f.key.Public().(ed25519.PublicKey)
	}
	return h
}

// Index 0 is viewer, index 1 is agent throughout these tests.
func (f *brokerPeerpathFixture) authenticatedPair(t *testing.T, p2p bool) [2]*protocol.Conn {
	t.Helper()
	var pending [2]chan brokerPeerpathResult
	for i, role := range []string{"viewer", "agent"} {
		pending[i] = make(chan brokerPeerpathResult, 1)
		go func() {
			c, err := relay.DialWithContext(f.ctx, f.address, f.dial, f.hello(role))
			if err != nil {
				pending[i] <- brokerPeerpathResult{err: err}
				return
			}
			f.track(c)
			stop := context.AfterFunc(f.ctx, func() { _ = c.Close() })
			defer stop()
			var secured net.Conn
			if role == "agent" {
				secured, err = secureconn.Accept(c, f.key)
			} else {
				secured, err = secureconn.Connect(c, f.id)
			}
			if err != nil {
				pending[i] <- brokerPeerpathResult{err: err}
				return
			}
			pc := protocol.NewConn(secured)
			err = pc.SetDeadline(time.Now().Add(5 * time.Second))
			if err == nil {
				err = brokerPeerpathAuth(pc, role, p2p)
			}
			if err == nil {
				err = pc.SetDeadline(time.Time{})
			}
			pending[i] <- brokerPeerpathResult{conn: pc, err: err}
		}()
	}
	var pair [2]*protocol.Conn
	for i := range pair {
		r := brokerPeerpathAwait(t, "encrypted relay/application auth", pending[i])
		if r.err != nil {
			t.Fatalf("endpoint %d bootstrap: %v", i, r.err)
		}
		pair[i] = r.conn
	}
	f.b.Lock()
	active, ok := f.b.active[f.id]
	_, waiting := f.b.devices[f.id]
	f.b.Unlock()
	if !ok || waiting || active.agent == nil || active.viewer == nil || active.owner != "device:"+f.id {
		t.Fatal("licensed encrypted pair was not registered as the broker's active session")
	}
	return pair
}

func brokerPeerpathAuth(c *protocol.Conn, role string, p2p bool) error {
	const authID = "broker-peerpath-auth"
	if role == "viewer" {
		params, err := json.Marshal(map[string]bool{"p2pV1": p2p})
		if err != nil {
			return err
		}
		if err := c.WriteMessage(protocol.Message{Kind: "request", ID: authID, Method: "auth", Params: params}); err != nil {
			return err
		}
		m, err := c.ReadMessage()
		if err != nil {
			return err
		}
		accepted, ok := m.Meta["p2pV1"].(bool)
		if m.Kind != "response" || m.ID != authID || !m.OK || !ok || accepted != p2p {
			return errors.New("fake application auth did not acknowledge p2pV1")
		}
		return nil
	}
	m, err := c.ReadMessage()
	if err != nil {
		return err
	}
	var offered map[string]bool
	if m.Kind != "request" || m.ID != authID || m.Method != "auth" || json.Unmarshal(m.Params, &offered) != nil {
		return errors.New("invalid fake application auth request")
	}
	if value, ok := offered["p2pV1"]; !ok || value != p2p {
		return errors.New("fake application auth did not offer p2pV1")
	}
	return c.WriteMessage(protocol.Message{Kind: "response", ID: authID, OK: true, Meta: map[string]any{"p2pV1": p2p}})
}

func (f *brokerPeerpathFixture) negotiate(t *testing.T, bases [2]*protocol.Conn) [2]*protocol.Conn {
	t.Helper()
	var pending [2]chan brokerPeerpathResult
	for i, role := range []string{"viewer", "agent"} {
		pending[i] = make(chan brokerPeerpathResult, 1)
		go func() {
			c, info, err := peerpath.Negotiate(f.ctx, bases[i], role, nil, peerpath.Options{
				STUNURLs: []string{}, Timeout: 3 * time.Second,
			})
			pending[i] <- brokerPeerpathResult{c, info, err}
		}()
	}
	var pair [2]*protocol.Conn
	for i := range pair {
		r := brokerPeerpathAwait(t, "local UDP direct negotiation", pending[i])
		if r.conn != nil {
			t.Cleanup(func() { _ = r.conn.Close() })
		}
		if r.err != nil || r.info.Mode != "p2p" || r.info.Reason != "udp_direct" || r.conn == bases[i] {
			t.Fatalf("endpoint %d did not select UDP direct: mode=%q reason=%q err=%v", i, r.info.Mode, r.info.Reason, r.err)
		}
		pair[i] = r.conn
	}
	return pair
}

func brokerPeerpathPayload(direction, sequence, size int) []byte {
	p := make([]byte, size)
	for i := range p {
		p[i] = byte(i*31 + i/251 + sequence*17 + direction*103)
	}
	return p
}

func brokerPeerpathOrderedPayloads(t *testing.T, pair [2]*protocol.Conn) {
	t.Helper()
	// Cross data-channel and encrypted-record boundaries, exercise backpressure,
	// and finish with small/empty messages to expose truncation or reordering.
	sizes := []int{0, 1, 16383, 16384, 16385, 65537, 500003, 2 << 20, 257, 0}
	done := make(chan error, 4)
	for _, c := range pair {
		if err := c.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for direction := range pair {
		go func() {
			for sequence, size := range sizes {
				m := protocol.Message{Kind: "event", Method: "test-payload", ID: fmt.Sprintf("%d/%d", direction, sequence), Data: brokerPeerpathPayload(direction, sequence, size)}
				if err := pair[direction].WriteMessage(m); err != nil {
					done <- fmt.Errorf("direction %d write %d: %w", direction, sequence, err)
					return
				}
			}
			done <- nil
		}()
		go func() {
			for sequence, size := range sizes {
				m, err := pair[1-direction].ReadMessage()
				if err != nil {
					done <- fmt.Errorf("direction %d read %d: %w", direction, sequence, err)
					return
				}
				if m.Kind != "event" || m.Method != "test-payload" || m.ID != fmt.Sprintf("%d/%d", direction, sequence) || !bytes.Equal(m.Data, brokerPeerpathPayload(direction, sequence, size)) {
					done <- fmt.Errorf("direction %d message %d was reordered, truncated or corrupted (size=%d, want=%d)", direction, sequence, len(m.Data), size)
					return
				}
			}
			done <- nil
		}()
	}
	for range 4 {
		if err := brokerPeerpathAwait(t, "bidirectional ordered payloads", done); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range pair {
		if err := c.SetDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *brokerPeerpathFixture) assertEmpty(t *testing.T) {
	t.Helper()
	eventually(t, "broker devices/active cleanup", func() bool {
		f.b.Lock()
		defer f.b.Unlock()
		_, waiting := f.b.devices[f.id]
		_, active := f.b.active[f.id]
		return !waiting && !active
	})
}

func (f *brokerPeerpathFixture) assertDirectStops(t *testing.T, pair [2]*protocol.Conn, stop func()) [2]error {
	t.Helper()
	var reasons [2]error
	var reads [2]chan error
	for i, c := range pair {
		if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		reads[i] = make(chan error, 1)
		go func() { _, err := c.ReadMessage(); reads[i] <- err }()
	}
	stop()
	for i, c := range pair {
		err := brokerPeerpathAwait(t, "direct shutdown", reads[i])
		reasons[i] = err
		if !errors.Is(err, peerpath.ErrRelayLost) && !errors.Is(err, peerpath.ErrDirectLost) {
			t.Errorf("endpoint %d direct read must terminate with path loss, not a test deadline: %v", i, err)
		}
		// Check repeated writes too: the ended direct stream must not silently
		// continue or fall back to the already-revoked relay.
		_ = c.SetWriteDeadline(time.Now().Add(time.Second))
		for sequence := range 2 {
			err := c.WriteMessage(protocol.Message{Kind: "event", Method: "after-stop", Data: brokerPeerpathPayload(i, sequence, 1024)})
			if !errors.Is(err, peerpath.ErrRelayLost) && !errors.Is(err, peerpath.ErrDirectLost) {
				t.Errorf("endpoint %d direct write %d survived stop or only hit a deadline: %v", i, sequence, err)
			}
		}
	}
	if f.ctx.Err() != nil {
		t.Fatal("fixture cancellation masked server-initiated shutdown")
	}
	f.assertEmpty(t)
	return reasons
}

func TestBrokerPeerpathTerminateDevice(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		name := "tcp"
		if useTLS {
			name = "tls"
		}
		t.Run(name, func(t *testing.T) {
			f := newBrokerPeerpathFixture(t, useTLS, 0)
			pair := f.negotiate(t, f.authenticatedPair(t, true))
			brokerPeerpathOrderedPayloads(t, pair)
			const reason = "administrator stopped integration-test device"
			f.assertDirectStops(t, pair, func() {
				if !f.b.terminateDevice("device:"+f.id, f.id, reason) {
					t.Fatal("broker did not find the active P2P device")
				}
			})
			ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
			defer cancel()
			c, err := relay.DialWithContext(ctx, f.address, f.dial, f.hello("agent"))
			if c != nil {
				_ = c.Close()
				t.Fatal("next agent bypassed the pending stop")
			}
			var rejected *relay.RejectionError
			if !errors.As(err, &rejected) || rejected.Code != "STOP" || rejected.Message != reason {
				t.Fatalf("next agent did not receive the actual broker STOP: %v", err)
			}
			f.assertEmpty(t)
			f.b.Lock()
			_, pending := f.b.stopRequests[f.id]
			f.b.Unlock()
			if pending {
				t.Fatal("delivered one-shot stop was not consumed")
			}
			// A subsequent agent reaches WAIT, proving STOP was delivered exactly
			// once on the real hello path, rather than consumed by a test helper.
			nextCtx, nextCancel := context.WithCancel(f.ctx)
			defer nextCancel()
			next := make(chan error, 1)
			go func() {
				c, err := relay.DialWithContext(nextCtx, f.address, f.dial, f.hello("agent"))
				if c != nil {
					_ = c.Close()
				}
				next <- err
			}()
			eventually(t, "agent WAIT after consuming one-shot STOP", func() bool {
				f.b.Lock()
				defer f.b.Unlock()
				w, ok := f.b.devices[f.id]
				return ok && w.role == "agent"
			})
			nextCancel()
			if err := brokerPeerpathAwait(t, "cancel waiting agent", next); err == nil || relay.RejectionCode(err) != "" {
				t.Fatalf("subsequent agent was unexpectedly paired or rejected: %v", err)
			}
			f.assertEmpty(t)
		})
	}
}

func TestBrokerPeerpathManagementPathLoss(t *testing.T) {
	for _, side := range []string{"agent", "viewer"} {
		t.Run(side, func(t *testing.T) {
			f := newBrokerPeerpathFixture(t, true, 0)
			pair := f.negotiate(t, f.authenticatedPair(t, true))
			brokerPeerpathOrderedPayloads(t, pair)
			f.b.Lock()
			active := f.b.active[f.id]
			f.b.Unlock()
			f.assertDirectStops(t, pair, func() {
				c := active.agent
				if side == "viewer" {
					c = active.viewer
				}
				// Close only one server-side management/data leg. The real
				// broker proxy and peerpath tether must propagate this to both peers.
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestBrokerPeerpathNaturalLicenseExpiry(t *testing.T) {
	f := newBrokerPeerpathFixture(t, true, 0, 5*time.Second)
	pair := f.negotiate(t, f.authenticatedPair(t, true))
	expires, err := f.b.accounts.DeviceLicenseExpiry(f.id)
	if err != nil || !expires.After(time.Now()) {
		t.Fatal("fixture expired before P2P was established", err)
	}
	// No test-side cancellation or socket close: the real license deadline on
	// the broker's existing TLS connections must end both direct endpoints.
	reasons := f.assertDirectStops(t, pair, func() {})
	// The database expiry is wall time; net deadlines are converted to the
	// runtime monotonic clock. Virtualized clock synchronization reproduced a
	// 1.6ms discrepancy at expiry, not an authorization/path failure seconds
	// early. Keep a strict small tolerance and test new admission only after
	// that wall-clock boundary; never change the production license deadline.
	const clockTolerance = 10 * time.Millisecond
	if early := time.Until(expires); early > clockTolerance {
		t.Fatalf("session ended %s before its license deadline: %v", early, reasons)
	} else if early > 0 {
		t.Logf("wall/monotonic expiry boundary difference: %s", early)
		time.Sleep(early + clockTolerance)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	c, err := relay.DialWithContext(ctx, f.address, f.dial, f.hello("viewer"))
	if c != nil {
		_ = c.Close()
		t.Fatal("expired device accepted another connection")
	}
	if relay.RejectionCode(err) != "LICENSE_REQUIRED" {
		t.Fatalf("expired device had wrong rejection: %v", err)
	}
}

func brokerPeerpathPing(t *testing.T, pair [2]*protocol.Conn, samples int) time.Duration {
	t.Helper()
	const warmup = 2
	for _, c := range pair {
		if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		for range samples + warmup {
			m, err := pair[1].ReadMessage()
			if err == nil {
				m.Kind, m.OK = "response", true
				err = pair[1].WriteMessage(m)
			}
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	var elapsed time.Duration
	for i := range samples + warmup {
		id := fmt.Sprintf("ping-%d", i)
		payload := brokerPeerpathPayload(0, i, 64)
		start := time.Now()
		if err := pair[0].WriteMessage(protocol.Message{Kind: "request", ID: id, Method: "ping", Data: payload}); err != nil {
			t.Fatal(err)
		}
		m, err := pair[0].ReadMessage()
		rtt := time.Since(start)
		if err != nil || m.Kind != "response" || m.ID != id || m.Method != "ping" || !m.OK || !bytes.Equal(m.Data, payload) {
			t.Fatalf("application ping %d failed: %v", i, err)
		}
		if i >= warmup {
			elapsed += rtt
		}
	}
	if err := brokerPeerpathAwait(t, "ping responder", done); err != nil {
		t.Fatal(err)
	}
	return elapsed / time.Duration(samples)
}

func TestBrokerPeerpathLocalPingComparison(t *testing.T) {
	const samples = 8
	const delay = 25 * time.Millisecond
	means := map[string]time.Duration{}
	for _, mode := range []string{"relay", "p2p"} {
		t.Run(mode, func(t *testing.T) {
			f := newBrokerPeerpathFixture(t, true, delay)
			pair := f.authenticatedPair(t, mode == "p2p")
			if mode == "p2p" {
				pair = f.negotiate(t, pair)
			}
			means[mode] = brokerPeerpathPing(t, pair, samples)
		})
	}
	if len(means) == 2 {
		t.Logf("same-machine application ping (%d samples after 2 warmups, 64-byte payload, TLS broker writes delayed %s per direction): relay mean=%s, p2p mean=%s; synthetic local comparison, not a public-network RTT prediction", samples, delay, means["relay"], means["p2p"])
	}
}
