package peerpath

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/secureconn"
)

type outcome struct {
	c    *protocol.Conn
	info Info
	err  error
}

func hostOptions() Options {
	return Options{STUNURLs: []string{}, Timeout: 2 * time.Second, configure: func(s *webrtc.SettingEngine) { s.SetIncludeLoopbackCandidate(true) }}
}

func TestLocalSDPIsValid(t *testing.T) {
	a, err := newAttempt(hostOptions(), 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	raw, reason := a.description(context.Background(), true, 200*time.Millisecond)
	if raw == "" {
		t.Fatalf("SDP generation %s: %v\n%s", reason, validateSDP(a.pc.LocalDescription().SDP), a.pc.LocalDescription().SDP)
	}
	if err := validateSDP(raw); err != nil {
		t.Fatal(err)
	}
}
func noUDPOptions() Options {
	o := hostOptions()
	o.configure = func(s *webrtc.SettingEngine) { s.SetIPFilter(func(net.IP) bool { return false }) }
	return o
}

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(12 * time.Second):
		t.Fatal("operation did not finish")
	}
	var zero T
	return zero
}

func encryptedPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	left, right := net.Pipe()
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	_ = left.SetDeadline(time.Now().Add(5 * time.Second))
	_ = right.SetDeadline(time.Now().Add(5 * time.Second))
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	type accepted struct {
		conn net.Conn
		err  error
	}
	ch := make(chan accepted, 1)
	go func() { c, err := secureconn.Accept(right, priv); ch <- accepted{c, err} }()
	viewer, err := secureconn.Connect(left, secureconn.DeviceID(pub))
	if err != nil {
		t.Fatal(err)
	}
	agent := await(t, ch)
	if agent.err != nil {
		t.Fatal(agent.err)
	}
	_ = viewer.SetDeadline(time.Time{})
	_ = agent.conn.SetDeadline(time.Time{})
	return viewer, agent.conn
}

func runPair(t *testing.T, ctx context.Context, bases [2]*protocol.Conn, options [2]Options, readers [2]func() (protocol.Message, error)) [2]outcome {
	t.Helper()
	var channels [2]chan outcome
	for i, role := range []string{"viewer", "agent"} {
		channels[i] = make(chan outcome, 1)
		go func() {
			c, info, err := Negotiate(ctx, bases[i], role, readers[i], options[i])
			channels[i] <- outcome{c, info, err}
		}()
	}
	var results [2]outcome
	for i := range results {
		results[i] = await(t, channels[i])
		if results[i].c != nil {
			c := results[i].c
			t.Cleanup(func() { _ = c.Close() })
		}
	}
	return results
}

func directPair(t *testing.T, ctx context.Context) ([2]*protocol.Conn, [2]*protocol.Conn) {
	t.Helper()
	a, b := encryptedPair(t)
	bases := [2]*protocol.Conn{protocol.NewConn(a), protocol.NewConn(b)}
	r := runPair(t, ctx, bases, [2]Options{hostOptions(), hostOptions()}, [2]func() (protocol.Message, error){})
	for _, result := range r {
		if result.err != nil || result.info.Mode != "p2p" {
			t.Fatalf("direct negotiation: %+v", result)
		}
	}
	if r[0].info != r[1].info {
		t.Fatal("decisions differ")
	}
	return [2]*protocol.Conn{r[0].c, r[1].c}, bases
}

func roundTrip(t *testing.T, sender, receiver *protocol.Conn, size int) {
	t.Helper()
	data := bytes.Repeat([]byte{0, 91, 255, 19, 128}, (size+4)/5)[:size]
	done := make(chan error, 1)
	go func() {
		done <- sender.WriteMessage(protocol.Message{Kind: "response", ID: "file-chunk", OK: true, Data: data})
	}()
	_ = receiver.SetReadDeadline(time.Now().Add(5 * time.Second))
	m, err := receiver.ReadMessage()
	if err != nil || m.ID != "file-chunk" || !bytes.Equal(m.Data, data) {
		t.Fatalf("payload corrupted: %v", err)
	}
	if err = await(t, done); err != nil {
		t.Fatal(err)
	}
	_ = receiver.SetReadDeadline(time.Time{})
}

func TestLocalDirectInterleavedLargeMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pair, _ := directPair(t, ctx)
	pair[0].EnableInterleaving()
	pair[1].EnableInterleaving()
	roundTrip(t, pair[0], pair[1], 2<<20)
	roundTrip(t, pair[1], pair[0], 500_003)
}

