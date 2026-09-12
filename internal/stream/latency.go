package stream

import (
	"math"
	"time"
)

// LatencyController is a conservative delay-based fallback for the TCP tile
// transport. It is not a reproduction of a learned congestion controller.
// Queue growth and capture/write costs take priority over visual quality;
// recovery is deliberately slower than backoff to avoid oscillating sizes.
type LatencyController struct {
	baseline, rtt        time.Duration
	lastChange           time.Time
	bad, good            int
	Quality, Width       int
	maxQuality, maxWidth int
}

func NewLatencyController(o Options) *LatencyController {
	return &LatencyController{Quality: o.Quality, Width: o.MaxWidth, maxQuality: o.Quality, maxWidth: o.MaxWidth}
}
func (c *LatencyController) Ack(rtt time.Duration) {
	if rtt <= 0 {
		return
	}
	if c.baseline == 0 || rtt < c.baseline {
		c.baseline = rtt
	}
	if c.rtt == 0 {
		c.rtt = rtt
	} else {
		c.rtt = (c.rtt*3 + rtt) / 4
	}
}
func (c *LatencyController) QueueDelay() time.Duration { return max(0, c.rtt-c.baseline) }

// FlightWindow keeps enough absolute tile updates in flight to fill the
// measured bandwidth-delay product without retaining the old fixed 100 ms
// window on a fast path. Before the first ACK it starts conservatively; after
// that the minimum observed RTT is propagation time, while QueueDelay remains
// excluded so congestion cannot enlarge the window that caused it.
func (c *LatencyController) FlightWindow(framePeriod time.Duration, maxMbps int) (frames, bytes int) {
	if framePeriod <= 0 {
		framePeriod = time.Second / 30
	}
	propagation := c.baseline
	if propagation <= 0 {
		propagation = 50 * time.Millisecond
		frames = 2
	} else {
		propagation = min(150*time.Millisecond, max(20*time.Millisecond, propagation))
		frames = int(math.Round(float64(propagation) / float64(framePeriod)))
		frames = min(8, max(2, frames))
	}
	if maxMbps <= 0 {
		return frames, 128 << 10
	}
	bytes = int(float64(maxMbps*1_000_000) / 8 * propagation.Seconds())
	bytes = min(1<<20, max(8<<10, bytes))
	return frames, bytes
}

func (c *LatencyController) Update(now time.Time, cost, budget time.Duration, sourceWidth int) {
	if c.QueueDelay() > 40*time.Millisecond || cost > budget*3/2 {
		c.bad++
		c.good = 0
	} else {
		c.good++
		c.bad = 0
	}
	if !c.lastChange.IsZero() && now.Sub(c.lastChange) < time.Second {
		return
	}
	if c.bad >= 3 {
		c.Quality = max(min(45, c.maxQuality), c.Quality-8)
		// Reducing the captured image also improves PNG/text traffic, which
		// does not respond to JPEG quality changes.
		if c.Quality <= 61 || c.QueueDelay() > 100*time.Millisecond || cost > budget*3 {
			width := c.Width
			if width == 0 || width > sourceWidth {
				width = sourceWidth
			}
			c.Width = max(min(640, sourceWidth), width*4/5)
		}
		c.lastChange = now
		c.bad = 0
		c.good = 0
	} else if c.good >= 150 && c.QueueDelay() < 15*time.Millisecond && cost < budget {
		c.Quality = min(c.maxQuality, c.Quality+2)
		limit := c.maxWidth
		if limit == 0 || limit > sourceWidth {
			limit = sourceWidth
		}
		if c.Width > 0 {
			c.Width = min(limit, c.Width+160)
		}
		c.lastChange = now
		c.good = 0
	}
}
