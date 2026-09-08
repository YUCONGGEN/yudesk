// Package peerpath upgrades an authenticated, end-to-end encrypted relay to a
// reliable UDP WebRTC data channel. The relay remains the authorization tether.
package peerpath

import (
	"errors"
	"net"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	defaultTimeout = 3 * time.Second
	signalGrace    = 2 * time.Second
	heartbeatEvery = 2 * time.Second
	tetherTimeout  = 7 * time.Second
	maxSDP         = 32 << 10
	maxCandidates  = 32
	chunkSize      = 16 << 10
	bufferLimit    = 256 << 10
)

var (
	ErrSignaling  = errors.New("peerpath: signaling failed; session terminated")
	ErrDirectLost = errors.New("peerpath: direct connection lost; reconnect to negotiate a new session")
	ErrRelayLost  = errors.New("peerpath: relay authorization tether lost; session terminated")
)

type Options struct {
	// Nil uses DefaultOptions. An explicitly empty slice gathers LAN hosts only.
	// Only stun: URLs using UDP are allowed; TURN and TCP are never used.
	STUNURLs []string
	// Timeout bounds the ICE attempt, including gathering. Zero means 3 seconds.
	// Values above 10 seconds are capped.
	// Signaling has an additional 2-second allowance to agree on the result.
	Timeout time.Duration
	// Wrap instruments transports before protocol.NewConn. Authenticate wraps
	// the initial relay, and Negotiate wraps a successful direct connection.
	// Negotiate preserves the supplied base (and its buffered reader) on fallback:
	// callers using Negotiate alone should instrument base at its construction.
	// The wrapper must preserve Close and deadline semantics.
	Wrap func(net.Conn) net.Conn

	// Per-call fault injection; never global, so concurrent tests stay isolated.
	configure func(*webrtc.SettingEngine)
}

func DefaultOptions() Options {
	return Options{STUNURLs: []string{"stun:www.yucg.cn:8233"}, Timeout: defaultTimeout}
}

func (o Options) normalized() Options {
	if o.STUNURLs == nil {
		o.STUNURLs = DefaultOptions().STUNURLs
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	o.Timeout = min(o.Timeout, 10*time.Second)
	return o
}

type Info struct {
	Mode   string `json:"mode"`
	Reason string `json:"reason"`
}

func (o Options) wrap(c net.Conn) net.Conn {
	if o.Wrap != nil {
		return o.Wrap(c)
	}
	return c
}
