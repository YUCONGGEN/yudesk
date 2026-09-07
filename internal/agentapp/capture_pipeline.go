package agentapp

import (
	"context"
	"image"
	"sync/atomic"
	"time"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/stream"
)

type capturedDesktop struct {
	shot desktop.Screenshot
	cost time.Duration
	err  error
}

type captureSettings struct {
	width  atomic.Int32
	idle   atomic.Bool
	pixels chan *image.RGBA
}

func (s *captureSettings) recycle(pixels *image.RGBA) {
	if pixels != nil {
		select {
		case s.pixels <- pixels:
		default:
		}
	}
}

// One capture worker owns the native capture boundary. It runs independently
// of compression and network writes, but retains at most ONE pending image.
// Replacing raw images is safe: the encoder compares against the last encoded
// image, not discarded captures, so dirty regions are not lost.
func captureLatest(ctx context.Context, options stream.Options, input *inputSession, settings *captureSettings, capture func(desktop.CaptureOptions) (desktop.Screenshot, error), frames chan capturedDesktop) {
	base := time.Second / time.Duration(options.FPS)
	var interactionUntil time.Time
	for ctx.Err() == nil {
		start := time.Now()
		var buffer *image.RGBA
		select {
		case buffer = <-settings.pixels:
		default:
		}
		shot, err := capture(desktop.CaptureOptions{Raw: true, Quality: options.Quality, MaxWidth: int(settings.width.Load()), Buffer: buffer})
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			input.geometry(shot.SourceWidth, shot.SourceHeight)
		}
		frame := capturedDesktop{shot: shot, cost: time.Since(start), err: err}
		select {
		case frames <- frame:
		default:
			select {
			case discarded := <-frames:
				settings.recycle(discarded.shot.Pixels)
			default:
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			settings.recycle(buffer)
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		waiting := true
		for waiting {
			interactive := options.Mode == "adaptive" && time.Now().Before(interactionUntil)
			interval := tileCaptureInterval(base, 0, options.SaveIdle && settings.idle.Load(), interactive)
			if interactive {
				interval = inputCaptureInterval(base, frame.cost)
			}
			timer := time.NewTimer(max(0, interval-time.Since(start)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-input.wake:
				timer.Stop()
				settings.idle.Store(false)
				interactionUntil = time.Now().Add(100 * time.Millisecond)
			case <-timer.C:
				waiting = false
			}
		}
	}
}

// Short input bursts can sample at 120 Hz only when capture is cheap. Expensive
// native capture keeps the previous <=60 Hz ceiling; fixed FPS is unchanged.
func inputCaptureInterval(base, captureCost time.Duration) time.Duration {
	return min(base, max(time.Second/120, min(time.Second/60, captureCost*2)))
}
