package relay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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

func TestExtendedOKCarriesSessionRTCPolicyAndPreservesBufferedData(t *testing.T) {
	policy := &RTCPolicy{
		ICEServers: []ICEServer{
			{URLs: []string{"stun:relay.example:8233"}},
			{URLs: []string{"turn:relay.example:8254?transport=udp"}, Username: "temporary", Credential: "secret"},
		},
		DirectTimeoutMS: 4200,
		RelayEnabled:    true,
	}
	encoded, err := EncodeRTCPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadString('\n')
		_, _ = fmt.Fprintf(conn, "OK %s\napplication-bytes", encoded)
	}()
	conn, err := DialWithContext(context.Background(), ln.Addr().String(), DialOptions{}, Hello{Role: "viewer", ID: "test", PeerPathV2: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got := PeerRTCPolicy(conn)
	if got == nil || got.DirectTimeoutMS != policy.DirectTimeoutMS || !got.RelayEnabled || len(got.ICEServers) != 2 || got.ICEServers[1].Username != "temporary" {
		t.Fatalf("unexpected policy: %+v", got)
	}
	buffer := make([]byte, len("application-bytes"))
	if _, err = io.ReadFull(conn, buffer); err != nil || string(buffer) != "application-bytes" {
		t.Fatalf("buffered data lost: %q, %v", buffer, err)
	}
	got.DirectTimeoutMS = 9999
	if PeerRTCPolicy(conn).DirectTimeoutMS != policy.DirectTimeoutMS {
		t.Fatal("PeerRTCPolicy did not return a defensive copy")
	}
}
