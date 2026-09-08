package agentapp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
)

func inputMove(x int) protocol.Message {
	return protocol.Message{Kind: "event", Method: "input", Params: json.RawMessage(fmt.Sprintf(`{"events":[{"type":"move","x":%d}]}`, x))}
}

func TestInputQueueCoalescesHoverButPreservesDragAndRequests(t *testing.T) {
	blocked, resume, applied := make(chan struct{}), make(chan struct{}), make(chan protocol.Message, 16)
	var once sync.Once
	q := newInputQueue(func(m protocol.Message) {
		once.Do(func() { close(blocked); <-resume })
		applied <- m
	})
	var release sync.Once
	defer func() { release.Do(func() { close(resume) }); q.stop() }()
	if !q.enqueue(inputMove(0)) {
		t.Fatal("first enqueue")
	}
	<-blocked
	for i := 1; i <= 1000; i++ {
		if !q.enqueue(inputMove(i)) {
			t.Fatal("hover flooded queue")
		}
	}
	down := protocol.Message{Kind: "event", Method: "input", Params: json.RawMessage(`{"events":[{"type":"down","button":1}]}`)}
	up := protocol.Message{Kind: "event", Method: "input", Params: json.RawMessage(`{"events":[{"type":"up","button":1}]}`)}
	legacy := inputMove(20)
	legacy.Kind = "request"
	legacy.ID = "legacy"
	tail := []protocol.Message{down, inputMove(3), inputMove(4), up, legacy, inputMove(30)}
	for _, m := range tail {
		if !q.enqueue(m) {
			t.Fatal("enqueue tail")
		}
	}
	release.Do(func() { close(resume) })
	want := append([]protocol.Message{inputMove(0), inputMove(1000)}, tail...)
	for _, expected := range want {
		select {
		case actual := <-applied:
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("input order changed: %+v != %+v", actual, expected)
			}
		case <-time.After(time.Second):
			t.Fatal("input stalled")
		}
	}
}

func TestInputQueueBoundedAndStopDiscardsPending(t *testing.T) {
	blocked, resume := make(chan struct{}), make(chan struct{})
	q := newInputQueue(func(protocol.Message) { close(blocked); <-resume })
	if !q.enqueue(inputMove(0)) {
		t.Fatal("enqueue")
	}
	<-blocked
	m := protocol.Message{Kind: "event", Method: "input_release"}
	for i := 0; i < inputQueueLimit; i++ {
		if !q.enqueue(m) {
			t.Fatal("premature overflow")
		}
	}
	if q.enqueue(m) {
		t.Fatal("unbounded queue")
	}
	stopped := make(chan struct{})
	go func() { q.stop(); close(stopped) }()
	// Wait until stop owns the state; shutdown must not execute pending events.
	until := time.Now().Add(time.Second)
	for {
		q.mu.Lock()
		stop := q.stopped
		q.mu.Unlock()
		if stop {
			break
		}
		if time.Now().After(until) {
			t.Fatal("stop stalled")
		}
		time.Sleep(time.Millisecond)
	}
	if q.enqueue(m) {
		t.Fatal("accepted input after stop")
	}
	select {
	case <-stopped:
		t.Fatal("did not wait for OS input")
	default:
	}
	close(resume)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop leaked worker")
	}
}
