package protocol

import (
	"net"
	"testing"
)

func TestFramedMessageRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	left, right := NewConn(a), NewConn(b)
	want := Message{Kind: "request", ID: "42", Method: "upload", Params: []byte(`{"path":"a.txt"}`), Data: []byte("hello")}
	done := make(chan error, 1)
	go func() { done <- left.WriteMessage(want) }()
	got, err := right.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Method != want.Method || string(got.Data) != "hello" {
		t.Fatalf("round trip mismatch: %#v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
