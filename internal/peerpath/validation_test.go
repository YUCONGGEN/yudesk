package peerpath

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
)

func TestSDPBoundsAndDirectOnlyCandidates(t *testing.T) {
	a, err := newAttempt(hostOptions(), 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	raw, reason := a.description(context.Background(), true, 100*time.Millisecond)
	if raw == "" {
		t.Fatalf("SDP unavailable: %s", reason)
	}
	var candidate string
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(line, "a=candidate:") {
			candidate = line
			break
		}
	}
	for name, invalid := range map[string]string{
		"SDP-size":        strings.Repeat("x", maxSDP+1),
		"candidate-count": raw + strings.Repeat(candidate+"\r\n", maxCandidates),
		"candidate-size":  strings.Replace(raw, candidate, candidate+" "+strings.Repeat("x", 1024), 1),
		"TURN":            strings.ReplaceAll(raw, "typ host", "typ relay"),
		"TCP":             strings.ReplaceAll(raw, " udp ", " tcp "),
		"fingerprint":     strings.ReplaceAll(raw, "fingerprint:sha-256", "fingerprint:sha-1"),
		"media":           strings.ReplaceAll(raw, "m=application", "m=video"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSDP(invalid); err == nil {
				t.Fatal("accepted invalid SDP")
			}
		})
	}
	fields := strings.Fields(candidate)
	fields[4] = "2001:db8::1"
	if err = validateSDP(strings.Replace(raw, candidate, strings.Join(fields, " "), 1)); err != nil {
		t.Fatalf("IPv6 rejected: %v", err)
	}
	fields[4] = "untrusted.local"
	if err = validateSDP(strings.Replace(raw, candidate, strings.Join(fields, " "), 1)); err == nil {
		t.Fatal("accepted remote DNS candidate")
	}
}

func TestNegotiationCancellationHasNoReaderLeft(t *testing.T) {
	a, b := encryptedPair(t)
	tracked := &instrumentedConn{Conn: a}
	base, peer := protocol.NewConn(tracked), protocol.NewConn(b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan outcome, 1)
	go func() {
		pc, info, err := Negotiate(ctx, base, "viewer", nil, hostOptions())
		done <- outcome{pc, info, err}
	}()
	if _, err := peer.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	cancel()
	result := await(t, done)
	if result.err == nil || !errors.Is(result.err, ErrSignaling) || result.c != nil || result.info != (Info{}) {
		t.Fatalf("cancelled negotiation: %+v", result)
	}
	if n := tracked.reads.Load(); n != 0 {
		t.Fatalf("%d negotiation readers leaked", n)
	}
	if _, err := peer.ReadMessage(); err == nil {
		t.Fatal("cancelled negotiation left base open")
	}
}
