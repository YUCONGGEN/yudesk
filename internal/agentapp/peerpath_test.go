package agentapp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/approval"
	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/filetransfer"
	"github.com/yudesk/yudesk/internal/peerpath"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/secureconn"
)

type peerCountConn struct {
	net.Conn
	bytes atomic.Uint64
}

func (c *peerCountConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.bytes.Add(uint64(n))
	return n, err
}

func TestPeerPathAgentFileAndConsent(t *testing.T) {
	for _, mode := range []string{"control", "view"} {
		t.Run(mode, func(t *testing.T) {
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			options := peerpath.Options{STUNURLs: []string{}, Timeout: 3 * time.Second}
			a := &agent{id: secureconn.DeviceID(pub), privateKey: key, pin: "123456", allowControl: true, shareDir: t.TempDir(), approvals: approval.New(), quit: make(chan struct{}), peerOptions: &options, inputFactory: func() *inputSession {
				s := newInputSession()
				s.apply = func([]desktop.InputEvent) error { return nil }
				return s
			}}
			x, y := net.Pipe()
			relayWire := &peerCountConn{Conn: x}
			done := make(chan struct{})
			go func() { defer close(done); a.handle(relayWire) }()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			t.Cleanup(func() {
				_ = x.Close()
				_ = y.Close()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("agent leaked")
				}
			})
			secured, err := secureconn.Connect(y, a.id)
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				pc    *protocol.Conn
				route peerpath.Info
				err   error
			}
			connected := make(chan result, 1)
			pin := "123456"
			if mode == "view" {
				pin = ""
			}
			go func() {
				pc, _, route, err := peerpath.Authenticate(ctx, secured, map[string]any{"pin": pin, "mode": mode}, options)
				connected <- result{pc, route, err}
			}()
			if pin == "" {
				select {
				case <-a.approvals.Events():
				case <-ctx.Done():
					t.Fatal("no consent request")
				}
				select {
				case <-connected:
					t.Fatal("connected before approval")
				case <-time.After(30 * time.Millisecond):
				}
				pending := a.approvals.Pending()
				if pending == nil || pending.Mode != "view" {
					t.Fatal("wrong approval")
				}
				if err := a.approvals.Resolve(pending.ID, true); err != nil {
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
			defer r.pc.Close()
			if r.route.Mode != "p2p" {
				t.Fatalf("did not test direct path: %+v", r.route)
			}
			sequence := 0
			call := func(method string, params any, data []byte) protocol.Message {
				t.Helper()
				sequence++
				id := fmt.Sprint(sequence)
				p, _ := json.Marshal(params)
				if err := r.pc.WriteMessageContext(ctx, protocol.Message{Kind: "request", ID: id, Method: method, Params: p, Data: data}); err != nil {
					t.Fatal(err)
				}
				m, err := r.pc.ReadMessage()
				if err != nil || m.ID != id {
					t.Fatalf("%s: %v %+v", method, err, m)
				}
				return m
			}
			if m := call("ping", nil, nil); !m.OK {
				t.Fatal(m.Error)
			}
			before := relayWire.bytes.Load()
			data := bytes.Repeat([]byte{0, 71, 255}, 50000)
			m := call("file_upload_begin", map[string]any{"path": "直连测试.bin", "size": len(data)}, nil)
			if mode == "view" {
				if m.OK {
					t.Fatal("view-only gained file permission")
				}
			} else {
				if !m.OK {
					t.Fatal(m.Error)
				}
				id := m.Meta["id"]
				digest := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
				for offset := 0; offset < len(data); {
					end := min(len(data), offset+filetransfer.ChunkSize)
					part := data[offset:end]
					m = call("file_upload_chunk", map[string]any{"id": id, "offset": offset, "sha256": digest(part)}, part)
					if !m.OK {
						t.Fatal(m.Error)
					}
					offset = end
				}
				if m = call("file_upload_commit", map[string]any{"id": id, "sha256": digest(data)}, nil); !m.OK {
					t.Fatal(m.Error)
				}
				m = call("file_download_begin", map[string]any{"path": "直连测试.bin"}, nil)
				if !m.OK {
					t.Fatal(m.Error)
				}
				id = m.Meta["id"]
				var received []byte
				for len(received) < len(data) {
					m = call("file_download_chunk", map[string]any{"id": id, "offset": len(received)}, nil)
					if !m.OK || len(m.Data) == 0 {
						t.Fatal("empty/failed chunk", m.Error)
					}
					received = append(received, m.Data...)
				}
				if !bytes.Equal(received, data) {
					t.Fatal("direct file corrupted")
				}
				if relayWire.bytes.Load()-before > 4096 {
					t.Fatal("file bytes still traveled through relay")
				}
			}
			// Simulate the server closing its authorized data connection. Direct UDP
			// must stop too, without needing the direct route itself to fail.
			_ = x.Close()
			_ = r.pc.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, err := r.pc.ReadMessage(); err == nil {
				t.Fatal("direct survived relay closure")
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("agent kept direct alive after server close")
			}
		})
	}
}
