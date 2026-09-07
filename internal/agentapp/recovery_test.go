package agentapp

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/stream"
)

func TestDesktopStreamRecoversFromProtectedDesktop(t *testing.T) {
	for _, tiled := range []bool{false, true} {
		name := "jpeg"
		if tiled {
			name = "tiles"
		}
		t.Run(name, func(t *testing.T) {
			a, b := net.Pipe()
			ctx, cancel := context.WithCancel(context.Background())
			var available atomic.Bool
			capture := func(desktop.CaptureOptions) (desktop.Screenshot, error) {
				if !available.Load() {
					return desktop.Screenshot{}, errors.New("test: protected desktop")
				}
				return desktop.Screenshot{Pixels: image.NewRGBA(image.Rect(0, 0, 16, 16)), JPEG: []byte{1, 2, 3}, Width: 16, Height: 16, SourceWidth: 16, SourceHeight: 16}, nil
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				run := runDesktopStream
				if tiled {
					run = runTileStream
				}
				run(ctx, protocol.NewConn(a), stream.Options{FPS: 30, Quality: 70, Mode: "fixed", SaveIdle: true}, "recovery", make(chan string), newInputSession(), capture)
			}()
			t.Cleanup(func() {
				cancel()
				a.Close()
				b.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("capture worker failed to stop")
				}
			})
			b.SetReadDeadline(time.Now().Add(8 * time.Second))
			r := protocol.NewConn(b)
			read := func(method string) protocol.Message {
				t.Helper()
				for i := 0; i < 10; i++ {
					m, err := r.ReadMessage()
					if err != nil {
						t.Fatal(err)
					}
					if m.Method == method {
						return m
					}
					if m.Method != "tiles" && m.Method != "frame" {
						t.Fatalf("wanted %s, got %+v", method, m)
					}
				}
				t.Fatalf("missing %s", method)
				return protocol.Message{}
			}
			// Covers connecting while already locked, then locking an existing session.
			for cycle := 0; cycle < 2; cycle++ {
				waiting := read("stream_waiting")
				if waiting.Error != "test: protected desktop" {
					t.Fatalf("missing explanation: %+v", waiting)
				}
				available.Store(true)
				read("stream_resumed")
				method := "frame"
				if tiled {
					method = "tiles"
				}
				frame := read(method)
				if tiled {
					var meta stream.TileFrame
					if json.Unmarshal(frame.Params, &meta) != nil || !meta.Reset || len(meta.Tiles) == 0 {
						t.Fatalf("resume did not force a complete keyframe: %s", frame.Params)
					}
				}
				available.Store(false)
			}
		})
	}
}

func TestCaptureRetryCancelsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	frames := make(chan capturedDesktop, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		captureLatest(ctx, stream.Options{FPS: 30}, newInputSession(), &captureSettings{pixels: make(chan *image.RGBA, 1)}, func(desktop.CaptureOptions) (desktop.Screenshot, error) {
			return desktop.Screenshot{}, errors.New("locked")
		}, frames)
	}()
	select {
	case <-frames:
	case <-time.After(time.Second):
		t.Fatal("no error state")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("retry sleep ignored cancellation")
	}
}
