package protocol

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestQueuedWriteCancellationKeepsConnection(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	writer, reader := NewConn(a), NewConn(b)
	// Occupy only the writer gate, exactly as another frame/file write does.
	writer.write <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := writer.WriteMessageContext(ctx, Message{Kind: "event", Method: "input"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-writer.write
	done := make(chan error, 1)
	go func() { done <- writer.WriteMessage(Message{Kind: "request", ID: "healthy", Method: "ping"}) }()
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	m, err := reader.ReadMessage()
	if err != nil || m.ID != "healthy" {
		t.Fatalf("queued timeout damaged connection: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestActiveWriteCancellationClosesIncompleteRecord(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	writer := NewConn(a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := writer.WriteMessageContext(ctx, Message{Kind: "event", Method: "frame", Data: make([]byte, 100000)}); err == nil {
		t.Fatal("blocked write succeeded")
	}
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := b.Read(make([]byte, 1)); err == nil {
		t.Fatal("partially written stream stayed open")
	}
}
