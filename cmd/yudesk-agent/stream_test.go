package main

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/stream"
)

func TestStreamBackpressureLimitsInFlightFrames(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acks := make(chan string, 4)
	input := newInputSession()
	var captures atomic.Int64
	capture := func(desktop.CaptureOptions) (desktop.Screenshot, error) {
		n := captures.Add(1)
		return desktop.Screenshot{JPEG: []byte{byte(n)}, Width: 640, Height: 480, SourceWidth: 640, SourceHeight: 480}, nil
	}
	done := make(chan struct{})
	go func() {
		runDesktopStream(ctx, protocol.NewConn(a), stream.Options{FPS: 60, Quality: 70, Mode: "fixed", FrameAck: true}, "test", acks, input, capture)
		close(done)
	}()
	reader := protocol.NewConn(b)
	m, err := reader.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if captures.Load() != 2 {
		t.Fatalf("queued stale frames: %d", captures.Load())
	}
	acks <- m.ID
	if _, err := reader.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not cancel")
	}
}

func TestFixedStreamPacingAndBandwidthBudget(t *testing.T) {
	for _, limited := range []bool{false, true} {
		a, b := net.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		o := stream.Options{FPS: 20, Quality: 75, Mode: "fixed"}
		size := 100
		if limited {
			o.MaxMbps = 1
			size = 20000
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			runDesktopStream(ctx, protocol.NewConn(a), o, "test", make(chan string), newInputSession(), func(options desktop.CaptureOptions) (desktop.Screenshot, error) {
				if options.Quality != 75 {
					t.Error("fixed quality was changed")
				}
				return desktop.Screenshot{JPEG: make([]byte, size), Width: 640, Height: 480}, nil
			})
		}()
		r := protocol.NewConn(b)
		start := time.Now()
		for i := 0; i < 4; i++ {
			if _, err := r.ReadMessage(); err != nil {
				t.Fatal(err)
			}
		}
		elapsed := time.Since(start)
		minimum := 140 * time.Millisecond
		if limited {
			minimum = 450 * time.Millisecond
		}
		if elapsed < minimum {
			t.Errorf("pacing too fast: limited=%v elapsed=%v", limited, elapsed)
		}
		cancel()
		a.Close()
		b.Close()
		<-done
	}
}
