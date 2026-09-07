package uilifecycle

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Tracker closes Done after the last attached browser page has gone away.
// It is not armed until a page attaches, so headless runs keep their existing
// lifecycle. The grace period lets an ordinary page reload reconnect.
type Tracker struct {
	mu         sync.Mutex
	clients    int
	armed      bool
	generation uint64
	grace      time.Duration
	done       chan struct{}
	once       sync.Once
	onEmpty    func()
}

func New(grace time.Duration) *Tracker {
	return &Tracker{grace: grace, done: make(chan struct{})}
}

// Reusable close events allow a session window to close without terminating
// the application. An ordinary new page attachment arms the next close event.
func NewWithCallback(grace time.Duration, onEmpty func()) *Tracker {
	t := New(grace)
	t.onEmpty = onEmpty
	return t
}

func (t *Tracker) Done() <-chan struct{} { return t.done }

func (t *Tracker) Attached() bool { t.mu.Lock(); defer t.mu.Unlock(); return t.clients > 0 }

// Suspend disarms close detection for an intentional "hide to background"
// action. The next browser attachment arms the tracker again, so an ordinary
// window close after reopening still terminates the owning process.
func (t *Tracker) Suspend() {
	t.mu.Lock()
	t.armed = false
	t.generation++
	t.mu.Unlock()
}

func (t *Tracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprint(w, ": yudesk-ui\n\n")
	flusher.Flush()

	t.mu.Lock()
	t.clients++
	t.armed = true
	t.generation++
	t.mu.Unlock()

	defer t.detach()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.done:
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (t *Tracker) detach() {
	t.mu.Lock()
	if t.clients > 0 {
		t.clients--
	}
	t.generation++
	generation := t.generation
	shouldWait := t.armed && t.clients == 0
	t.mu.Unlock()
	if !shouldWait {
		return
	}
	time.AfterFunc(t.grace, func() {
		t.mu.Lock()
		shouldClose := t.armed && t.clients == 0 && t.generation == generation
		if shouldClose && t.onEmpty != nil {
			t.armed = false
			t.generation++
		}
		t.mu.Unlock()
		if shouldClose {
			if t.onEmpty != nil {
				t.onEmpty()
			} else {
				t.once.Do(func() { close(t.done) })
			}
		}
	})
}
