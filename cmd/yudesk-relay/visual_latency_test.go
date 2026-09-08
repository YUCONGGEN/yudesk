package main

import (
	"crypto/ed25519"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

type delayedReadChunk struct {
	data []byte
	err  error
	due  time.Time
}
type delayedReadConn struct {
	net.Conn
	chunks     chan delayedReadChunk
	done       chan struct{}
	closeOnce  sync.Once
	pending    []byte
	pendingErr error
}

// Adds propagation delay by arrival time, not a sleep per Read call. It does
// not simulate packet loss, bandwidth limits or a real ISP connection.
func newDelayedReadConn(conn net.Conn, delay time.Duration) net.Conn {
	c := &delayedReadConn{Conn: conn, chunks: make(chan delayedReadChunk, 32), done: make(chan struct{})}
	go func() {
		for {
			buf := make([]byte, 64<<10)
			n, err := conn.Read(buf)
			chunk := delayedReadChunk{buf[:n], err, time.Now().Add(delay)}
			select {
			case c.chunks <- chunk:
			case <-c.done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}
func (c *delayedReadConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(c.pending) == 0 && c.pendingErr == nil {
		select {
		case chunk := <-c.chunks:
			if wait := time.Until(chunk.due); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-timer.C:
				case <-c.done:
					timer.Stop()
					return 0, net.ErrClosed
				}
			}
			c.pending, c.pendingErr = chunk.data, chunk.err
		case <-c.done:
			return 0, net.ErrClosed
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	if len(c.pending) == 0 {
		return n, c.pendingErr
	}
	return n, nil
}
func (c *delayedReadConn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return c.Conn.Close()
}

func TestNativeVisualLatency(t *testing.T) {
	helper, script := os.Getenv("YUDESK_TEST_AGENT_HELPER"), os.Getenv("YUDESK_VISUAL_TEST_SCRIPT")
	if helper == "" || script == "" {
		t.Skip("requires native helper, Viewer and headless browser")
	}
	t.Logf("relay runtime=%s; injected RTT values are timing targets, not exact link measurements", runtime.Version())
	for _, delay := range []time.Duration{0, 20 * time.Millisecond, 50 * time.Millisecond} {
		t.Run(fmt.Sprintf("added-rtt-%dms", 2*delay.Milliseconds()), func(t *testing.T) {
			f := newLifecycleFixture(t, delay)
			dir, id := f.prepareAgent()
			if err := f.b.accounts.RegisterLicensedDeviceNamed(id.ID, id.PrivateKey.Public().(ed25519.PublicKey), "visual-latency-fixture"); err != nil {
				t.Fatal(err)
			}
			f.admin("grant", id.ID)
			logfile, err := os.CreateTemp(f.root, "visual-agent-*.log")
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"-test.run=^TestVisualLatencyAgentHelper$", "-test.timeout=90s"}
			if profileDir := os.Getenv("YUDESK_VISUAL_PROFILE_DIR"); profileDir != "" {
				if err := os.MkdirAll(profileDir, 0700); err != nil {
					t.Fatal(err)
				}
				prefix := filepath.Join(profileDir, fmt.Sprintf("visual-%dms", 2*delay.Milliseconds()))
				args = append(args, "-test.cpuprofile="+prefix+".cpu", "-test.memprofile="+prefix+".mem")
			}
			cmd := exec.Command(helper, args...)
			cmd.Env = append(os.Environ(), "YUDESK_VISUAL_HELPER=1", "YUDESK_VISUAL_IDENTITY="+dir, "YUDESK_VISUAL_RELAY="+f.address, "YUDESK_VISUAL_FINGERPRINT="+f.fingerprint)
			cmd.Stdout, cmd.Stderr = logfile, logfile
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); logfile.Close(); close(done) }()
			t.Cleanup(func() {
				select {
				case <-done:
				default:
					_ = cmd.Process.Kill()
					<-done
				}
				if t.Failed() {
					data, _ := os.ReadFile(logfile.Name())
					t.Log(string(data))
				}
			})
			f.waitPairing(id.ID)
			viewerDir := filepath.Join(f.root, "viewer")
			v := f.connectViewer(viewerDir, id)
			base := f.page(filepath.Join(viewerDir, "viewer-session.url"))
			browser := exec.Command("node", script, base)
			browser.Env = append(os.Environ(), fmt.Sprintf("YUDESK_VISUAL_MIN_RTT_MS=%d", (2*delay).Milliseconds()))
			output, err := browser.CombinedOutput()
			if err != nil {
				t.Fatalf("visual latency measurement: %v %s", err, output)
			}
			t.Logf("synthetic desktop, added RTT %dms: %s", 2*delay.Milliseconds(), output)
			// Closing the only browser watch is itself the user-requested exit
			// action. Assert it actually exits instead of racing an extra POST
			// against an already closed HTTP listener (which falsely failed QA).
			v.exited(t)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("synthetic Agent did not exit after disconnect")
			}
		})
	}
}
