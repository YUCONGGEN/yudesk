package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/account"
)

func TestWaitingDisconnectClearsPresenceImmediately(t *testing.T) {
	store, err := account.Open(t.TempDir() + "/wait.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b := &broker{token: "fixture-token", devices: map[string]waiting{}, accounts: store}
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { b.handle(server); close(done) }()
	_, _ = fmt.Fprintln(client, `YU_RELAY/1 {"id":"test","role":"agent","token":"fixture-token"}`)
	reader := bufio.NewReader(client)
	if line, err := reader.ReadString('\n'); err != nil || line != "WAIT\n" {
		t.Fatal("not waiting", err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closed socket kept stale presence")
	}
	b.Lock()
	defer b.Unlock()
	if len(b.devices) != 0 {
		t.Fatal("disconnected device still ready")
	}
}

func TestWaitingReaderPreservesEarlyHandshakeAndBackpressure(t *testing.T) {
	a, b := net.Pipe()
	stream, done := watchWaitingConnection(a, bufio.NewReader(a))
	defer stream.Close()
	want := bytes.Repeat([]byte("encrypted bytes"), 8192)
	go func() { _, _ = b.Write(want); _ = b.Close() }()
	got, err := io.ReadAll(stream)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("waiting stream corrupted", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waiting reader leaked")
	}
}
