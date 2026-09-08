package agentapp

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/filetransfer"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestEncryptedInterleavedAndLegacyFileRoundTrip(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			agent := &agent{id: secureconn.DeviceID(pub), privateKey: key, pin: "123456", allowControl: true, shareDir: t.TempDir()}
			a, b := net.Pipe()
			defer b.Close()
			_ = b.SetDeadline(time.Now().Add(8 * time.Second))
			done := make(chan struct{})
			go func() { agent.handle(a); close(done) }()
			defer func() {
				b.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("agent leaked")
				}
			}()
			secure, err := secureconn.Connect(b, agent.id)
			if err != nil {
				t.Fatal(err)
			}
			c := protocol.NewConn(secure)
			if enabled {
				c.OfferInterleaving()
			}
			seq := 0
			call := func(method string, params map[string]any, data []byte) protocol.Message {
				t.Helper()
				seq++
				id := fmt.Sprint(seq)
				raw, _ := json.Marshal(params)
				if err := c.WriteMessage(protocol.Message{Kind: "request", Method: method, ID: id, Params: raw, Data: data}); err != nil {
					t.Fatal(method, err)
				}
				m, err := c.ReadMessage()
				if err != nil || !m.OK || m.ID != id {
					t.Fatalf("%s: %+v %v", method, m, err)
				}
				return m
			}
			m := call("auth", map[string]any{"pin": "123456", "mode": "control", "interleaveV1": enabled}, nil)
			if m.Meta["interleaveV1"] != enabled {
				t.Fatal("wrong negotiated capability")
			}
			if enabled {
				c.EnableInterleaving()
			}
			c.SetBulkRate(1_000_000)
			data := bytes.Repeat([]byte{0, 71, 0xff}, 50000)
			digest := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
			m = call("file_upload_begin", map[string]any{"path": "分片测试.bin", "size": len(data)}, nil)
			id := m.Meta["id"]
			for offset := 0; offset < len(data); {
				end := min(len(data), offset+filetransfer.ChunkSize)
				part := data[offset:end]
				call("file_upload_chunk", map[string]any{"id": id, "offset": offset, "sha256": digest(part)}, part)
				offset = end
				call("ping", nil, nil)
			}
			call("file_upload_commit", map[string]any{"id": id, "sha256": digest(data)}, nil)
			m = call("file_download_begin", map[string]any{"path": "分片测试.bin"}, nil)
			id = m.Meta["id"]
			var received []byte
			for len(received) < len(data) {
				m = call("file_download_chunk", map[string]any{"id": id, "offset": len(received)}, nil)
				if len(m.Data) == 0 {
					t.Fatal("empty chunk")
				}
				received = append(received, m.Data...)
				call("ping", nil, nil)
			}
			if !bytes.Equal(data, received) {
				t.Fatal("file corrupted")
			}
		})
	}
}
