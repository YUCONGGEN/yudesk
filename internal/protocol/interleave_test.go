package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"testing"
	"time"
)

type firstWriteConn struct {
	net.Conn
	once            sync.Once
	entered, resume chan struct{}
	bytesPerSecond  int
	clock           time.Time
	bytesWritten    int64
}

func (c *firstWriteConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered); <-c.resume })
	if c.bytesPerSecond > 0 {
		// Absolute byte clock: do not add timer-rounding overhead once per
		// fragment and accidentally penalize the interleaved configuration.
		if c.clock.IsZero() {
			c.clock = time.Now()
		}
		c.bytesWritten += int64(len(p))
		if wait := time.Until(c.clock.Add(time.Duration(c.bytesWritten * int64(time.Second) / int64(c.bytesPerSecond)))); wait > 0 {
			time.Sleep(wait)
		}
	}
	return c.Conn.Write(p)
}

func waitGate(t testing.TB, g *priorityGate, high, low int) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		g.mu.Lock()
		ready := len(g.high) == high && len(g.low) == low
		g.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("writer did not queue")
}

// This records a real request/reply on the loaded protocol connection. The
// unbuffered, bandwidth-limited wire isolates application queueing, NOT an
// Internet RTT prediction. Every frame is read and verified before returning.
func probeBehindFrame(t testing.TB, enabled bool, size, rate int) time.Duration {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = a.SetDeadline(time.Now().Add(15 * time.Second))
	_ = b.SetDeadline(time.Now().Add(15 * time.Second))
	wire := &firstWriteConn{Conn: a, entered: make(chan struct{}), resume: make(chan struct{}), bytesPerSecond: rate}
	server, client := NewConn(wire), NewConn(b)
	if enabled {
		server.EnableInterleaving()
		client.EnableInterleaving()
	}
	data := bytes.Repeat([]byte{0x6d}, size)
	bulkDone := make(chan error, 1)
	go func() {
		bulkDone <- server.WriteMessage(Message{Kind: "event", Method: "frame", ID: "frame", Data: data})
	}()
	<-wire.entered
	controlDone := make(chan error, 1)
	go func() {
		m, err := server.ReadMessage()
		if err == nil {
			err = server.WriteMessage(Response(m.ID, nil, nil))
		}
		controlDone <- err
	}()
	started := time.Now()
	if err := client.WriteMessage(Message{Kind: "request", Method: "ping", ID: "probe"}); err != nil {
		t.Fatal(err)
	}
	waitGate(t, &server.write, 1, 0)
	close(wire.resume)
	var rtt time.Duration
	var order []string
	for range 2 {
		m, err := client.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, m.ID)
		if m.ID == "probe" {
			rtt = time.Since(started)
		} else if m.ID != "frame" || !bytes.Equal(m.Data, data) {
			t.Fatal("frame lost or corrupted")
		}
	}
	if err := <-bulkDone; err != nil {
		t.Fatal(err)
	}
	if err := <-controlDone; err != nil {
		t.Fatal(err)
	}
	if enabled && order[0] != "probe" {
		t.Fatalf("probe remained behind entire frame: %v", order)
	}
	if !enabled && order[0] != "frame" {
		t.Fatalf("legacy framing reordered: %v", order)
	}
	return rtt
}

func TestInterleavingPreemptsLargeFrameWithoutLosingData(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) { probeBehindFrame(t, enabled, 257<<10, 0) })
	}
}

func BenchmarkLoadedProbe(b *testing.B) {
	for _, rate := range []int{250000, 1000000} {
		for _, enabled := range []bool{false, true} {
			b.Run(fmt.Sprintf("Mbps%d/interleave=%v", rate*8/1000000, enabled), func(b *testing.B) {
				var samples []time.Duration
				for i := 0; i < b.N; i++ {
					samples = append(samples, probeBehindFrame(b, enabled, 256<<10, rate))
				}
				sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
				b.ReportMetric(float64(samples[len(samples)/2].Microseconds())/1000, "probe-p50-ms")
				b.ReportMetric(float64(samples[(95*len(samples)+99)/100-1].Microseconds())/1000, "probe-p95-ms")
			})
		}
	}
}