func TestFallbackAgreesAndPreservesBase(t *testing.T) {
	for _, test := range []struct {
		name    string
		options [2]Options
	}{
		{"viewer-no-udp", [2]Options{noUDPOptions(), hostOptions()}},
		{"agent-no-udp", [2]Options{hostOptions(), noUDPOptions()}},
		{"neither-udp", [2]Options{noUDPOptions(), noUDPOptions()}},
		{"turn-rejected", [2]Options{{STUNURLs: []string{"turn:127.0.0.1:3478"}}, hostOptions()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, b := encryptedPair(t)
			bases := [2]*protocol.Conn{protocol.NewConn(a), protocol.NewConn(b)}
			started := time.Now()
			r := runPair(t, context.Background(), bases, test.options, [2]func() (protocol.Message, error){})
			if time.Since(started) > time.Second {
				t.Fatal("no-UDP fallback was not fast")
			}
			for i, result := range r {
				if result.err != nil || result.info.Mode != "relay" || result.c != bases[i] {
					t.Fatalf("fallback: %+v", result)
				}
			}
			if r[0].info != r[1].info {
				t.Fatalf("decisions differ: %+v", r)
			}
			roundTrip(t, r[0].c, r[1].c, 99_999)
		})
	}
}

func TestICETimeoutFallsBack(t *testing.T) {
	a, b := encryptedPair(t)
	bases := [2]*protocol.Conn{protocol.NewConn(a), protocol.NewConn(b)}
	var readers [2]func() (protocol.Message, error)
	for i := range readers {
		readers[i] = func() (protocol.Message, error) {
			m, err := bases[i].ReadMessage()
			if m.Method == "offer" || m.Method == "answer" {
				var s signal
				_ = json.Unmarshal(m.Params, &s)
				lines := strings.Split(s.SDP, "\r\n")
				for j, line := range lines {
					if strings.HasPrefix(line, "a=candidate:") {
						f := strings.Fields(line)
						f[4] = "192.0.2.1"
						lines[j] = strings.Join(f, " ")
					}
				}
				s.SDP = strings.Join(lines, "\r\n")
				m.Params, _ = json.Marshal(s)
			}
			return m, err
		}
	}
	o := hostOptions()
	o.Timeout = 250 * time.Millisecond
	started := time.Now()
	r := runPair(t, context.Background(), bases, [2]Options{o, o}, readers)
	if time.Since(started) > time.Second {
		t.Fatal("ICE timeout exceeded budget")
	}
	for _, result := range r {
		if result.err != nil || result.info.Mode != "relay" || result.info.Reason != "ice_timeout" {
			t.Fatalf("timeout fallback: %+v", result)
		}
	}
	roundTrip(t, r[0].c, r[1].c, 1234)
}

func TestShortReadsLargeWriteAndDeadlines(t *testing.T) {
	pair, _ := directPair(t, context.Background())
	left, right := pair[0].Conn, pair[1].Conn
	data := bytes.Repeat([]byte{0, 255, 19, 203, 71}, 420_003)
	done := make(chan error, 1)
	go func() {
		n, err := left.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	_ = right.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(data))
	for off := 0; off < len(got); {
		n, err := right.Read(got[off:min(off+37, len(got))])
		if err != nil {
			t.Fatal(err)
		}
		off += n
	}
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, got) {
		t.Fatal("short reads discarded tails")
	}
	_ = right.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	_, err := right.Read(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read deadline: %v", err)
	}
	_ = right.SetReadDeadline(time.Time{})
	_ = left.SetWriteDeadline(time.Now().Add(-time.Second))
	if _, err = left.Write([]byte{1}); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write deadline: %v", err)
	}
	_ = left.SetWriteDeadline(time.Time{})
	go func() { _, err := left.Write([]byte{73}); done <- err }()
	var last [1]byte
	if _, err = right.Read(last[:]); err != nil || last[0] != 73 {
		t.Fatalf("deadline reset: %v", err)
	}
	if err = await(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestBackpressureDeadlineUpdateAndConcurrentClose(t *testing.T) {
	pair, _ := directPair(t, context.Background())
	left := pair[0].Conn.(*sessionConn)
	writeDone := make(chan error, 1)
	go func() { _, err := left.Write(make([]byte, 8<<20)); writeDone <- err }()
	// No receiver drains the SCTP stream. A deadline changed during Write must
	// interrupt backpressure, while the retained send buffer stays bounded.
	_ = left.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	if err := await(t, writeDone); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("blocked write: %v", err)
	}
	stream := left.Conn.(*dataConn)
	if n := stream.dc.BufferedAmount(); n > bufferLimit {
		t.Fatalf("unbounded send queue: %d", n)
	}
	_ = left.SetWriteDeadline(time.Time{})
	go func() { _, err := left.Write(make([]byte, 8<<20)); writeDone <- err }()
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = left.Close() }()
	}
	closed := make(chan struct{})
	go func() { wg.Wait(); close(closed) }()
	await(t, closed)
	if err := await(t, writeDone); err == nil {
		t.Fatal("blocked write survived close")
	}
}

