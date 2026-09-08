package agentapp

import (
	"encoding/json"
	"sync"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/protocol"
)

const inputQueueLimit = 64
const inputParamsLimit = 64 << 10

type queuedInput struct {
	message protocol.Message
	hover   bool
}

// One input worker, not a goroutine per event. Network ACKs and capture do not
// wait on OS input. Queue overflow fails the session instead of dropping an up
// and leaving a remote button pressed. Shutdown discards pending operations,
// waits for the in-flight OS call, then the session releases its owned inputs.
type inputQueue struct {
	mu      sync.Mutex
	items   []queuedInput
	buttons uint8
	stopped bool
	wake    chan struct{}
	done    chan struct{}
}

func newInputQueue(apply func(protocol.Message)) *inputQueue {
	q := &inputQueue{wake: make(chan struct{}, 1), done: make(chan struct{}), items: make([]queuedInput, 0, inputQueueLimit)}
	go func() {
		defer close(q.done)
		for {
			q.mu.Lock()
			if q.stopped {
				q.mu.Unlock()
				return
			}
			if len(q.items) == 0 {
				q.mu.Unlock()
				<-q.wake
				continue
			}
			m := q.items[0].message
			copy(q.items, q.items[1:])
			q.items[len(q.items)-1] = queuedInput{}
			q.items = q.items[:len(q.items)-1]
			q.mu.Unlock()
			apply(m)
		}
	}()
	return q
}

func (q *inputQueue) enqueue(m protocol.Message) bool {
	if len(m.Params) > inputParamsLimit || len(m.Data) != 0 {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return false
	}
	// Only adjacent, single, absolute hover moves can replace one another.
	// Preserve every drag sample, key, wheel, release and legacy request.
	var p struct {
		Events []desktop.InputEvent `json:"events"`
	}
	valid := m.Method == "input" && json.Unmarshal(m.Params, &p) == nil
	hover := valid && q.buttons == 0 && m.Kind == "event" && len(p.Events) == 1 && p.Events[0].Type == "move"
	if hover && len(q.items) > 0 && q.items[len(q.items)-1].hover {
		q.items[len(q.items)-1] = queuedInput{m, true}
		return true
	}
	if len(q.items) == inputQueueLimit {
		return false
	}
	if m.Method == "input_release" {
		q.buttons = 0
	} else if valid {
		for _, event := range p.Events {
			if event.Button >= 1 && event.Button <= 3 {
				switch event.Type {
				case "down":
					q.buttons |= 1 << event.Button
				case "up":
					q.buttons &^= 1 << event.Button
				}
			}
		}
	}
	q.items = append(q.items, queuedInput{m, hover})
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *inputQueue) stop() {
	q.mu.Lock()
	q.stopped = true
	clear(q.items)
	q.items = nil
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	<-q.done
}