func TestInterleavingConcurrentBulkAndSmallMessages(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = a.SetDeadline(time.Now().Add(5 * time.Second))
	_ = b.SetDeadline(time.Now().Add(5 * time.Second))
	w, r := NewConn(a), NewConn(b)
	w.EnableInterleaving()
	r.EnableInterleaving()
	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(id int) {
			size := 32
			if id%3 == 0 {
				size = 100000 + id
			}
			done <- w.WriteMessage(Message{Kind: "event", ID: fmt.Sprint(id), Data: bytes.Repeat([]byte{byte(id)}, size)})
		}(i)
	}
	seen := map[string]bool{}
	for range 20 {
		m, err := r.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if seen[m.ID] {
			t.Fatal("duplicate")
		}
		seen[m.ID] = true
		var id int
		fmt.Sscan(m.ID, &id)
		size := 32
		if id%3 == 0 {
			size = 100000 + id
		}
		if !bytes.Equal(m.Data, bytes.Repeat([]byte{byte(id)}, size)) {
			t.Fatal("corruption")
		}
	}
	for range 20 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestInterleavingCancellationAfterFirstFragmentCloses(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	w, r := NewConn(a), NewConn(b)
	w.EnableInterleaving()
	r.EnableInterleaving()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.WriteMessageContext(ctx, Message{Kind: "event", Data: make([]byte, 8*fragmentSize)}) }()
	_, complete, err := r.readPacket()
	if err != nil || complete {
		t.Fatalf("first fragment: %v %v", complete, err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("write leaked")
	}
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := r.ReadMessage(); err == nil {
		t.Fatal("incomplete message connection stayed usable")
	}
}

