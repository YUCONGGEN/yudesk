package viewerapp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
)

func TestCancelledAudioListenerDoesNotAcquireCapture(t *testing.T) {
	l, r := net.Pipe()
	defer l.Close()
	defer r.Close()
	c := newClient(l)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 100 {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/audio", nil).WithContext(ctx)
		c.serveAudioOnDemand(w, req, false)
		if w.Flushed {
			t.Fatal("cancelled listener started audio response")
		}
	}
}

func TestAudioStreamSurvivesSilentIntervals(t *testing.T) {
	l, r := net.Pipe()
	defer l.Close()
	defer r.Close()
	c := newClient(l)
	s := httptest.NewServer(http.HandlerFunc(c.serveAudio))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	packet := make([]byte, 3840)
	for i := range 2 {
		if i > 0 {
			time.Sleep(2200 * time.Millisecond)
		}
		c.audio.publish(packet)
		if _, err := io.ReadFull(resp.Body, packet); err != nil {
			t.Fatalf("audio after silence: %v", err)
		}
	}
}

func TestAudioHubDropsIdleAndBoundsBacklog(t *testing.T) {
	h := newAudioHub()
	h.publish(make([]byte, 3840))
	if len(h.chunks) != 0 {
		t.Fatal("audio buffered without listener")
	}
	h.active = true
	for i := range 100 {
		h.publish(make([]byte, 3840+i*4))
	}
	if len(h.chunks) > 3 {
		t.Fatal("unbounded audio queue")
	}
	for len(h.chunks) > 0 {
		if len(<-h.chunks) > 3840 {
			t.Fatal("oversized packet")
		}
	}
	h.publish(make([]byte, 3))
	if h.errorMessage() == "" {
		t.Fatal("bad alignment accepted")
	}
}

func TestOnDemandAudioStopPrecedesNextStart(t *testing.T) {
	l, r := net.Pipe()
	defer l.Close()
	defer r.Close()
	c := newClient(l)
	p := protocol.NewConn(r)
	methods := make(chan string, 8)
	go func() {
		for {
			m, err := p.ReadMessage()
			if err != nil {
				return
			}
			methods <- m.Method
			if p.WriteMessage(protocol.Response(m.ID, nil, nil)) != nil {
				return
			}
		}
	}()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.serveAudioOnDemand(w, r, true) }))
	defer s.Close()
	for range 3 {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			t.Fatal(response.Status)
		}
		select {
		case got := <-methods:
			if got != "audio_start" {
				t.Fatal(got)
			}
		case <-time.After(time.Second):
			t.Fatal("audio did not start")
		}
		cancel()
		response.Body.Close()
		select {
		case got := <-methods:
			if got != "audio_stop" {
				t.Fatal(got)
			}
		case <-time.After(time.Second):
			t.Fatal("audio did not stop")
		}
	}
}
