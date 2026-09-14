package peerpath

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/yudesk/yudesk/internal/protocol"
)

type signal struct {
	SDP   string `json:"sdp,omitempty"`
	Ready bool   `json:"ready,omitempty"`
	Info
}

type signaling struct {
	base     *protocol.Conn
	read     func() (protocol.Message, error)
	ctx      context.Context
	deadline time.Time
}

func (s signaling) send(stage string, value signal) error {
	if err := s.base.SetWriteDeadline(s.deadline); err != nil {
		return err
	}
	p, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.base.WriteMessageContext(s.ctx, protocol.Message{Kind: "peerpath", Method: stage, Params: p})
}

func (s signaling) receive(stage string) (signal, error) {
	var value signal
	if err := s.base.SetReadDeadline(s.deadline); err != nil {
		return value, err
	}
	m, err := s.read()
	if err != nil {
		return value, err
	}
	if m.Kind != "peerpath" || m.Method != stage || m.ID != "" || m.OK || m.Error != "" || len(m.Data) != 0 || len(m.Meta) != 0 || len(m.Params) > 2*maxSDP {
		return value, errors.New("unexpected signaling message")
	}
	d := json.NewDecoder(bytes.NewReader(m.Params))
	d.DisallowUnknownFields()
	if err = d.Decode(&value); err != nil {
		return value, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return value, errors.New("trailing signaling data")
	}
	if len(value.Reason) > 64 {
		return value, errors.New("oversized signaling reason")
	}
	return value, nil
}

func (s signaling) exchange(role, stage string, local signal) (signal, error) {
	if role == "viewer" {
		if err := s.send(stage, local); err != nil {
			return signal{}, err
		}
		return s.receive(stage)
	}
	remote, err := s.receive(stage)
	if err != nil {
		return signal{}, err
	}
	return remote, s.send(stage, local)
}

