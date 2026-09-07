package agentapp

import (
	"bytes"
	"context"
	"image"
	_ "image/png"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/stream"
)

func TestCapturePipelineKeepsOnlyLatestFrameWithoutConsumer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	settings := &captureSettings{}
	settings.width.Store(1280)
	frames := make(chan capturedDesktop, 1)
	started := make(chan byte, 32)
	done := make(chan struct{})
	n := byte(0)
	go func() {
		defer close(done)
		captureLatest(ctx, stream.Options{FPS: 60, Quality: 70, Mode: "fixed"}, newInputSession(), settings, func(options desktop.CaptureOptions) (desktop.Screenshot, error) {
			n++
			if !options.Raw || options.MaxWidth != 1280 {
				t.Error("capture options lost")
			}
			img := image.NewRGBA(image.Rect(0, 0, 2, 2))
			img.Pix[0] = n
			started <- n
			return desktop.Screenshot{Pixels: img, SourceWidth: 2, SourceHeight: 2}, nil
		}, frames)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("capture worker leaked")
		}
	})
	for i := 0; i < 7; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("capture blocked behind slow consumer")
		}
	}
	if len(frames) != 1 {
		t.Fatalf("unbounded or missing waiting image: %d", len(frames))
	}
	latest := <-frames
	if latest.shot.Pixels.Pix[0] < 6 {
		t.Fatalf("stale image %d retained", latest.shot.Pixels.Pix[0])
	}
}

func TestInputCaptureIntervalBudget(t *testing.T) {
	base := time.Second / 30
	for _, tc := range []struct{ cost, want time.Duration }{{time.Millisecond, time.Second / 120}, {6 * time.Millisecond, 12 * time.Millisecond}, {20 * time.Millisecond, time.Second / 60}} {
		if got := inputCaptureInterval(base, tc.cost); got != tc.want {
			t.Fatalf("cost %v: got %v want %v", tc.cost, got, tc.want)
		}
	}
}

func TestRecycledCaptureBuffersDoNotOverwriteEncoderReference(t *testing.T) {
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	var sequence, reused atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		runTileStream(ctx, protocol.NewConn(a), stream.Options{FPS: 60, Quality: 70, Mode: "fixed", SaveIdle: true}, "test", make(chan string), newInputSession(), func(o desktop.CaptureOptions) (desktop.Screenshot, error) {
			img := o.Buffer
			if img == nil {
				img = image.NewRGBA(image.Rect(0, 0, 16, 16))
			} else {
				reused.Add(1)
			}
			n := byte(sequence.Add(1))
			for i := 0; i < len(img.Pix); i += 4 {
				img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = n, 77, 123, 255
			}
			return desktop.Screenshot{Pixels: img, Width: 16, Height: 16, SourceWidth: 16, SourceHeight: 16}, nil
		})
	}()
	t.Cleanup(func() { cancel(); a.Close(); b.Close(); <-done })
	b.SetReadDeadline(time.Now().Add(3 * time.Second))
	r := protocol.NewConn(b)
	var last uint32
	for i := 0; i < 6; i++ {
		time.Sleep(35 * time.Millisecond) // force overlap and replacement of captures
		m, err := r.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(bytes.NewReader(m.Data))
		if err != nil {
			t.Fatal(err)
		}
		red, _, _, _ := img.At(0, 0).RGBA()
		if red <= last {
			t.Fatalf("reference frame overwritten or stale: %d after %d", red, last)
		}
		last = red
	}
	if reused.Load() == 0 {
		t.Fatal("test did not exercise capture buffer reuse")
	}
}
