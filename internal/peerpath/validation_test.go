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
	a, err := newAttempt(context.Background(), hostOptions(), 100*time.Millisecond)
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
			if err := validateSDP(invalid, false); err == nil {
				t.Fatal("accepted invalid SDP")
			}
		})
	}
	fields := strings.Fields(candidate)
	fields[4] = "2001:db8::1"
	if err = validateSDP(strings.Replace(raw, candidate, strings.Join(fields, " "), 1), false); err != nil {
		t.Fatalf("IPv6 rejected: %v", err)
	}
	fields[4] = "untrusted.local"
	if err = validateSDP(strings.Replace(raw, candidate, strings.Join(fields, " "), 1), false); err == nil {
		t.Fatal("accepted remote DNS candidate")
	}
}

func TestEndpointDependentMappingDetection(t *testing.T) {
	base := "v=0\r\na=candidate:1 1 udp 1694498815 203.0.113.10 41000 typ srflx raddr 192.168.1.8 rport 52000\r\n"
	if endpointDependentMapping(base) {
		t.Fatal("one mapped endpoint was classified as endpoint-dependent")
	}
	symmetric := base + "a=candidate:2 1 udp 1694498814 203.0.113.10 41001 typ srflx raddr 192.168.1.8 rport 52000\r\n"
	if !endpointDependentMapping(symmetric) {
		t.Fatal("different public ports for one local endpoint were not detected")
	}
	differentInterfaces := base + "a=candidate:3 1 udp 1694498813 203.0.113.10 41001 typ srflx raddr 192.168.2.8 rport 52000\r\n"
	if endpointDependentMapping(differentInterfaces) {
		t.Fatal("different local interfaces were misclassified as symmetric NAT")
	}
}

func TestReadyStateCompatibilityAndV2Paths(t *testing.T) {
	tests := []struct {
		name   string
		value  signal
		pathV2 bool
		valid  bool
	}{
		{name: "v1-direct", value: signal{Ready: true}, valid: true},
		{name: "v1-fallback", value: signal{Info: Info{Reason: "ice_timeout"}}, valid: true},
		{name: "v1-rejects-v2-label", value: signal{Ready: true, Info: Info{Mode: "p2p", Reason: "udp_direct"}}},
		{name: "v2-ipv4", value: signal{Ready: true, Info: Info{Mode: "p2p", Reason: "udp_direct"}}, pathV2: true, valid: true},
		{name: "v2-ipv6", value: signal{Ready: true, Info: Info{Mode: "p2p", Reason: "ipv6_direct"}}, pathV2: true, valid: true},
		{name: "v2-turn-udp", value: signal{Ready: true, Info: Info{Mode: "udp-relay", Reason: "turn_udp"}}, pathV2: true, valid: true},
		{name: "v2-rejects-false-ready-path", value: signal{Info: Info{Mode: "p2p", Reason: "udp_direct"}}, pathV2: true},
		{name: "v2-rejects-unknown", value: signal{Ready: true, Info: Info{Mode: "quic", Reason: "unknown"}}, pathV2: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validReady(test.value, test.pathV2); got != test.valid {
				t.Fatalf("validReady=%v want %v for %+v", got, test.valid, test.value)
			}
		})
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
