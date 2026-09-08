package core

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
)

func inputJSON(kind string, x, width int) string {
	return fmt.Sprintf(`{"events":[{"type":%q,"x":%d,"y":1,"button":1}],"width":%d,"height":100}`, kind, x, width)
}

func queuedSession(t *testing.T) *Session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Session{ctx: ctx, cancel: cancel, out: newLatestMoveQueue[protocol.Message](), changed: make(chan struct{}), control: true, frame: &Frame{Width: 20000, Height: 100}}
}

func TestControllerHoverBacklogKeepsClickEdgesAndNewestPosition(t *testing.T) {
	s := queuedSession(t)
	down, up := inputJSON("down", 1, 8192), inputJSON("up", 7999, 8192)
	for i := 0; i < 8000; i++ {
		if err := s.SendInputJSON(inputJSON("move", i, 8192)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SendInputJSON(down); err != nil {
		t.Fatal(err)
	}
	if err := s.SendInputJSON(up); err != nil {
		t.Fatal(err)
	}
	s.ReleaseInput()
	if s.out.len() != 4 {
		t.Fatalf("backlog = %d", s.out.len())
	}
	for _, want := range []string{inputJSON("move", 7999, 8192), down, up} {
		m, ok := s.out.next(s.ctx)
		if !ok || string(m.Params) != want {
			t.Fatalf("got %+v, want %s", m, want)
		}
	}
	m, _ := s.out.next(s.ctx)
	if m.Method != "input_release" {
		t.Fatal("release reordered")
	}
}

func TestControllerCriticalMessagesAndGeometryFenceMoves(t *testing.T) {
	s := queuedSession(t)
	messages := []protocol.Message{
		{Kind: "event", Method: "input", Params: json.RawMessage(inputJSON("move", 1, 100))},
		{Kind: "event", Method: "input", Params: json.RawMessage(inputJSON("move", 2, 200))},
		{Kind: "event", Method: "frame_ack", ID: "frame"},
		{Kind: "event", Method: "input", Params: json.RawMessage(inputJSON("move", 3, 200))},
		{Kind: "event", Method: "input", Params: json.RawMessage(inputJSON("key_down", 0, 200))},
		{Kind: "event", Method: "input", Params: json.RawMessage(inputJSON("move", 4, 200))},
		{Kind: "event", Method: "input", Params: json.RawMessage(inputJSON("key_up", 0, 200))},
		{Kind: "request", Method: "ping", ID: "ping"},
	}
	for _, m := range messages {
		if err := s.enqueue(m); err != nil {
			t.Fatal(err)
		}
	}
	if s.out.len() != len(messages) {
		t.Fatal("merged across a boundary")
	}
	for _, want := range messages {
		got, _ := s.out.next(s.ctx)
		if got.Method != want.Method || got.ID != want.ID || string(got.Params) != string(want.Params) {
			t.Fatal("reordered message")
		}
	}
}

func TestControllerOverflowDisconnectsAndViewModeCannotQueue(t *testing.T) {
	s := queuedSession(t)
	s.control = false
	if s.SendInputJSON(inputJSON("down", 1, 100)) == nil || s.out.len() != 0 {
		t.Fatal("view input queued")
	}
	s.control = true
	for i := 0; i < 64; i++ {
		if err := s.SendInputJSON(inputJSON("down", 1, 100)); err != nil {
			t.Fatal(err)
		}
	}
	if s.SendInputJSON(inputJSON("up", 1, 100)) == nil || !s.closed {
		t.Fatal("overflow did not fail closed")
	}
	if _, ok := s.out.next(s.ctx); ok {
		t.Fatal("canceled session drained stale input")
	}
}

func TestReceiverHoverReleaseAndPermissionBoundaries(t *testing.T) {
	e := testEngine(t)
	e.canInput = true
	e.incomingSession = 2
	down, up := inputJSON("down", 1, 8192), inputJSON("up", 7999, 8192)
	for i := 0; i < 8000; i++ {
		if err := e.queueInputForSession([]byte(inputJSON("move", i, 8192)), true, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.queueInputForSession([]byte(down), true, 2); err != nil {
		t.Fatal(err)
	}
	if err := e.queueInputForSession([]byte(up), true, 2); err != nil {
		t.Fatal(err)
	}
	if e.input.len() != 3 {
		t.Fatalf("backlog = %d", e.input.len())
	}
	for _, want := range []string{inputJSON("move", 7999, 8192), down, up} {
		if got := e.NextInputJSON(10); got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	}
	if err := e.queueInputForSession([]byte(down), true, 2); err != nil {
		t.Fatal(err)
	}
	e.SetAccessibility(false)
	if got := e.NextInputJSON(10); got != `{"release":true}` {
		t.Fatalf("revocation retained input: %s", got)
	}
	if e.queueInputForSession([]byte(down), true, 2) == nil {
		t.Fatal("revoked permission bypassed")
	}
	e.SetAccessibility(true)
	if e.queueInputForSession([]byte(down), true, 1) == nil {
		t.Fatal("stale session bypassed")
	}
	for i := 0; i < 64; i++ {
		if err := e.queueInput([]byte(down), true); err != nil {
			t.Fatal(err)
		}
	}
	if e.queueInput([]byte(up), true) == nil {
		t.Fatal("overflow accepted")
	}
	if got := e.NextInputJSON(10); got != `{"release":true}` || e.input.len() != 0 {
		t.Fatal("overflow did not release and clear")
	}
}

func TestMoveQueueConcurrentWrapAndCancellation(t *testing.T) {
	q := newLatestMoveQueue[int]()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000 && ctx.Err() == nil; i++ {
			for !q.offer(i, nil, false) && ctx.Err() == nil {
				runtime.Gosched()
			}
		}
	}()
	for i := 0; i < 10000; i++ {
		got, ok := q.next(ctx)
		if !ok || got != i {
			t.Fatalf("entry %d: %d, %v", i, got, ok)
		}
	}
	<-done
	cancel()
	if _, ok := q.next(ctx); ok {
		t.Fatal("cancellation ignored")
	}
}

func TestBothDirectionsPreserveEveryDragPointAfterDownDequeued(t *testing.T) {
	for _, receiver := range []bool{false, true} {
		t.Run(fmt.Sprint("receiver=", receiver), func(t *testing.T) {
			s := queuedSession(t)
			send := s.SendInputJSON
			next := func() string { m, _ := s.out.next(s.ctx); return string(m.Params) }
			if receiver {
				e := testEngine(t)
				e.canInput = true
				send = func(raw string) error { return e.queueInput([]byte(raw), true) }
				next = func() string { return e.NextInputJSON(100) }
			}
			// Native RemoteView sends down in a mixed [move, down] batch.
			down := `{"events":[{"type":"move","x":1,"y":1},{"type":"down","x":1,"y":1,"button":1}],"width":256,"height":256}`
			if err := send(down); err != nil {
				t.Fatal(err)
			}
			if next() != down {
				t.Fatal("down lost")
			}
			var path []string
			// Alternating vertices form a zigzag. Keeping only its endpoint
			// would draw a different line, even if down/up are retained.
			for i := 0; i < 48; i++ {
				raw := fmt.Sprintf(`{"events":[{"type":"move","x":%d,"y":%d}],"width":256,"height":256}`, 20+160*(i%2), 2+i*4)
				path = append(path, raw)
			}
			path = append(path, inputJSON("up", 180, 256))
			for _, raw := range path {
				if err := send(raw); err != nil {
					t.Fatal(err)
				}
			}
			for i, want := range path {
				if got := next(); got != want {
					t.Fatalf("vertex %d altered: %s", i, got)
				}
			}
			// Hover coalescing resumes only after the button is up.
			for i := 0; i < 10; i++ {
				if err := send(inputJSON("move", i, 256)); err != nil {
					t.Fatal(err)
				}
			}
			if got := next(); got != inputJSON("move", 9, 256) {
				t.Fatal("hover did not resume", got)
			}
		})
	}
}

func TestMoveQueueButtonMaskBatchesReleaseAndClear(t *testing.T) {
	q := newLatestMoveQueue[string]()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	push := func(raw string) {
		t.Helper()
		if !q.offer(raw, []byte(raw), false) {
			t.Fatal("unexpected overflow")
		}
	}
	pop := func(want string) {
		t.Helper()
		got, ok := q.next(ctx)
		if !ok || got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	downs := `{"events":[{"type":"down","button":1},{"type":"down","button":2}],"width":100,"height":100}`
	push(downs)
	pop(downs)
	up := inputJSON("up", 1, 100)
	push(up)
	pop(up) // Button 2 is still held after button 1 is released.
	for i := 0; i < 3; i++ {
		push(inputJSON("move", i, 100))
	}
	if q.len() != 3 {
		t.Fatal("second held button lost its path")
	}
	q.clear()
	batch := `{"events":[{"type":"move","x":1,"y":1},{"type":"move","x":2,"y":2}],"width":100,"height":100}`
	push(batch)
	push(batch)
	if q.len() != 2 {
		t.Fatal("multi-point batch was coalesced")
	}
	q.clear()
	push(downs)
	pop(downs)
	if !q.offer("release", nil, true) {
		t.Fatal("release refused")
	}
	for i := 0; i < 3; i++ {
		push(inputJSON("move", i, 100))
	}
	pop("release")
	pop(inputJSON("move", 2, 100))
}

func TestDragOverflowDoesNotSilentlyDropPathOrApplyRejectedUp(t *testing.T) {
	q := newLatestMoveQueue[string]()
	ctx := context.Background()
	down := inputJSON("down", 1, 100)
	q.offer(down, []byte(down), false)
	q.next(ctx)
	for i := 0; i < 64; i++ {
		raw := inputJSON("move", i, 100)
		if !q.offer(raw, []byte(raw), false) {
			t.Fatal("early overflow")
		}
	}
	up := inputJSON("up", 64, 100)
	if q.offer(up, []byte(up), false) {
		t.Fatal("unbounded drag queue")
	}
	q.next(ctx)
	move := inputJSON("move", 65, 100)
	if !q.offer(move, []byte(move), false) || q.len() != 64 {
		t.Fatal("rejected up reset held state")
	}
	q.clear()
	for i := 0; i < 1000; i++ {
		if !q.offer(move, []byte(move), false) {
			t.Fatal("reset did not clear held state")
		}
	}
	if q.len() != 1 {
		t.Fatal("hover did not resume after clear")
	}
}
