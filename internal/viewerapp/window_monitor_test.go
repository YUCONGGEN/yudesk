package viewerapp

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type monitorTestSocket struct {
	conn  *websocket.Conn
	write sync.Mutex
}

func (c *monitorTestSocket) send(v any) error {
	c.write.Lock()
	defer c.write.Unlock()
	return c.conn.WriteJSON(v)
}

type monitorFixture struct {
	window      *appWindow
	connections chan *monitorTestSocket
	closed      chan struct{}
	inventories atomic.Int32
	missing     atomic.Bool
	server      *httptest.Server
}

func newMonitorFixture(t *testing.T) *monitorFixture {
	t.Helper()
	f := &monitorFixture{connections: make(chan *monitorTestSocket, 32), closed: make(chan struct{}, 8)}
	var mu sync.Mutex
	var sockets []*monitorTestSocket
	const page = "http://127.0.0.1:15555/?access_token=monitor-test-token"
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		ws, e := up.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		c := &monitorTestSocket{conn: ws}
		mu.Lock()
		sockets = append(sockets, c)
		mu.Unlock()
		for {
			var command struct {
				ID     int
				Method string
			}
			if ws.ReadJSON(&command) != nil {
				return
			}
			result := map[string]any{}
			if command.Method == "Target.getTargets" {
				f.inventories.Add(1)
				var infos = []map[string]string{}
				if !f.missing.Load() {
					infos = append(infos, map[string]string{"targetId": "owned-target", "type": "page", "url": page})
				}
				result["targetInfos"] = infos
			}
			if c.send(map[string]any{"id": command.ID, "result": result}) != nil {
				return
			}
			if command.Method == "Target.setDiscoverTargets" {
				f.connections <- c
			}
		}
	}))
	f.window = newAppWindow("127.0.0.1:15555", "monitor-test-token")
	f.window.profile = t.TempDir()
	f.window.onClose = func() {
		select {
		case f.closed <- struct{}{}:
		default:
		}
	}
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(f.server.URL, "http://"))
	if e := os.WriteFile(filepath.Join(f.window.profile, "DevToolsActivePort"), []byte(fmt.Sprintf("%s\n/devtools/browser/test\n", port)), 0600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		f.window.mu.Lock()
		f.window.stopMonitor()
		f.window.mu.Unlock()
		f.server.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range sockets {
			c.conn.Close()
		}
	})
	return f
}

func (f *monitorFixture) socket(t *testing.T) *monitorTestSocket {
	t.Helper()
	select {
	case c := <-f.connections:
		return c
	case <-time.After(4 * time.Second):
		t.Fatal("monitor did not connect")
		return nil
	}
}

func (f *monitorFixture) expectOpen(t *testing.T, duration time.Duration) {
	t.Helper()
	select {
	case <-f.closed:
		t.Fatal("monitor loss or retirement closed app")
	case <-time.After(duration):
	}
}

func TestWindowMonitorReconnectsThenStillDetectsRealClose(t *testing.T) {
	f := newMonitorFixture(t)
	if e := f.window.monitor("owned-target"); e != nil {
		t.Fatal(e)
	}
	current := f.socket(t)
	for cycle := int32(1); cycle <= 3; cycle++ {
		current.conn.Close()
		current = f.socket(t)
		for until := time.Now().Add(time.Second); f.inventories.Load() < cycle && time.Now().Before(until); {
			time.Sleep(time.Millisecond)
		}
		if f.inventories.Load() < cycle {
			t.Fatal("recovery did not verify live target")
		}
		f.expectOpen(t, 30*time.Millisecond)
	}
	if e := current.send(map[string]any{"method": "Target.targetDestroyed", "params": map[string]string{"targetId": "another-window"}}); e != nil {
		t.Fatal(e)
	}
	f.expectOpen(t, 30*time.Millisecond)
	if e := current.send(map[string]any{"method": "Target.targetDestroyed", "params": map[string]string{"targetId": "owned-target"}}); e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("real close was lost after recovery")
	}
}

func TestWindowMonitorRetirementDoesNotCloseReplacement(t *testing.T) {
	f := newMonitorFixture(t)
	if e := f.window.monitor("owned-target"); e != nil {
		t.Fatal(e)
	}
	old := f.socket(t)
	old.conn.Close()
	f.window.mu.Lock()
	e := f.window.monitor("owned-target")
	f.window.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	current := f.socket(t)
	f.expectOpen(t, 250*time.Millisecond)
	current.send(map[string]any{"method": "Target.targetDestroyed", "params": map[string]string{"targetId": "owned-target"}})
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("replacement monitor not active")
	}
}

func TestWindowMonitorUnavailableIPCAloneDoesNotExit(t *testing.T) {
	f := newMonitorFixture(t)
	if e := f.window.monitor("owned-target"); e != nil {
		t.Fatal(e)
	}
	c := f.socket(t)
	f.server.Close()
	c.conn.Close()
	// No evidence of owned-process exit and no successful missing-target
	// inventory: an unavailable monitor alone must not terminate the client.
	f.expectOpen(t, 2500*time.Millisecond)
	f.window.mu.Lock()
	f.window.stopMonitor()
	f.window.mu.Unlock()
	f.expectOpen(t, 50*time.Millisecond)
}

func TestWindowMonitorConfirmsMissingTarget(t *testing.T) {
	f := newMonitorFixture(t)
	if e := f.window.monitor("owned-target"); e != nil {
		t.Fatal(e)
	}
	c := f.socket(t)
	f.missing.Store(true)
	c.conn.Close()
	select {
	case <-f.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("confirmed closed target ignored")
	}
	if f.inventories.Load() < 2 {
		t.Fatal("closed without independent target inventories")
	}
}

func TestWindowMonitorConfirmsOwnedProcessExit(t *testing.T) {
	f := newMonitorFixture(t)
	processDone := make(chan struct{})
	f.window.processDone = processDone
	if e := f.window.monitor("owned-target"); e != nil {
		t.Fatal(e)
	}
	c := f.socket(t)
	f.server.Close()
	close(processDone)
	c.conn.Close()
	select {
	case <-f.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closed browser process was left running as invisible app")
	}
}
