package agentapp

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"time"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/stream"
)

func runTileStream(ctx context.Context, c *protocol.Conn, options stream.Options, generation string, acks <-chan string, input *inputSession, capture func(desktop.CaptureOptions) (desktop.Screenshot, error)) {
	options = options.Normalized()
	base := time.Second / time.Duration(options.FPS)
	encoder := &stream.TileEncoder{}
	sequence := uint64(0)
	type flight struct {
		size int
		sent time.Time
	}
	pending := map[string]flight{}
	bytesPending := 0
	inFlightLimit := tileFlightBudget(options.MaxMbps)
	controller := stream.NewLatencyController(options)
	ack := func(id string) {
		if packet, ok := pending[id]; ok {
			bytesPending -= packet.size
			controller.Ack(time.Since(packet.sent))
			delete(pending, id)
		}
	}
	quality := options.Quality
	idle := 0
	settings := &captureSettings{pixels: make(chan *image.RGBA, 3)}
	settings.width.Store(int32(options.MaxWidth))
	frames := make(chan capturedDesktop, 1)
	captureCtx, cancelCapture := context.WithCancel(ctx)
	captureDone := make(chan struct{})
	go func() { defer close(captureDone); captureLatest(captureCtx, options, input, settings, capture, frames) }()
	defer func() { cancelCapture(); <-captureDone }()
	var sendNotBefore time.Time
	var previousPixels *image.RGBA
	var captureError string
	for ctx.Err() == nil {
		// Permit a small bandwidth-delay window for cheap dirty updates, while
		// bounding large frames by bytes. Never capture a queue of stale frames.
		for options.FrameAck && (len(pending) >= 8 || bytesPending >= inFlightLimit) {
			select {
			case id := <-acks:
				ack(id)
			case <-ctx.Done():
				return
			}
		}
		// Wait BEFORE taking an image: capture keeps the waiting slot fresh
		// during slow network writes, bandwidth pacing and ACK waits.
		for time.Now().Before(sendNotBefore) {
			timer := time.NewTimer(time.Until(sendNotBefore))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case id := <-acks:
				timer.Stop()
				ack(id)
			case <-timer.C:
			}
		}
		var captured capturedDesktop
		for ready := false; !ready; {
			select {
			case <-ctx.Done():
				return
			case id := <-acks:
				ack(id)
			case captured = <-frames:
				ready = true
			}
		}
		if captured.err != nil {
			if captureError != captured.err.Error() {
				captureError = captured.err.Error()
				if c.WriteMessageContext(ctx, protocol.Message{Kind: "event", Method: "stream_waiting", Error: captureError}) != nil {
					return
				}
			}
			continue
		}
		if captureError != "" {
			captureError = ""
			encoder = &stream.TileEncoder{}
			if c.WriteMessageContext(ctx, protocol.Message{Kind: "event", Method: "stream_resumed"}) != nil {
				return
			}
		}
		start := time.Now()
		shot := captured.shot
		frame, data, err := encoder.Encode(shot.Pixels, quality, false)
		// Encode's workers have joined; only its new reference image must stay
		// immutable. Never return the currently compared buffer to capture.
		if err == nil {
			settings.recycle(previousPixels)
			previousPixels = shot.Pixels
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			_ = c.WriteMessage(protocol.Message{Kind: "event", Method: "stream_error", Error: err.Error()})
			return
		}
		var bandwidthInterval time.Duration
		if len(frame.Tiles) > 0 || !options.SaveIdle {
			idle = 0
			sequence++
			frame.SourceWidth, frame.SourceHeight = shot.SourceWidth, shot.SourceHeight
			params, _ := json.Marshal(frame)
			id := fmt.Sprintf("%s:%d", generation, sequence)
			encodeMS := float64((captured.cost + time.Since(start)).Microseconds()) / 1000
			sentAt := time.Now()
			if err := c.WriteMessage(protocol.Message{Kind: "event", Method: "tiles", ID: id, Params: params, Data: data, Meta: map[string]any{
				"width": frame.Width, "height": frame.Height, "quality": quality, "captureMs": encodeMS,
				"codec": "tiles-v1", "changedTiles": len(frame.Tiles), "frameAck": options.FrameAck, "queueMs": float64(controller.QueueDelay().Microseconds()) / 1000,
			}}); err != nil {
				return
			}
			if options.FrameAck {
				pending[id] = flight{size: len(data), sent: sentAt}
				bytesPending += len(data)
			}
			if options.MaxMbps > 0 {
				bandwidthInterval = time.Duration(float64(len(data)*8) / float64(options.MaxMbps*1000000) * float64(time.Second))
			}
			if options.Mode == "adaptive" {
				// A fast socket write can merely fill the OS send buffer. Include
				// estimated serialization time at the configured payload budget,
				// otherwise large frames look cheap and never trigger backoff.
				controller.Update(time.Now(), max(captured.cost+time.Since(start), bandwidthInterval), base, shot.SourceWidth)
				quality = controller.Quality
				settings.width.Store(int32(controller.Width))
			}
			sendNotBefore = start.Add(bandwidthInterval)
		} else {
			idle++
		}
		settings.idle.Store(idle > options.FPS)
		draining := true
		for draining {
			select {
			case id := <-acks:
				ack(id)
			default:
				draining = false
			}
		}
	}
}

func tileCaptureInterval(base, bandwidth time.Duration, idle, interactive bool) time.Duration {
	interval := base
	if interactive {
		interval = min(base, time.Second/60)
	} else if idle {
		interval = max(base, 100*time.Millisecond)
	}
	return max(interval, bandwidth)
}

func tileFlightBudget(mbps int) int {
	if mbps <= 0 {
		return 128 << 10
	}
	// Approximately 100ms of configured payload bandwidth, not 1MB at every
	// link speed. A single atomic update can exceed this bound; after it is
	// sent, wait for ACKs before capturing any additional update.
	return min(1<<20, max(8<<10, mbps*1_000_000/8/10))
}
