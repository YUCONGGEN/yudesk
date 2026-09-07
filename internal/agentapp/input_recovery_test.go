package agentapp

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func TestRejectedKeyDoesNotEndSessionOrPoisonMouse(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	moved := make(chan struct{}, 1)
	a := &agent{id: secureconn.DeviceID(pub), privateKey: key, pin: "test-pin", allowControl: true, inputFactory: func() *inputSession {
		s := newInputSession()
		s.apply = func(events []desktop.InputEvent) error {
			if events[0].Type == "key_down" {
				return desktop.ErrInputRejected
			}
			if events[0].Type == "move" {
				moved <- struct{}{}
			}
			return nil
		}
		return s
	}}
	left, right := net.Pipe()
	defer right.Close()
	_ = right.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan struct{})
	go func() { a.handle(left); close(done) }()
	secured, err := secureconn.Connect(right, a.id)
	if err != nil {
		t.Fatal(err)
	}
	c := protocol.NewConn(secured)
	write := func(m protocol.Message) {
		t.Helper()
		if err := c.WriteMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	write(protocol.Message{Kind: "request", ID: "auth", Method: "auth", Params: []byte(`{"pin":"test-pin","mode":"control"}`)})
	if m, err := c.ReadMessage(); err != nil || !m.OK {
		t.Fatal("auth failed", err)
	}
	write(protocol.Message{Kind: "event", Method: "input", Params: []byte(`{"events":[{"type":"key_down","key":"Unmapped"}]}`)})
	write(protocol.Message{Kind: "event", Method: "input", Params: []byte(`{"events":[{"type":"move","x":42,"y":30}]}`)})
	write(protocol.Message{Kind: "request", Method: "ping", ID: "alive"})
	seenError, seenRecovery := false, false
	for {
		m, err := c.ReadMessage()
		if err != nil {
			t.Fatal("input error killed session", err)
		}
		if m.Method == "input_error" {
			seenError = true
		}
		if m.Method == "input_ok" {
			seenRecovery = true
		}
		if m.ID == "alive" {
			break
		}
	}
	if !seenError || !seenRecovery {
		t.Fatal("missing input error/recovery notification")
	}
	select {
	case <-moved:
	default:
		t.Fatal("mouse stopped after rejected key")
	}
	_ = right.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session did not exit")
	}
}

func TestReleaseRetainsFailedKeysAndRetriesBeforeNextInput(t *testing.T) {
	s := newInputSession()
	failRelease := true
	var applied []desktop.InputEvent
	s.apply = func(events []desktop.InputEvent) error {
		e := events[0]
		if e.Type == "key_up" && failRelease {
			return errors.New("permission denied")
		}
		applied = append(applied, e)
		return nil
	}
	if err := s.handle([]byte(`{"events":[{"type":"key_down","key":"A","code":"KeyA"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.release(); err == nil || len(s.keys) != 1 {
		t.Fatal("failed release obligation lost")
	}
	failRelease = false
	if err := s.handle([]byte(`{"events":[{"type":"move","x":2,"y":3}]}`)); err != nil {
		t.Fatal(err)
	}
	if len(s.keys) != 0 || s.releasePending || len(applied) != 3 || applied[1].Code != "KeyA" || applied[1].Type != "key_up" || applied[2].Type != "move" {
		t.Fatalf("release order: %+v", applied)
	}
}
