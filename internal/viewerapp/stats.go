package viewerapp

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type measuredConn struct {
	net.Conn
	received, sent atomic.Uint64
}

func (c *measuredConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.received.Add(uint64(n))
	return n, err
}
func (c *measuredConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.sent.Add(uint64(n))
	return n, err
}

type networkSnapshot struct {
	Transport       string  `json:"transport"`
	TransportReason string  `json:"transportReason,omitempty"`
	RTTMs           float64 `json:"rttMs"`
	ProbeOK         bool    `json:"probeOK"`
	ReceiveMbps     float64 `json:"receiveMbps"`
	SendMbps        float64 `json:"sendMbps"`
	ReceivedBytes   uint64  `json:"receivedBytes"`
	SentBytes       uint64  `json:"sentBytes"`
	FPS             float64 `json:"fps"`
	FrameAgeMs      int64   `json:"frameAgeMs"`
	Width, Height   int
	Quality         int     `json:"quality"`
	Error           string  `json:"error,omitempty"`
	DesktopWaiting  bool    `json:"desktopWaiting"`
	DesktopMessage  string  `json:"desktopMessage,omitempty"`
	InputError      string  `json:"inputError,omitempty"`
	Codec           string  `json:"codec"`
	CaptureMs       float64 `json:"captureMs"`
	ChangedTiles    int     `json:"changedTiles"`
	QueueMs         float64 `json:"queueMs"`
}

type networkStats struct {
	mu        sync.Mutex
	frames    uint64
	lastFrame time.Time
	value     networkSnapshot
}

func (s *networkStats) frame(meta map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames++
	s.lastFrame = time.Now()
	number := func(name string) int {
		if n, ok := meta[name].(float64); ok {
			return int(n)
		}
		return 0
	}
	s.value.Width, s.value.Height = number("width"), number("height")
	s.value.Quality = number("quality")
	s.value.Codec, _ = meta["codec"].(string)
	if s.value.Codec == "" {
		s.value.Codec = "jpeg"
	}
	s.value.CaptureMs, _ = meta["captureMs"].(float64)
	s.value.ChangedTiles = number("changedTiles")
	s.value.QueueMs, _ = meta["queueMs"].(float64)
	s.value.Error = ""
	s.value.DesktopWaiting, s.value.DesktopMessage = false, ""
}

func (s *networkStats) snapshot() networkSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.value
	if !s.lastFrame.IsZero() {
		v.FrameAgeMs = time.Since(s.lastFrame).Milliseconds()
	}
	return v
}

func (c *client) monitorNetwork(wire *measuredConn) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := time.Now()
	var rx, tx, frames uint64
	for {
		select {
		case <-c.closed:
			return
		case <-ticker.C:
		}
		started := time.Now()
		_, err := c.requestTimeout("ping", nil, nil, 3*time.Second)
		now := time.Now()
		elapsed := now.Sub(last).Seconds()
		nrx, ntx := wire.received.Load(), wire.sent.Load()
		c.stats.mu.Lock()
		v := &c.stats.value
		v.ProbeOK = err == nil
		if err == nil {
			v.RTTMs = float64(now.Sub(started).Microseconds()) / 1000
		}
		v.ReceiveMbps = float64(nrx-rx) * 8 / elapsed / 1e6
		v.SendMbps = float64(ntx-tx) * 8 / elapsed / 1e6
		v.FPS = float64(c.stats.frames-frames) / elapsed
		v.ReceivedBytes, v.SentBytes = nrx, ntx
		frames = c.stats.frames
		c.stats.mu.Unlock()
		rx, tx, last = nrx, ntx, now
	}
}
