package main

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/relay"
)

type delayedWaitConn struct {
	net.Conn
	entered chan struct{}
	release chan struct{}
}

func (c *delayedWaitConn) Write(data []byte) (int, error) {
	if string(data) == "WAIT\n" {
		close(c.entered)
		<-c.release
	}
	return c.Conn.Write(data)
}

func TestWaitingSignalCannotFollowPairedOK(t *testing.T) {
	store, err := account.Open(filepath.Join(t.TempDir(), "signals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b := &broker{token: "local-test", accounts: store, devices: map[string]waiting{}, active: map[string]activeSession{}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	go func() {
		for i := 0; i < 2; i++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if i == 0 {
				c = &delayedWaitConn{Conn: c, entered: entered, release: release}
			}
			go b.handle(c)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		c   net.Conn
		err error
	}
	id := strings.Repeat("A", 24)
	dial := func(role string) <-chan result {
		done := make(chan result, 1)
		go func() {
			c, err := relay.DialWithContext(ctx, ln.Addr().String(), relay.DialOptions{}, relay.Hello{Role: role, ID: id, Token: "local-test"})
			done <- result{c, err}
		}()
		return done
	}
	agentDone := dial("agent")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("agent did not enter WAIT")
	}
	viewerDone := dial("viewer")
	eventually(t, "pairing while WAIT is delayed", func() bool { b.Lock(); defer b.Unlock(); _, ok := b.active[id]; return ok })
	unblock()
	a, v := <-agentDone, <-viewerDone
	if a.err != nil || v.err != nil {
		t.Fatalf("pairing failed %v %v", a.err, v.err)
	}
	defer a.c.Close()
	defer v.c.Close()
	_ = a.c.SetReadDeadline(time.Now().Add(time.Second))
	go func() { _, _ = v.c.Write([]byte("encrypted-desktop")) }()
	buf := make([]byte, len("encrypted-desktop"))
	if _, err := io.ReadFull(a.c, buf); err != nil || string(buf) != "encrypted-desktop" {
		t.Fatalf("signalling leaked into application stream: %q %v", buf, err)
	}
}