// Negotiate requires successful application authorization and mutual p2pV1.
// role is "viewer" (offerer) or "agent" (answerer). read is the ONLY reader;
// nil means base.ReadMessage. A supplied callback may consume an approval
// reader's result channel, but MUST unblock when base closes/deadlines expire.
// On relay return, no internal goroutine reads base; the caller resumes read.
// On direct return, peerpath owns read for the tether until final.Close(). The
// caller must read application messages from final, and must keep base open.
// ctx is the session context and also owns the successful direct/tether lifetime.
// Standalone callers must EnableInterleaving on final if auth negotiated it.
// Any non-nil error terminates base; Info{} never claims a reusable fallback.
func Negotiate(ctx context.Context, base *protocol.Conn, role string, read func() (protocol.Message, error), options Options) (final *protocol.Conn, info Info, err error) {
	if base == nil {
		return nil, Info{}, fmt.Errorf("%w: nil base", ErrSignaling)
	}
	if role != "viewer" && role != "agent" {
		_ = base.Close()
		return nil, Info{}, fmt.Errorf("%w: invalid role", ErrSignaling)
	}
	if read == nil {
		read = base.ReadMessage
	}
	o := options.normalized()
	attemptCtx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	deadline := time.Now().Add(o.Timeout + signalGrace)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	s := signaling{base: base, read: read, ctx: ctx, deadline: deadline}
	stop := context.AfterFunc(ctx, func() {
		_ = base.SetDeadline(time.Now())
		_ = base.Close()
	})
	var a *attempt
	defer func() {
		stop()
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			if final != nil && final != base {
				_ = final.Close()
			} else {
				_ = base.Close()
			}
			final, info = nil, Info{}
			err = fmt.Errorf("%w: %w", ErrSignaling, err)
		}
		if a != nil && (err != nil || info.Mode == "relay") {
			a.close()
		}
	}()

	// Multi-STUN and TURN gathering gets enough time to expose all useful
	// candidates, while very short caller deadlines remain authoritative.
	gatherBudget := min(o.Timeout, min(1200*time.Millisecond, max(250*time.Millisecond, o.Timeout/4)))
	if len(o.STUNURLs) > 1 || len(o.ICEServers) != 0 {
		gatherBudget = min(o.Timeout, max(gatherBudget, 700*time.Millisecond))
	}
	local := signal{}
	remote := signal{}
	// Initialize the local UDP mux/NAT mapping before signaling. Viewer and
	// agent enter negotiation together, so PCP/UPnP discovery runs in parallel
	// instead of adding its latency twice in series.
	if attemptCtx.Err() != nil {
		local.Reason = "ice_timeout"
	} else {
		a, err = newAttempt(attemptCtx, o, gatherBudget)
		if err != nil {
			local.Reason = "rtc_unavailable"
		}
	}
	if role == "agent" {
		remote, err = s.receive("offer")
		if err != nil {
			return nil, Info{}, err
		}
		if err = validDescription(remote, len(o.ICEServers) != 0); err != nil {
			return nil, Info{}, err
		}
	}
	if role == "agent" && remote.SDP == "" {
		local.Reason = "peer_unavailable"
	} else if local.Reason == "" {
		if role == "agent" {
			if err = a.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: remote.SDP}); err != nil {
				return nil, Info{}, err
			}
		}
		local.SDP, local.Reason = a.description(attemptCtx, role == "viewer", gatherBudget)
		if local.SDP != "" && len(o.ICEServers) == 0 && !a.mapped && endpointDependentMapping(local.SDP) {
			local.SDP = ""
			local.Reason = "endpoint_dependent_nat"
		}
	}
	if role == "viewer" {
		if err = s.send("offer", local); err != nil {
			return nil, Info{}, err
		}
		remote, err = s.receive("answer")
		if err != nil {
			return nil, Info{}, err
		}
		if err = validDescription(remote, len(o.ICEServers) != 0); err != nil {
			return nil, Info{}, err
		}
		if remote.SDP != "" {
			if a == nil || local.SDP == "" {
				return nil, Info{}, errors.New("answer without an offer")
			}
			if err = a.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: remote.SDP}); err != nil {
				return nil, Info{}, err
			}
		}
	} else if err = s.send("answer", local); err != nil {
		return nil, Info{}, err
	}

	var stream *dataConn
	reason := local.Reason
	selected := Info{Reason: reason}
	if reason == "" {
		if remote.SDP == "" {
			reason = "peer_unavailable"
		} else {
			stream, selected = a.ready(attemptCtx, len(o.ICEServers) != 0)
			reason = selected.Reason
		}
	}
	if stream == nil {
		selected = Info{Reason: reason}
	}
	ownReady := signal{Ready: stream != nil, Info: selected}
	if ownReady.Ready && !o.pathV2 {
		ownReady.Info = Info{}
	}
	otherReady, err := s.exchange(role, "ready", ownReady)
	if err != nil {
		return nil, Info{}, err
	}
	if otherReady.SDP != "" || !validReady(otherReady, o.pathV2) {
		return nil, Info{}, errors.New("invalid ready state")
	}
	viewer, agent := ownReady, otherReady
	if role == "agent" {
		viewer, agent = otherReady, ownReady
	}
	info = Info{Mode: "p2p", Reason: "udp_direct"}
	if !viewer.Ready || !agent.Ready {
		info = Info{Mode: "relay", Reason: viewer.Reason}
		if info.Reason == "" || info.Reason == "peer_unavailable" {
			info.Reason = agent.Reason
		}
		if info.Reason == "" {
			info.Reason = "peer_unavailable"
		}
	} else if viewer.Mode == "udp-relay" || agent.Mode == "udp-relay" {
		info = Info{Mode: "udp-relay", Reason: "turn_udp"}
	} else if viewer.Reason == "ipv6_direct" && agent.Reason == "ipv6_direct" {
		info.Reason = "ipv6_direct"
	}
	// Both sides acknowledge the identical decision. A lost/malformed commit
	// closes the session; neither side unilaterally falls back after committing.
	commit, err := s.exchange(role, "commit", signal{Info: info})
	if err != nil {
		return nil, Info{}, err
	}
	if commit.Info != info || commit.Ready || commit.SDP != "" {
		return nil, Info{}, errors.New("inconsistent path commit")
	}
	if err = base.SetDeadline(time.Time{}); err != nil {
		return nil, Info{}, err
	}
	if info.Mode == "relay" {
		return base, info, nil
	}
	select {
	case <-a.failed:
		return nil, Info{}, ErrDirectLost
	default:
	}
	direct := newSessionConn(ctx, stream, a, base, read)
	final = protocol.NewConn(o.wrap(direct))
	return final, info, nil
}

func validDescription(v signal, allowRelay bool) error {
	if v.Ready || v.Mode != "" {
		return errors.New("invalid description state")
	}
	if v.SDP == "" {
		if !validReason(v.Reason) {
			return errors.New("invalid unavailable description")
		}
		return nil
	}
	if v.Reason != "" {
		return errors.New("description with failure reason")
	}
	return validateSDP(v.SDP, allowRelay)
}

func validReady(value signal, pathV2 bool) bool {
	if !pathV2 {
		if value.Mode != "" {
			return false
		}
		if value.Ready {
			return value.Reason == ""
		}
		return validReason(value.Reason)
	}
	if value.Ready {
		return (value.Mode == "p2p" && (value.Reason == "udp_direct" || value.Reason == "ipv6_direct")) || value.Mode == "udp-relay" && value.Reason == "turn_udp"
	}
	return value.Mode == "" && validReason(value.Reason)
}

func validReason(s string) bool {
	switch s {
	case "rtc_unavailable", "udp_unavailable", "peer_unavailable", "ice_timeout", "ice_failed", "endpoint_dependent_nat":
		return true
	}
	return false
}
