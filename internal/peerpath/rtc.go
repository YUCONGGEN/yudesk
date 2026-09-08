package peerpath

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/sdp/v3"
	"github.com/pion/stun/v4"
	"github.com/pion/webrtc/v4"
)

const channelLabel = "yudesk-peerpath-v1"

type attempt struct {
	pc        *webrtc.PeerConnection
	mu        sync.Mutex
	stream    *dataConn
	closed    bool
	opened    chan struct{}
	failed    chan struct{}
	failOnce  sync.Once
	closeOnce sync.Once
}

func newAttempt(o Options, gatherBudget time.Duration) (*attempt, error) {
	if len(o.STUNURLs) > 4 {
		return nil, errors.New("too many STUN URLs")
	}
	for _, url := range o.STUNURLs {
		u, err := stun.ParseURI(url)
		if len(url) > 256 || err != nil || u.Scheme != stun.SchemeTypeSTUN || u.Proto != stun.ProtoTypeUDP {
			return nil, errors.New("only UDP STUN URLs are allowed")
		}
	}
	var settings webrtc.SettingEngine
	settings.DetachDataChannels()
	settings.EnableDataChannelBlockWrite(true)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	settings.SetSTUNGatherTimeout(gatherBudget)
	settings.SetICETimeouts(4*time.Second, 6*time.Second, time.Second)
	settings.SetSCTPMaxReceiveBufferSize(bufferLimit)
	settings.SetSCTPMaxMessageSize(chunkSize)
	if o.configure != nil {
		o.configure(&settings)
	}
	config := webrtc.Configuration{}
	if len(o.STUNURLs) != 0 {
		config.ICEServers = []webrtc.ICEServer{{URLs: o.STUNURLs}}
	}
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(config)
	if err != nil {
		return nil, err
	}
	a := &attempt{pc: pc, opened: make(chan struct{}), failed: make(chan struct{})}
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			a.fail()
		}
	})
	// The single negotiated, ordered, fully reliable stream is created at both
	// ends. No remote-created channel can bypass its reliability configuration.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) { _ = dc.Close(); a.fail() })
	id, negotiated, ordered := uint16(0), true, true
	dc, err := pc.CreateDataChannel(channelLabel, &webrtc.DataChannelInit{ID: &id, Negotiated: &negotiated, Ordered: &ordered})
	if err != nil {
		a.close()
		return nil, err
	}
	dc.OnError(func(error) { a.fail() })
	dc.OnClose(a.fail)
	dc.OnOpen(func() {
		raw, err := dc.DetachWithDeadline()
		if err != nil {
			a.fail()
			return
		}
		stream := newDataConn(raw, dc)
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			_ = stream.Close()
			return
		}
		a.stream = stream
		close(a.opened)
		a.mu.Unlock()
	})
	return a, nil
}

func (a *attempt) fail() { a.failOnce.Do(func() { close(a.failed) }) }

func (a *attempt) close() {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		stream := a.stream
		a.mu.Unlock()
		if stream != nil {
			_ = stream.Close()
		}
		_ = a.pc.GracefulClose()
	})
}

