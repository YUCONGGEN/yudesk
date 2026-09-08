package peerpath

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v4"
)

type connDeadline struct {
	mu      sync.Mutex
	when    time.Time
	changed chan struct{}
}

func (d *connDeadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.when = t
	if d.changed != nil {
		close(d.changed)
	}
	d.changed = make(chan struct{})
}

func (d *connDeadline) snapshot() (time.Time, <-chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.changed == nil {
		d.changed = make(chan struct{})
	}
	return d.when, d.changed
}

// wait also covers callers queued for the read/write gate. Moving a deadline
// (including clearing it) wakes existing waits without spawning goroutines.
func wait(event, done <-chan struct{}, deadline *connDeadline) error {
	for {
		select {
		case <-done:
			return net.ErrClosed
		default:
		}
		when, changed := deadline.snapshot()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !when.IsZero() {
			if !time.Now().Before(when) {
				return os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(time.Until(when))
			timeout = timer.C
		}
		select {
		case <-done:
			if timer != nil {
				timer.Stop()
			}
			return net.ErrClosed
		case <-event:
			if timer != nil {
				timer.Stop()
			}
			return nil
		case <-changed:
			if timer != nil {
				timer.Stop()
			}
		case <-timeout:
			// Recheck the deadline: it might have been extended concurrently.
		}
	}
}

// dataConn converts reliable, ordered SCTP messages to a byte stream. It keeps
// one fragment of read tail, bounds outbound bytes and SCTP's receive window,
// and applies deadlines even while writers wait for space or another writer.
// There is no unbounded application receive queue or per-Read goroutine.
type dataConn struct {
	raw                 datachannel.ReadWriteCloserDeadliner
	dc                  *webrtc.DataChannel
	done                chan struct{}
	closeOnce           sync.Once
	readGate, writeGate chan struct{}
	drained             chan struct{}
	rd, wd              connDeadline
	buffer              [chunkSize]byte
	tail                []byte // owned by readGate
}

var _ net.Conn = (*dataConn)(nil)

func newDataConn(raw datachannel.ReadWriteCloserDeadliner, dc *webrtc.DataChannel) *dataConn {
	c := &dataConn{raw: raw, dc: dc, done: make(chan struct{}), readGate: make(chan struct{}, 1), writeGate: make(chan struct{}, 1), drained: make(chan struct{}, 1)}
	c.readGate <- struct{}{}
	c.writeGate <- struct{}{}
	dc.SetBufferedAmountLowThreshold(bufferLimit / 2)
	dc.OnBufferedAmountLow(func() {
		select {
		case c.drained <- struct{}{}:
		default:
		}
	})
	return c
}

func (c *dataConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := wait(c.readGate, c.done, &c.rd); err != nil {
		return 0, err
	}
	defer func() { c.readGate <- struct{}{} }()
	for len(c.tail) == 0 {
		n, text, err := c.raw.ReadDataChannel(c.buffer[:])
		if err != nil {
			return 0, c.ioError(err)
		}
		if text || n <= 0 || n > chunkSize {
			return 0, errors.New("peerpath: invalid data channel fragment")
		}
		c.tail = c.buffer[:n]
	}
	n := copy(p, c.tail)
	c.tail = c.tail[n:]
	return n, nil
}

func (c *dataConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := wait(c.writeGate, c.done, &c.wd); err != nil {
		return 0, err
	}
	defer func() { c.writeGate <- struct{}{} }()
	total := 0
	for len(p) != 0 {
		next := min(len(p), chunkSize)
		for c.dc.BufferedAmount()+uint64(next) > bufferLimit {
			if err := wait(c.drained, c.done, &c.wd); err != nil {
				return total, err
			}
		}
		select {
		case <-c.done:
			return total, net.ErrClosed
		default:
		}
		when, _ := c.wd.snapshot()
		if !when.IsZero() && !time.Now().Before(when) {
			return total, os.ErrDeadlineExceeded
		}
		n, err := c.raw.Write(p[:next])
		total += n
		if err != nil {
			return total, c.ioError(err)
		}
		if n != next {
			return total, io.ErrShortWrite
		}
		p = p[n:]
	}
	return total, nil
}

func (c *dataConn) ioError(err error) error {
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return os.ErrDeadlineExceeded
	}
	return err
}

func (c *dataConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.raw.SetReadDeadline(time.Now())
		_ = c.raw.SetWriteDeadline(time.Now())
		_ = c.raw.Close()
	})
	return nil
}

func (c *dataConn) SetReadDeadline(t time.Time) error {
	c.rd.set(t)
	return c.raw.SetReadDeadline(t)
}

func (c *dataConn) SetWriteDeadline(t time.Time) error {
	c.wd.set(t)
	return c.raw.SetWriteDeadline(t)
}

func (c *dataConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

type rtcAddr string

func (a rtcAddr) Network() string        { return "webrtc/udp" }
func (a rtcAddr) String() string         { return string(a) }
func (c *dataConn) LocalAddr() net.Addr  { return rtcAddr("local") }
func (c *dataConn) RemoteAddr() net.Addr { return rtcAddr("remote") }
