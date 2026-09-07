package agentapp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/stream"
)

func runDesktopStream(ctx context.Context, c *protocol.Conn, options stream.Options, generation string, acks <-chan string, input *inputSession, capture func(desktop.CaptureOptions) (desktop.Screenshot, error)) {
	options = options.Normalized()
	base := time.Second / time.Duration(options.FPS)
	quality := options.Quality
	var previous [32]byte
	hasPrevious := false
	lastSent := time.Time{}
	unchanged := 0
	pending := map[string]bool{}
	sequence := uint64(0)
	var captureError string
	for {
		if ctx.Err() != nil {
			return
		}
		// At most two complete frames may be in flight. Slow links cannot build
		// an ever-growing queue of obsolete screenshots ahead of input replies.
		if options.FrameAck {
			for len(pending) >= 2 {
				select {
				case id := <-acks:
					delete(pending, id)
				case <-ctx.Done():
					return
				}
			}
		}
		started := time.Now()
		shot, err := capture(desktop.CaptureOptions{Quality: quality, MaxWidth: options.MaxWidth})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if captureError != err.Error() {
				captureError = err.Error()
				if c.WriteMessageContext(ctx, protocol.Message{Kind: "event", Method: "stream_waiting", Error: captureError}) != nil {
					return
				}
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		if captureError != "" {
			captureError = ""
			hasPrevious = false
			if c.WriteMessageContext(ctx, protocol.Message{Kind: "event", Method: "stream_resumed"}) != nil {
				return
			}
		}
		input.geometry(shot.SourceWidth, shot.SourceHeight)
		hash := sha256.Sum256(shot.JPEG)
		if hasPrevious && hash == previous {
			unchanged++
		} else {
			unchanged = 0
		}
		interval := base
		if !options.SaveIdle || unchanged == 0 || time.Since(lastSent) >= 5*time.Second {
			sequence++
			id := fmt.Sprintf("%s:%d", generation, sequence)
			if err := c.WriteMessage(protocol.Message{Kind: "event", Method: "frame", ID: id, Data: shot.JPEG, Meta: map[string]any{
				"width": shot.Width, "height": shot.Height, "sourceWidth": shot.SourceWidth, "sourceHeight": shot.SourceHeight,
				"quality": quality, "targetFPS": options.FPS, "mode": options.Mode, "frameAck": options.FrameAck,
				"captureMs": float64(time.Since(started).Microseconds()) / 1000,
			}}); err != nil {
				return
			}
			if options.FrameAck {
				pending[id] = true
			}
			lastSent = time.Now()
			if options.MaxMbps > 0 {
				// Payload-rate budget, not a claim about ISP/TLS wire accounting.
				budget := time.Duration(float64(len(shot.JPEG)*8) / float64(options.MaxMbps*1000000) * float64(time.Second))
				if budget > interval {
					interval = budget
				}
			}
		}
		previous, hasPrevious = hash, true
		if options.Mode == "adaptive" && unchanged == 0 {
			elapsed := time.Since(started)
			if (elapsed > base || interval > base) && quality > 35 {
				quality = max(35, quality-5)
			} else if elapsed < base/2 && quality < options.Quality {
				quality++
			}
		}
		if options.SaveIdle && unchanged > options.FPS && interval < 200*time.Millisecond {
			interval = 200 * time.Millisecond
		}
		wait := interval - time.Since(started)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		waiting := true
		for waiting {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case id := <-acks:
				delete(pending, id)
			case <-input.wake:
				// Input wakes an idle capture, but does not bypass the rate budget.
				if options.SaveIdle && unchanged > options.FPS && time.Since(started) >= base && options.MaxMbps == 0 {
					timer.Stop()
					waiting = false
				}
			case <-timer.C:
				waiting = false
			}
		}
	}
}
