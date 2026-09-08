package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/peerpath"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestMobileP2PControllerAndAgent(t *testing.T) {
	for _, pin := range []string{"123456", ""} {
		t.Run("pin-"+pin, func(t *testing.T) {
			e := testEngine(t)
			options := peerpath.Options{STUNURLs: []string{}, Timeout: 3 * time.Second}
			e.peerOptions = &options
			x, y := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); e.serveAgent(e.ctx, x) }()
			t.Cleanup(func() {
				_ = x.Close()
				_ = y.Close()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("mobile agent leaked")
				}
			})
			secured, err := secureconn.Connect(y, e.identity.ID)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			type result struct {
				s   *Session
				err error
			}
			connected := make(chan result, 1)
			go func() {
				s, err := openSessionWithOptions(ctx, secured, pin, true, options)
				connected <- result{s, err}
			}()
			if pin == "" {
				select {
				case <-e.approvals.Events():
				case <-ctx.Done():
					t.Fatal("missing consent")
				}
				pending := e.approvals.Pending()
				if pending == nil {
					t.Fatal("missing pending")
				}
				if err := e.approvals.Resolve(pending.ID, true); err != nil {
					t.Fatal(err)
				}
			}
			var r result
			select {
			case r = <-connected:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if r.err != nil {
				t.Fatal(r.err)
			}
			defer r.s.Close()
			var status map[string]any
			_ = json.Unmarshal([]byte(r.s.StatusJSON()), &status)
			if status["transport"] != "p2p" {
				t.Fatal("not direct", status)
			}
			data := jpegData(t, 2048, 1024)
			if err := e.SubmitJPEG(data, 2048, 1024); err != nil {
				t.Fatal(err)
			}
			until(t, func() bool { r.s.mu.Lock(); defer r.s.mu.Unlock(); return r.s.frame != nil })
			r.s.mu.Lock()
			frame := r.s.frame
			r.s.mu.Unlock()
			if !bytes.Equal(frame.Data, data) {
				t.Fatal("direct frame corrupted")
			}
			_ = x.Close()
			until(t, func() bool { r.s.mu.Lock(); defer r.s.mu.Unlock(); return r.s.closed })
		})
	}
}
