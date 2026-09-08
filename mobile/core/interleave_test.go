package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/stream"
)

func TestAgentNegotiatedAndLegacyLargeFrames(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			e := testEngine(t)
			c, _ := rawPeer(t, e)
			if enabled {
				c.OfferInterleaving()
			}
			params, _ := json.Marshal(map[string]any{"pin": "123456", "mode": "control", "interleaveV1": enabled})
			if err := c.WriteMessage(protocol.Message{Kind: "request", Method: "auth", ID: "auth", Params: params}); err != nil {
				t.Fatal(err)
			}
			m, err := c.ReadMessage()
			if err != nil || !m.OK || m.Meta["interleaveV1"] != enabled {
				t.Fatal("capability negotiation", m, err)
			}
			if enabled {
				c.EnableInterleaving()
			}
			params, _ = json.Marshal(stream.Options{ProfileVersion: 1, FPS: 30, Quality: 75, FrameAck: true})
			if err = c.WriteMessage(protocol.Message{Kind: "request", Method: "stream_start", ID: "stream", Params: params}); err != nil {
				t.Fatal(err)
			}
			if m, err = c.ReadMessage(); err != nil || !m.OK {
				t.Fatal("stream", err)
			}
			data := jpegData(t, 2048, 1024)
			if len(data) <= 8<<10 {
				t.Fatal("fixture did not exercise fragmentation")
			}
			if err = e.SubmitJPEG(data, 2048, 1024); err != nil {
				t.Fatal(err)
			}
			if m, err = c.ReadMessage(); err != nil || m.Method != "frame" || !bytes.Equal(m.Data, data) {
				t.Fatal("frame integrity", m.Method, err)
			}
			if err = c.WriteMessage(protocol.Message{Kind: "request", Method: "ping", ID: "alive"}); err != nil {
				t.Fatal(err)
			}
			if m, err = c.ReadMessage(); err != nil || m.ID != "alive" || !m.OK {
				t.Fatal("post-frame ping", m.ID, err)
			}
		})
	}
}

type firstFrameWrite struct {
	net.Conn
	once sync.Once
	sent chan struct{}
}

func (c *firstFrameWrite) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.once.Do(func() { close(c.sent) })
	return n, err
}

func TestStreamCancelFinishesAlreadyStartedFragmentedFrame(t *testing.T) {
	e := testEngine(t)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	wire := &firstFrameWrite{Conn: a, sent: make(chan struct{})}
	w, r := protocol.NewConn(wire), protocol.NewConn(b)
	w.EnableInterleaving()
	r.EnableInterleaving()
	w.SetBulkRate(128 << 10)
	data := bytes.Repeat([]byte{71}, 32<<10)
	e.mu.Lock()
	e.frame = &Frame{Data: data, Revision: 1, Width: 640, Height: 480}
	e.mu.Unlock()
	streamCtx, stop := context.WithCancel(e.ctx)
	defer stop()
	done := make(chan struct{})
	go func() {
		e.sendFrames(streamCtx, e.ctx, w, make(chan string), stream.Options{FPS: 30}, "test")
		close(done)
	}()
	result := make(chan protocol.Message, 1)
	failure := make(chan error, 1)
	go func() {
		m, err := r.ReadMessage()
		if err != nil {
			failure <- err
		} else {
			result <- m
		}
	}()
	<-wire.sent
	stop()
	select {
	case m := <-result:
		if !bytes.Equal(m.Data, data) {
			t.Fatal("cancel corrupted frame")
		}
	case err := <-failure:
		t.Fatal("settings change broke session", err)
	case <-time.After(2 * time.Second):
		t.Fatal("frame did not finish")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop")
	}
	go func() { failure <- w.WriteMessage(protocol.Response("still-alive", nil, nil)) }()
	m, err := r.ReadMessage()
	if err != nil || m.ID != "still-alive" {
		t.Fatal("session not reusable", err)
	}
	if err := <-failure; err != nil {
		t.Fatal(err)
	}
}