func (a *attempt) description(ctx context.Context, offer bool, budget time.Duration) (string, string) {
	var desc webrtc.SessionDescription
	var err error
	if offer {
		desc, err = a.pc.CreateOffer(nil)
	} else {
		desc, err = a.pc.CreateAnswer(nil)
	}
	if err != nil {
		return "", "rtc_unavailable"
	}
	gathered := webrtc.GatheringCompletePromise(a.pc)
	if err = a.pc.SetLocalDescription(desc); err != nil {
		return "", "rtc_unavailable"
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-gathered:
	case <-timer.C:
	case <-ctx.Done():
		return "", "ice_timeout"
	}
	local := a.pc.LocalDescription()
	if local == nil {
		return "", "rtc_unavailable"
	}
	// Cap local candidates too; later gathering is never signaled. The STUN
	// gather timeout independently stops Pion's background gather operation.
	lines := strings.Split(local.SDP, "\r\n")
	kept, count := lines[:0], 0
	for _, line := range lines {
		if strings.HasPrefix(line, "a=candidate:") {
			// Pion emits a component-2 copy for compatibility even for SCTP.
			// A data-only transport needs just component 1 (no RTP/RTCP).
			fields := strings.Fields(line)
			if len(fields) > 1 && fields[1] != "1" {
				continue
			}
			count++
			if count > maxCandidates {
				continue
			}
		}
		kept = append(kept, line)
	}
	if count == 0 {
		return "", "udp_unavailable"
	}
	result := strings.Join(kept, "\r\n")
	if err = validateSDP(result); err != nil {
		return "", "rtc_unavailable"
	}
	return result, ""
}

// Validate before handing untrusted signaling to Pion. Fingerprints are
// authenticated by base's E2E encryption; Pion performs DTLS verification.
func validateSDP(raw string) error {
	if len(raw) == 0 || len(raw) > maxSDP {
		return errors.New("invalid SDP size")
	}
	var desc sdp.SessionDescription
	if err := desc.Unmarshal([]byte(raw)); err != nil {
		return err
	}
	if len(desc.MediaDescriptions) != 1 || desc.MediaDescriptions[0].MediaName.Media != "application" {
		return errors.New("expected one data-channel media section")
	}
	attrs := append(append([]sdp.Attribute(nil), desc.Attributes...), desc.MediaDescriptions[0].Attributes...)
	candidates, fingerprints := 0, 0
	for _, attr := range attrs {
		switch attr.Key {
		case "candidate":
			candidates++
			if candidates > maxCandidates || len(attr.Value) > 1024 {
				return errors.New("too many/oversized ICE candidates")
			}
			c, err := ice.UnmarshalCandidate(attr.Value)
			if err != nil {
				return fmt.Errorf("invalid ICE candidate: %w", err)
			}
			if !c.NetworkType().IsUDP() || (c.Type() != ice.CandidateTypeHost && c.Type() != ice.CandidateTypeServerReflexive) || net.ParseIP(c.Address()) == nil || c.Component() != 1 {
				return errors.New("only host/srflx UDP IP candidates are allowed")
			}
		case "fingerprint":
			fields := strings.Fields(attr.Value)
			if len(fields) != 2 || fields[0] != "sha-256" {
				return errors.New("invalid DTLS fingerprint")
			}
			bytes, err := hex.DecodeString(strings.ReplaceAll(fields[1], ":", ""))
			if err != nil || len(bytes) != 32 {
				return errors.New("invalid DTLS fingerprint")
			}
			fingerprints++
		}
	}
	if candidates == 0 || fingerprints == 0 {
		return errors.New("missing candidate or authenticated DTLS fingerprint")
	}
	return nil
}

func (a *attempt) ready(ctx context.Context) (*dataConn, string) {
	select {
	case <-a.opened:
	case <-a.failed:
		return nil, "ice_failed"
	case <-ctx.Done():
		return nil, "ice_timeout"
	}
	select {
	case <-a.failed:
		return nil, "ice_failed"
	default:
	}
	pair, err := a.pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local.Protocol != webrtc.ICEProtocolUDP || pair.Remote.Protocol != webrtc.ICEProtocolUDP || !directCandidate(pair.Local.Typ) || !directCandidate(pair.Remote.Typ) {
		return nil, "ice_failed"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stream, ""
}

func directCandidate(t webrtc.ICECandidateType) bool {
	// ICE can discover a peer-reflexive pair while probing signaled host/srflx
	// candidates. It is still direct UDP; relay candidates are never permitted.
	return t == webrtc.ICECandidateTypeHost || t == webrtc.ICECandidateTypeSrflx || t == webrtc.ICECandidateTypePrflx
}
