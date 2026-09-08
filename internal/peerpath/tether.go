package peerpath

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
)

// sessionConn owns both paths. Once committed, any transport failure ends the
// session. It never resends application bytes over relay or changes their order.
type sessionConn struct {
	net.Conn
	ctx    context.Context
	a      *attempt
	base   *protocol.Conn
	done   chan struct{}
	closed chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
	err    error // published by closing done
}

func newSessionConn(ctx context.Context, stream *dataConn, a *attempt, base *protocol.Conn, read func() (protocol.Message, error)) *sessionConn {
	c := &sessionConn{Conn: stream, ctx: ctx, a: a, base: base, done: make(chan struct{}), closed: make(chan struct{})}
	c.wg.Add(3)
	go func() {
		defer c.wg.Done()
		for {
			if err := base.SetReadDeadline(time.Now().Add(tetherTimeout)); err != nil {
				c.end(fmt.Errorf("%w: %v", ErrRelayLost, err))
				return
			}
			m, err := read()
			if err != nil {
				c.end(fmt.Errorf("%w: %v", ErrRelayLost, err))
				return
			}
			if m.Kind != "peerpath" || m.Method != "alive" || m.ID != "" || len(m.Params) != 0 || len(m.Data) != 0 || len(m.Meta) != 0 || m.OK || m.Error != "" {
				c.end(fmt.Errorf("%w: unexpected management message", ErrRelayLost))
				return
			}
			select {
			case <-c.done:
				return
			default:
			}
		}
	}()
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(heartbeatEvery)
		defer ticker.Stop()
		for {
			if err := base.SetWriteDeadline(time.Now().Add(signalGrace)); err != nil {
				c.end(fmt.Errorf("%w: %v", ErrRelayLost, err))
				return
			}
			if err := base.WriteMessage(protocol.Message{Kind: "peerpath", Method: "alive"}); err != nil {
				c.end(fmt.Errorf("%w: %v", ErrRelayLost, err))
				return
			}
			select {
			case <-c.done:
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer c.wg.Done()
		select {
		case <-c.done:
		case <-ctx.Done():
			c.end(ctx.Err())
		case <-a.failed:
			c.end(ErrDirectLost)
		}
	}()
	return c
}

func (c *sessionConn) end(err error) {
	c.once.Do(func() {
		if c.ctx.Err() != nil {
			err = c.ctx.Err()
		}
		c.err = err
		close(c.done)
		// A worker never waits for itself. Close callers join cleanup, including
		// the tether reader and all Pion workers, through closed.
		go func() {
			_ = c.Conn.Close()
			// Release application I/O before a potentially slow TLS close_notify.
			_ = c.base.SetWriteDeadline(time.Now())
			_ = c.base.Close()
			c.a.close()
			c.wg.Wait()
			close(c.closed)
		}()
	})
}

func (c *sessionConn) terminal() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

func (c *sessionConn) Read(p []byte) (int, error) {
	if err := c.terminal(); err != nil {
		return 0, err
	}
	n, err := c.Conn.Read(p)
	return n, c.check(err)
}

func (c *sessionConn) Write(p []byte) (int, error) {
	if err := c.terminal(); err != nil {
		return 0, err
	}
	n, err := c.Conn.Write(p)
	return n, c.check(err)
}

func (c *sessionConn) check(err error) error {
	if terminal := c.terminal(); terminal != nil {
		return terminal
	}
	if err != nil {
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			c.end(fmt.Errorf("%w: %v", ErrDirectLost, err))
			return c.terminal()
		}
	}
	return err
}

func (c *sessionConn) Close() error {
	c.end(net.ErrClosed)
	<-c.closed
	return nil
}
