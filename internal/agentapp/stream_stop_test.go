package agentapp

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestStreamReconfigurationClosesStuckWriter(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = a.Write([]byte("incomplete frame")) }()
	if waitStreamStopped(context.Background(), done, a, 20*time.Millisecond) {
		t.Fatal("blocked frame unexpectedly finished")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writer leaked after timeout")
	}
}
func TestStreamReconfigurationKeepsHealthyConnection(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan struct{})
	close(done)
	if !waitStreamStopped(context.Background(), done, a, time.Second) {
		t.Fatal("clean stream stop rejected")
	}
	written := make(chan error, 1)
	go func() { _, err := a.Write([]byte{1}); written <- err }()
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := b.Read(make([]byte, 1)); err != nil {
		t.Fatal("healthy connection closed", err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}
