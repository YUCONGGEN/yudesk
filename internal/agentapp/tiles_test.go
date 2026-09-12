package agentapp

import (
	"context"
	"image"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/stream"
)

func TestTileInteractionPacing(t *testing.T) {
	base := time.Second / 30
	for _, tc := range []struct {
		name              string
		bandwidth         time.Duration
		idle, interactive bool
		want              time.Duration
	}{
		{"normal", 0, false, false, base},
		{"idle", 0, true, false, 100 * time.Millisecond},
		{"input wakes idle", 0, true, true, time.Second / 60},
		{"input follow-up", 0, false, true, time.Second / 60},
		{"input respects bandwidth", 150 * time.Millisecond, true, true, 150 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tileCaptureInterval(base, tc.bandwidth, tc.idle, tc.interactive); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	if got := inputCaptureInterval(base, 0); got != time.Second/120 {
		t.Fatalf("fast interaction interval: got %v", got)
	}
	if got := inputTailCaptureInterval(base, 0); got != time.Second/60 {
		t.Fatalf("interaction tail interval: got %v", got)
	}
}

func TestTileInputCannotBypassAcknowledgementWindow(t *testing.T) {
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	acks := make(chan string, 32)
	input := newInputSession()
	var captures atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		runTileStream(ctx, protocol.NewConn(a), stream.Options{FPS: 60, Quality: 70, Mode: "adaptive", FrameAck: true}, "test", acks, input, func(desktop.CaptureOptions) (desktop.Screenshot, error) {
			n := captures.Add(1)
			img := image.NewRGBA(image.Rect(0, 0, 16, 16))
			img.Pix[0], img.Pix[3] = byte(n), 255
			return desktop.Screenshot{Pixels: img, Width: 16, Height: 16, SourceWidth: 16, SourceHeight: 16}, nil
		})
	}()
	t.Cleanup(func() { cancel(); a.Close(); b.Close(); <-done })
	b.SetReadDeadline(time.Now().Add(3 * time.Second))
	r := protocol.NewConn(b)
	var first string
	// Before the first ACK the latency controller starts with a two-frame
	// window instead of the previous fixed eight-frame allowance.
	for i := 0; i < 2; i++ {
		m, err := r.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = m.ID
		}
	}
	input.wake <- struct{}{}
	next := make(chan error, 1)
	go func() { _, err := r.ReadMessage(); next <- err }()
	select {
	case err := <-next:
		t.Fatalf("transmitted past ACK bound: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	if n := captures.Load(); n <= 2 {
		t.Fatal("network wait blocked the independent capture worker")
	}
	acks <- first
	select {
	case err := <-next:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ACK did not resume sending")
	}
}
