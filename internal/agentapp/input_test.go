package agentapp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yudesk/yudesk/internal/desktop"
)

func TestInputCancellationStopsRemainingBatch(t *testing.T) {
	s := newInputSession()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	s.apply = func([]desktop.InputEvent) error { calls++; cancel(); return nil }
	if err := s.handleContext(ctx, json.RawMessage(`{"events":[{"type":"move"},{"type":"move"},{"type":"move"}]}`)); err != context.Canceled {
		t.Fatal("batch ignored cancellation", err)
	}
	if calls != 1 {
		t.Fatalf("injected %d events after cancellation", calls)
	}
}

func TestInputDragCoordinatesAndRelease(t *testing.T) {
	s := newInputSession()
	s.geometry(3840, 2160)
	var events []desktop.InputEvent
	s.apply = func(batch []desktop.InputEvent) error { events = append(events, batch...); return nil }
	err := s.handle(json.RawMessage(`{"width":1280,"height":720,"events":[{"type":"move","x":0,"y":0},{"type":"down","button":1},{"type":"move","x":1279,"y":719},{"type":"key_down","key":"Control"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if events[2].X != 3839 || events[2].Y != 2159 {
		t.Fatalf("scaled drag %v", events)
	}
	s.release()
	if events[4].Type != "up" || events[4].Button != 1 || events[5].Type != "key_up" {
		t.Fatalf("stuck inputs not released %v", events)
	}
	s.release()
	if len(events) != 6 {
		t.Fatal("released unowned inputs")
	}
}