func TestRelayCloseTerminatesDirect(t *testing.T) {
	pair, bases := directPair(t, context.Background())
	_ = bases[0].Close()
	for _, pc := range pair {
		session := pc.Conn.(*sessionConn)
		await(t, session.closed)
		if _, err := pc.Conn.Write([]byte("must not replay")); !errors.Is(err, ErrRelayLost) && !errors.Is(err, ErrDirectLost) {
			t.Fatalf("not a terminal transport error: %v", err)
		}
	}
}

func TestSessionCancellationAndMidSessionDirectLoss(t *testing.T) {
	for _, cancelSession := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelSession), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pair, _ := directPair(t, ctx)
			left := pair[0].Conn.(*sessionConn)
			if cancelSession {
				cancel()
			} else {
				_ = left.a.pc.Close()
			}
			for i, pc := range pair {
				session := pc.Conn.(*sessionConn)
				await(t, session.closed)
				n, err := session.Write([]byte{1})
				if n != 0 {
					t.Fatalf("endpoint %d accepted %d bytes after termination", i, n)
				}
				if cancelSession && !errors.Is(err, context.Canceled) {
					t.Fatalf("endpoint %d session context not retained: %v", i, err)
				}
				// Closing the PeerConnection wakes both endpoints. The peer may
				// close its tether before our local ICE monitor is scheduled, and
				// end() retains the first terminal event. Either transport error is
				// valid; both endpoints must terminate without accepting more data.
				if !cancelSession && !errors.Is(err, ErrDirectLost) && !errors.Is(err, ErrRelayLost) {
					t.Fatalf("endpoint %d direct loss not terminal: %v", i, err)
				}
			}
		})
	}
}

func TestMalformedSignalsAndCommitAreFatal(t *testing.T) {
	for _, stage := range []string{"offer", "ready", "commit"} {
		t.Run(stage, func(t *testing.T) {
			a, b := encryptedPair(t)
			bases := [2]*protocol.Conn{protocol.NewConn(a), protocol.NewConn(b)}
			read := func() (protocol.Message, error) {
				m, err := bases[1].ReadMessage()
				if m.Method == stage {
					m.Params = json.RawMessage(`{"mode":"p2p","reason":"forged"}`)
				}
				return m, err
			}
			r := runPair(t, context.Background(), bases, [2]Options{noUDPOptions(), hostOptions()}, [2]func() (protocol.Message, error){nil, read})
			// The sender can have acknowledged its relay commit just before the
			// receiver detects corruption; that connection must already be closed.
			for _, result := range r {
				if result.err != nil {
					if !errors.Is(result.err, ErrSignaling) || result.info != (Info{}) {
						t.Fatalf("unsafe failure: %+v", result)
					}
				} else if _, err := result.c.ReadMessage(); err == nil {
					t.Fatal("corrupt session remained usable")
				}
			}
		})
	}
}

func TestSingleApprovalReader(t *testing.T) {
	a, b := encryptedPair(t)
	bases := [2]*protocol.Conn{protocol.NewConn(a), protocol.NewConn(b)}
	type readResult struct {
		m   protocol.Message
		err error
	}
	incoming := make(chan readResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			m, err := bases[1].ReadMessage()
			incoming <- readResult{m, err}
			if err != nil {
				return
			}
		}
	}()
	var active atomic.Int32
	read := func() (protocol.Message, error) {
		if active.Add(1) != 1 {
			t.Error("multiple callback readers")
		}
		defer active.Add(-1)
		result := <-incoming
		return result.m, result.err
	}
	r := runPair(t, context.Background(), bases, [2]Options{hostOptions(), hostOptions()}, [2]func() (protocol.Message, error){nil, read})
	for _, result := range r {
		if result.err != nil || result.info.Mode != "p2p" {
			t.Fatalf("approval reader: %+v", result)
		}
	}
	roundTrip(t, r[0].c, r[1].c, 17_777)
	_ = r[0].c.Close()
	_ = r[1].c.Close()
	await(t, done)
}
