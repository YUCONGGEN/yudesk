package agentapp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/systemaudio"
)

func TestAudioCaptureOnDemandStopsAndRestarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active, maxActive, starts atomic.Int32
	packets := make(chan protocol.Message, 10)
	a := &sessionAudio{ctx: ctx, capture: func(ctx context.Context, sink systemaudio.Sink) error {
		n := active.Add(1)
		defer active.Add(-1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		starts.Add(1)
		if err := sink(systemaudio.Format{SampleRate: 48000, Channels: 2, Bits: 16}, make([]byte, 3840)); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}, send: func(m protocol.Message) error { packets <- m; return nil }}
	if starts.Load() != 0 {
		t.Fatal("capture started before opt-in")
	}
	for range 4 {
		if err := a.start(); err != nil {
			t.Fatal(err)
		}
		select {
		case m := <-packets:
			if m.Method != "audio" || len(m.Data) != 3840 {
				t.Fatal("bad PCM packet")
			}
		case <-time.After(time.Second):
			t.Fatal("no packet")
		}
		if err := a.start(); err == nil {
			t.Fatal("duplicate capture accepted")
		}
		if err := a.stop(); err != nil {
			t.Fatal(err)
		}
		if active.Load() != 0 {
			t.Fatal("capture remains active after stop")
		}
	}
	if starts.Load() != 4 || maxActive.Load() != 1 {
		t.Fatal("native captures overlapped")
	}
}

func TestAudioCaptureSurfacesDeviceFailure(t *testing.T) {
	got := make(chan protocol.Message, 1)
	a := &sessionAudio{ctx: context.Background(), capture: func(context.Context, systemaudio.Sink) error { return errors.New("test output disconnected") }, send: func(m protocol.Message) error { got <- m; return nil }}
	if err := a.start(); err != nil {
		t.Fatal(err)
	}
	defer a.stop()
	select {
	case m := <-got:
		if m.Method != "audio_error" || m.Error != "test output disconnected" {
			t.Fatalf("%+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("missing error")
	}
}

func TestAudioCaptureDoesNotBlockOnSlowNetwork(t *testing.T) {
	produced, release, packet := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &sessionAudio{ctx: ctx, capture: func(ctx context.Context, sink systemaudio.Sink) error {
		for range 500 {
			if err := sink(systemaudio.Format{SampleRate: 48000, Channels: 2, Bits: 16}, make([]byte, 3840)); err != nil {
				return err
			}
		}
		close(produced)
		<-ctx.Done()
		return ctx.Err()
	}, send: func(protocol.Message) error {
		select {
		case packet <- struct{}{}:
		default:
		}
		<-release
		return nil
	}}
	if err := a.start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-produced:
	case <-time.After(time.Second):
		t.Fatal("capture blocked on network")
	}
	close(release)
	if err := a.stop(); err != nil {
		t.Fatal(err)
	}
}
