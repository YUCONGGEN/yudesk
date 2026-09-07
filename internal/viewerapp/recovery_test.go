package viewerapp

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
)

func TestDesktopServiceRequiresExplicitInstallConfirmation(t *testing.T) {
	for _, path := range []string{"/api/local/service/install", "/api/local/service/remove"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			r := httptest.NewRequest(method, path, strings.NewReader(`{"confirmed":false}`))
			w := httptest.NewRecorder()
			d := &unifiedDesk{}
			if !d.serve(w, r) {
				t.Fatal("service route was not handled")
			}
			want := http.StatusBadRequest
			if method == http.MethodGet {
				want = http.StatusMethodNotAllowed
			}
			if w.Code != want {
				t.Fatalf("%s %s: got %d want %d", method, path, w.Code, want)
			}
		}
	}
}

func TestDesktopWaitingKeepsConnectionAndHidesStaleFrame(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := newClient(a)
	p := protocol.NewConn(b)
	for _, method := range []string{"stream_waiting", "stream_resumed"} {
		if err := p.WriteMessage(protocol.Message{Kind: "event", Method: method, Error: "test locked"}); err != nil {
			t.Fatal(err)
		}
		// A response following the event provides a deterministic read-loop barrier.
		barrier := make(chan responseResult, 1)
		c.pendingMu.Lock()
		c.pending[method] = barrier
		c.pendingMu.Unlock()
		if err := p.WriteMessage(protocol.Response(method, nil, nil)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-barrier:
		case <-time.After(time.Second):
			t.Fatal("reader stopped")
		}
		if !c.stats.snapshot().DesktopWaiting {
			t.Fatal("stale frame exposed")
		}
		select {
		case <-c.closed:
			t.Fatal("waiting ended session")
		default:
		}
	}
	c.stats.frame(map[string]any{"width": float64(640), "height": float64(480)})
	if s := c.stats.snapshot(); s.DesktopWaiting || s.DesktopMessage != "" {
		t.Fatalf("fresh frame did not resume: %+v", s)
	}
}
