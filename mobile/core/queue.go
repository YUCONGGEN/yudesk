package core

import (
	"context"
	"encoding/json"
	"sync"
)

// Keep the existing 64-message limit. Only adjacent single hover moves with the
// same coordinate space may replace each other. Accepted button state persists
// after dequeue: a down already sent/applied still makes later moves a drag.
type queuedValue[T any] struct {
	value         T
	width, height int
}

type latestMoveQueue[T any] struct {
	mu         sync.Mutex
	values     [64]queuedValue[T]
	head, size int
	wake       chan struct{}
	buttons    uint8
}

func newLatestMoveQueue[T any]() *latestMoveQueue[T] {
	return &latestMoveQueue[T]{wake: make(chan struct{}, 1)}
}

// raw is present only for input events; release marks an explicit input reset.
func (q *latestMoveQueue[T]) offer(value T, raw []byte, release bool) bool {
	var b inputBatch
	valid := len(raw) > 0 && json.Unmarshal(raw, &b) == nil
	q.mu.Lock()
	defer q.mu.Unlock()
	width, height := 0, 0
	if valid && q.buttons == 0 && len(b.Events) == 1 && b.Events[0].Type == "move" {
		width, height = b.Width, b.Height
	}
	entry := queuedValue[T]{value, width, height}
	if q.size > 0 && width > 0 {
		tail := (q.head + q.size - 1) % len(q.values)
		if q.values[tail].width == width && q.values[tail].height == height {
			q.values[tail] = entry
			return true
		}
	}
	if q.size == len(q.values) {
		return false
	}
	q.values[(q.head+q.size)%len(q.values)] = entry
	q.size++
	if release {
		q.buttons = 0
	} else if valid {
		for _, ev := range b.Events {
			if ev.Button >= 1 && ev.Button <= 3 {
				switch ev.Type {
				case "down":
					q.buttons |= 1 << ev.Button
				case "up":
					q.buttons &^= 1 << ev.Button
				}
			}
		}
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *latestMoveQueue[T]) next(ctx context.Context) (T, bool) {
	var zero T
	for ctx.Err() == nil {
		q.mu.Lock()
		if q.size > 0 {
			value := q.values[q.head].value
			q.values[q.head] = queuedValue[T]{}
			q.head = (q.head + 1) % len(q.values)
			q.size--
			q.mu.Unlock()
			return value, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return zero, false
		case <-q.wake:
		}
	}
	return zero, false
}

func (q *latestMoveQueue[T]) clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	clear(q.values[:])
	q.head, q.size = 0, 0
	q.buttons = 0
}

func (q *latestMoveQueue[T]) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.size
}
