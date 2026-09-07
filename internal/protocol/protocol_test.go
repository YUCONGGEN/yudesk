package protocol

import (
	"bytes"
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

type bufferedTestConn struct {
	net.Conn
	buf              bytes.Buffer
	writes, maxWrite int
}

func (c *bufferedTestConn) Read(p []byte) (int, error) { return c.buf.Read(p) }
func (c *bufferedTestConn) Write(p []byte) (int, error) {
	c.writes++
	if c.maxWrite > 0 && len(p) > c.maxWrite {
		p = p[:c.maxWrite]
	}
	return c.buf.Write(p)
}

func TestCoalescedMessageWritesPreserveFraming(t *testing.T) {
	for _, tc := range []struct{ size, maxWrite, writes int }{{800, 0, 1}, {100000, 0, 2}, {800, 7, 0}} {
		wire := &bufferedTestConn{maxWrite: tc.maxWrite}
		want := bytes.Repeat([]byte{43}, tc.size)
		if err := NewConn(wire).WriteMessage(Message{Kind: "event", Method: "tiles", Data: want}); err != nil {
			t.Fatal(err)
		}
		if tc.writes > 0 && wire.writes != tc.writes {
			t.Fatalf("size %d wrote %d times, want %d", tc.size, wire.writes, tc.writes)
		}
		got, err := NewConn(wire).ReadMessage()
		if err != nil || got.Method != "tiles" || !bytes.Equal(want, got.Data) {
			t.Fatalf("framing broken: %v", err)
		}
	}
}
