package core

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/stream"
)

func TestControllerPullsNewestFrameAndAcknowledgesSkippedFrames(t *testing.T) {
	s := queuedSession(t)
	s.frame = nil
	s.acks = make(map[int64]string)
	a, b := net.Pipe()
	s.conn = protocol.NewConn(a)
	peer := protocol.NewConn(b)
	defer a.Close()
	defer b.Close()
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan struct{})
	go func() { s.reader(); close(done) }()
	data := jpegData(t, 64, 48)
	for _, id := range []string{"a", "b", "c"} {
		if err := peer.WriteMessage(protocol.Message{Kind: "event", Method: "frame", ID: id, Data: data, Meta: map[string]any{"width": 64, "height": 48, "frameAck": true}}); err != nil {
			t.Fatal(err)
		}
	}
	until(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.revision == 3 })
	if s.out.len() != 0 {
		t.Fatal("ACK preceded consumer progress")
	}
	f, err := s.NextFrame(0, 100)
	if err != nil || f == nil || f.Revision != 3 {
		t.Fatalf("newest frame: %+v, %v", f, err)
	}
	acks := make(map[string]bool)
	for i := 0; i < 3; i++ {
		m, ok := s.out.next(s.ctx)
		if !ok || m.Method != "frame_ack" {
			t.Fatal("missing frame acknowledgement")
		}
		acks[m.ID] = true
	}
	if !acks["a"] || !acks["b"] || !acks["c"] {
		t.Fatal("skipped frames exhausted ACK window")
	}
	if f, err = s.NextFrame(3, 1); err != nil || f != nil {
		t.Fatal("repeated already consumed frame")
	}
	a.Close()
	<-done
}

func TestCaptureSendsNewestFrameAfterACKBackpressure(t *testing.T) {
	e := testEngine(t)
	e.streaming = true
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	acks := make(chan string, 2)
	done := make(chan struct{})
	go func() {
		e.sendFrames(ctx, e.ctx, protocol.NewConn(a), acks, stream.Options{FPS: 30, FrameAck: true}, "latest")
		close(done)
	}()
	peer := protocol.NewConn(b)
	data := jpegData(t, 64, 48)
	for i := 0; i < 2; i++ {
		if err := e.SubmitJPEG(data, 64, 48); err != nil {
			t.Fatal(err)
		}
		m, err := peer.ReadMessage()
		if err != nil || m.Method != "frame" {
			t.Fatalf("initial frame: %+v, %v", m, err)
		}
	}
	for i := 2; i < 100; i++ {
		if err := e.SubmitJPEG(data, 64, 48); err != nil {
			t.Fatal(err)
		}
	}
	acks <- "latest:1"
	m, err := peer.ReadMessage()
	if err != nil || m.ID != "latest:100" {
		t.Fatalf("stale capture after backpressure: %s, %v", m.ID, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capture worker did not stop")
	}
}
