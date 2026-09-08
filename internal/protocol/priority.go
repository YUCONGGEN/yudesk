package protocol

import (
	"context"
	"sync"
)

// Short control messages may pass bulk fragments, with a bounded burst so a
// busy input/audio stream cannot permanently starve screen/file progress.
type priorityGate struct {
	mu        sync.Mutex
	held      bool
	burst     int
	high, low []*writeWaiter
}
type writeWaiter struct {
	ready   chan struct{}
	granted bool
}

func (g *priorityGate) acquire(ctx context.Context, high bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	if !g.held {
		g.held = true
		g.mu.Unlock()
		return nil
	}
	w := &writeWaiter{ready: make(chan struct{})}
	if high {
		g.high = append(g.high, w)
	} else {
		g.low = append(g.low, w)
	}
	g.mu.Unlock()
	select {
	case <-w.ready:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		granted := w.granted
		if !granted {
			queue := &g.low
			if high {
				queue = &g.high
			}
			for i, item := range *queue {
				if item == w {
					copy((*queue)[i:], (*queue)[i+1:])
					(*queue)[len(*queue)-1] = nil
					*queue = (*queue)[:len(*queue)-1]
					break
				}
			}
		}
		g.mu.Unlock()
		if granted {
			g.release()
		}
		return ctx.Err()
	}
}

func (g *priorityGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	queue := &g.low
	if len(g.high) > 0 && (g.burst < 8 || len(g.low) == 0) {
		queue = &g.high
		g.burst++
	} else {
		g.burst = 0
	}
	if len(*queue) == 0 {
		g.held = false
		g.burst = 0
		return
	}
	w := (*queue)[0]
	(*queue)[0] = nil
	*queue = (*queue)[1:]
	w.granted = true
	close(w.ready)
}