func TestPacedBulkYieldsToControlAndRespectsBudget(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = a.SetDeadline(time.Now().Add(3 * time.Second))
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	w, r := NewConn(a), NewConn(b)
	w.EnableInterleaving()
	r.EnableInterleaving()
	w.SetBulkRate(80 << 10)
	done := make(chan error, 2)
	started := time.Now()
	go func() { done <- w.WriteMessage(Message{Kind: "event", ID: "bulk", Data: make([]byte, 3*fragmentSize)}) }()
	if _, complete, err := r.readPacket(); err != nil || complete {
		t.Fatal("first paced fragment", complete, err)
	}
	go func() { done <- w.WriteMessage(Response("control", nil, nil)) }()
	m, err := r.ReadMessage()
	if err != nil || m.ID != "control" {
		t.Fatal("pacer blocked control", m.ID, err)
	}
	m, err = r.ReadMessage()
	if err != nil || m.ID != "bulk" || len(m.Data) != 3*fragmentSize {
		t.Fatal("paced frame corrupted", err)
	}
	if time.Since(started) < 180*time.Millisecond {
		t.Fatal("payload budget ignored")
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func rawFragment(flags byte, offset, total uint32, header, data []byte) []byte {
	h := make([]byte, 9)
	h[0] = flags
	binary.BigEndian.PutUint32(h[1:5], offset)
	binary.BigEndian.PutUint32(h[5:9], total)
	h = append(h, header...)
	p := make([]byte, 8)
	binary.BigEndian.PutUint32(p[:4], uint32(len(h))|fragmentFlag)
	binary.BigEndian.PutUint32(p[4:], uint32(len(data)))
	p = append(p, h...)
	return append(p, data...)
}
func TestMalformedInterleavingIsBounded(t *testing.T) {
	h := []byte(`{"kind":"event","method":"frame"}`)
	first := rawFragment(1, 0, 2, h, []byte{1})
	for name, wire := range map[string][]byte{
		"not negotiated":        rawFragment(3, 0, 1, h, []byte{1}),
		"unknown flags":         rawFragment(7, 0, 1, h, []byte{1}),
		"oversize total":        rawFragment(1, 0, MaxDataSize+1, h, []byte{1}),
		"missing start":         rawFragment(2, 1, 2, nil, []byte{2}),
		"zero data":             rawFragment(3, 0, 0, h, nil),
		"huge fragment":         rawFragment(3, 0, fragmentSize+1, h, make([]byte, fragmentSize+1)),
		"bad json":              rawFragment(3, 0, 1, []byte("{"), []byte{1}),
		"early final":           rawFragment(3, 0, 2, h, []byte{1}),
		"missing final":         rawFragment(1, 0, 1, h, []byte{1}),
		"overlap":               append(bytes.Clone(first), rawFragment(2, 0, 2, nil, []byte{2})...),
		"changed total":         append(bytes.Clone(first), rawFragment(0, 1, 3, nil, []byte{2})...),
		"second start":          append(bytes.Clone(first), rawFragment(3, 0, 1, h, []byte{2})...),
		"continuation metadata": append(bytes.Clone(first), rawFragment(2, 1, 2, h, []byte{2})...),
		"truncated":             first,
	} {
		t.Run(name, func(t *testing.T) {
			c := NewConn(&bufferedTestConn{buf: *bytes.NewBuffer(wire)})
			if name != "not negotiated" {
				c.EnableInterleaving()
			}
			if _, err := c.ReadMessage(); err == nil {
				t.Fatal("invalid fragment accepted")
			}
			if c.partial != nil {
				t.Fatal("partial allocation retained after error")
			}
		})
	}
}

func TestReceiveOfferDoesNotChangeLegacyWrites(t *testing.T) {
	wire := &bufferedTestConn{}
	w := NewConn(wire)
	w.OfferInterleaving()
	data := make([]byte, 100000)
	if err := w.WriteMessage(Message{Kind: "event", Data: data}); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(wire.buf.Bytes()[:4])&fragmentFlag != 0 {
		t.Fatal("offer enabled sending before acceptance")
	}
	m, err := NewConn(wire).ReadMessage()
	if err != nil || len(m.Data) != len(data) {
		t.Fatal("legacy receiver failed", err)
	}
}

func TestPriorityGateFairnessAndCancellation(t *testing.T) {
	var g priorityGate
	if err := g.acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	order := make(chan bool, 11)
	go func() { _ = g.acquire(context.Background(), false); order <- false; g.release() }()
	waitGate(t, &g, 0, 1)
	for i := 0; i < 10; i++ {
		go func() { _ = g.acquire(context.Background(), true); order <- true; g.release() }()
	}
	waitGate(t, &g, 10, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.acquire(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	g.release()
	for i := 0; i < 11; i++ {
		select {
		case high := <-order:
			if (i == 8) == high {
				t.Fatalf("bad priority/fairness at %d", i)
			}
		case <-time.After(time.Second):
			t.Fatal("gate stuck")
		}
	}
	for range 200 {
		_ = g.acquire(context.Background(), false)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			if g.acquire(ctx, true) == nil {
				g.release()
			}
			close(done)
		}()
		cancel()
		g.release()
		<-done
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held || len(g.high) > 0 || len(g.low) > 0 {
		t.Fatal("cancel leaked writer gate")
	}
}

func FuzzInterleavedReader(f *testing.F) {
	h, _ := json.Marshal(Message{Kind: "event", Method: "frame"})
	f.Add(rawFragment(3, 0, 1, h, []byte{1}))
	f.Add([]byte("invalid"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 65536 {
			return
		}
		c := NewConn(&bufferedTestConn{buf: *bytes.NewBuffer(data)})
		c.EnableInterleaving()
		for i := 0; i < 10; i++ {
			if _, err := c.ReadMessage(); err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				return
			}
		}
	})
}
