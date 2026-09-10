package core

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

func testConference(t *testing.T) (*Conference, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	return &Conference{conn: client, reader: bufio.NewReader(client), done: make(chan struct{})}, server
}

func TestConferenceReadAndSend(t *testing.T) {
	conference, server := testConference(t)
	defer conference.Close()
	defer server.Close()
	go func() { _, _ = server.Write([]byte("{\"type\":\"welcome\",\"id\":\"self\"}\n")) }()
	message, err := conference.Read()
	if err != nil || !strings.Contains(message, `"type":"welcome"`) {
		t.Fatalf("Read() = %q, %v", message, err)
	}
	received := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(server).ReadString('\n')
		received <- line
	}()
	if err := conference.Send(`{"type":"state","microphone":true}`); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-received:
		if !strings.Contains(line, `"microphone":true`) {
			t.Fatalf("sent line = %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("conference send blocked")
	}
}

func TestConferenceRejectsUnknownOrOversizedMessages(t *testing.T) {
	conference, server := testConference(t)
	defer conference.Close()
	defer server.Close()
	if err := conference.Send(`{"type":"state","unexpected":true}`); err == nil {
		t.Fatal("unknown signaling field accepted")
	}
	if err := conference.Send(strings.Repeat("x", 65537)); err == nil {
		t.Fatal("oversized signaling message accepted")
	}
}

func TestConferenceCloseIsIdempotent(t *testing.T) {
	conference, server := testConference(t)
	defer server.Close()
	closed := 0
	conference.onClose = func() { closed++ }
	conference.Close()
	conference.Close()
	if closed != 1 {
		t.Fatalf("onClose called %d times", closed)
	}
	if err := conference.Send(`{"type":"ping"}`); err == nil {
		t.Fatal("send succeeded after close")
	}
}
