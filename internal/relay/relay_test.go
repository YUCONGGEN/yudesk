package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestCancelWhileWaitingForPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	waiting := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = bufio.NewReader(c).ReadString('\n')
		_, _ = fmt.Fprintln(c, "WAIT")
		close(waiting)
		_, _ = c.Read(make([]byte, 1))
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		c, err := DialWithContext(ctx, ln.Addr().String(), DialOptions{}, Hello{Role: "agent", ID: "test"})
		if c != nil {
			c.Close()
		}
		done <- err
	}()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("did not enter WAIT")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not close waiting connection")
	}
}

func TestStructuredAndLegacyRelayRejections(t *testing.T) {
	tests := []struct {
		line string
		code string
	}{
		{"ERR STOP administrator disconnected this device", "STOP"},
		{"ERR DISABLED device has been revoked", "DISABLED"},
		{"ERR LICENSE_REQUIRED device activation is required or has expired", "LICENSE_REQUIRED"},
		{"ERR device activation is required or has expired", "LICENSE_REQUIRED"},
		{"ERR device has been revoked", "DISABLED"},
		{"ERR another refusal", "DENIED"},
	}
	for _, test := range tests {
		err := parseRelayRejection(test.line)
		if got := RejectionCode(err); got != test.code {
			t.Errorf("%q code=%q want=%q", test.line, got, test.code)
		}
		var rejection *RejectionError
		if !errors.As(err, &rejection) {
			t.Errorf("%q did not return RejectionError", test.line)
		}
	}
}
