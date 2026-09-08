package viewerapp

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/secureconn"
)

// The fixture shares no screen and injects no real input. It completes an
// actual E2E handshake and protocol bootstrap, then lets the test drop only
// this ephemeral remote stream.
func sessionLifecyclePeer(t *testing.T) (address, id string, drop func()) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var once sync.Once
	drop = func() { once.Do(func() { close(stop) }) }
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		go func() {
			select {
			case <-stop:
				conn.Close()
			case <-done:
			}
		}()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err = bufio.NewReader(conn).ReadString('\n'); err != nil {
			return
		}
		if _, err = io.WriteString(conn, "OK\n"); err != nil {
			return
		}
		secured, err := secureconn.Accept(conn, key)
		if err != nil {
			return
		}
		pc := protocol.NewConn(secured)
		for {
			m, err := pc.ReadMessage()
			if err != nil {
				return
			}
			if m.Kind != "request" {
				continue
			}
			if err = pc.WriteMessage(protocol.Message{Kind: "response", ID: m.ID, OK: true, Meta: map[string]any{"name": "isolated test peer"}}); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		drop()
		l.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("test peer did not stop")
		}
	})
	return l.Addr().String(), secureconn.DeviceID(pub), drop
}

func waitSessionMode(t *testing.T, client *http.Client, target, want string) {
	t.Helper()
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		r, err := client.Get(strings.Replace(target, "/?", "/api/ui/mode?", 1))
		if err == nil {
			body, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if string(body) == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("UI did not reach %s", want)
}

func TestSessionLifecycleKeepsRemoteFailureSeparateFromAppExit(t *testing.T) {
	for _, action := range []string{"remote_close", "end_control", "exit", "parent_cancel", "window_close"} {
		t.Run(action, func(t *testing.T) {
			addr, id, drop := sessionLifecyclePeer(t)
			dir := t.TempDir()
			journal, err := openLifecycleJournal(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			const token = "session-lifecycle-test"
			h, err := newViewerHost("127.0.0.1:0", token, journal)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			config := viewerConfig{relayAddr: addr, deviceID: id, pin: "123456", ui: h, web: h.listener.Addr().String(), fps: 30, quality: 80}
			type result struct {
				o viewerSessionOutcome
				e error
			}
			done := make(chan result, 1)
			go func() { o, e := runViewerSession(config, dir, token); done <- result{o, e} }()
			client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
			defer client.CloseIdleConnections()
			target := viewerUIURL(config.web, token)
			waitSessionMode(t, client, target, "session")
			switch action {
			case "remote_close":
				drop()
			case "parent_cancel":
				h.cancel()
			case "window_close":
				h.window.onClose()
			default:
				path := "/api/disconnect?"
				if action == "exit" {
					path = "/api/exit?"
				}
				r, e := client.Post(strings.Replace(target, "/?", path, 1), "application/json", nil)
				if e != nil {
					t.Fatal(e)
				}
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
			}
			select {
			case r := <-done:
				if r.e != nil {
					t.Fatal(r.e)
				}
				want := action == "remote_close" || action == "end_control"
				if r.o.returnToLauncher != want {
					t.Fatalf("returnToLauncher=%v want %v", r.o.returnToLauncher, want)
				}
				if want && h.ctx.Err() != nil {
					t.Fatal("session stop killed local host")
				}
				if action == "remote_close" && r.o.message == "" {
					t.Fatal("no helpful disconnect message")
				}
				if !want && h.ctx.Err() == nil {
					t.Fatal("explicit app exit did not cancel host")
				}
			case <-time.After(4 * time.Second):
				t.Fatal("session did not complete")
			}
			data, err := os.ReadFile(filepath.Join(dir, "logs", "lifecycle.log"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), token) || strings.Contains(string(data), "123456") {
				t.Fatal("lifecycle journal leaked session credentials")
			}
		})
	}
}

func TestRemoteDropReturnsActualMainLoopToLauncher(t *testing.T) {
	addr, id, drop := sessionLifecyclePeer(t)
	dir := t.TempDir()
	config := viewerConfig{relayAddr: addr, deviceID: id, pin: "123456", web: "127.0.0.1:0", stateDir: dir, fps: 30, quality: 80}
	done := make(chan error, 1)
	go func() { done <- runViewer(config) }()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	var target string
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		data, err := os.ReadFile(viewerSessionPath(dir))
		if err == nil {
			target = strings.TrimSpace(string(data))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if target == "" {
		t.Fatal("session URL was not created")
	}
	defer func() {
		r, e := client.Post(strings.Replace(target, "/?", "/api/exit?", 1), "application/json", nil)
		if e == nil {
			r.Body.Close()
		}
	}()
	waitSessionMode(t, client, target, "session")
	drop()
	waitSessionMode(t, client, target, "launcher")
	select {
	case e := <-done:
		t.Fatalf("main loop exited after remote drop: %v", e)
	default:
	}
	r, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(body), "远程连接已断开") {
		t.Fatal("launcher did not explain remote disconnect")
	}
	r, err = client.Post(strings.Replace(target, "/?", "/api/exit?", 1), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("explicit exit did not end main loop")
	}
}
