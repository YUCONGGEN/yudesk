package peerpath

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/sdp/v3"
	"github.com/pion/stun/v4"
	"github.com/pion/webrtc/v4"
	"github.com/yudesk/yudesk/internal/natmap"
)

const channelLabel = "yudesk-peerpath-v1"

type attempt struct {
	pc         *webrtc.PeerConnection
	allowRelay bool
	udpMux     ice.UDPMux
	mapping    natmap.Lease
	mapped     bool
	mu         sync.Mutex
	stream     *dataConn
	closed     bool
	opened     chan struct{}
	failed     chan struct{}
	failOnce   sync.Once
	closeOnce  sync.Once
}

func newAttempt(ctx context.Context, o Options, gatherBudget time.Duration) (*attempt, error) {
	if len(o.STUNURLs) > 8 || len(o.ICEServers) > 4 {
		return nil, errors.New("too many STUN URLs")
	}
	for _, url := range o.STUNURLs {
		u, err := stun.ParseURI(url)
		if len(url) > 256 || err != nil || u.Scheme != stun.SchemeTypeSTUN || u.Proto != stun.ProtoTypeUDP {
			return nil, errors.New("only UDP STUN URLs are allowed")
		}
	}
	for _, server := range o.ICEServers {
		if len(server.URLs) == 0 || len(server.URLs) > 4 || server.Username == "" || server.Credential == nil {
			return nil, errors.New("invalid authenticated TURN server")
		}
		for _, url := range server.URLs {
			u, err := stun.ParseURI(url)
			if len(url) > 512 || err != nil || u.Scheme != stun.SchemeTypeTURN || u.Proto != stun.ProtoTypeUDP {
				return nil, errors.New("only authenticated UDP TURN URLs are allowed")
			}
		}
	}
	var settings webrtc.SettingEngine
	settings.DetachDataChannels()
	settings.EnableDataChannelBlockWrite(true)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	settings.SetSTUNGatherTimeout(gatherBudget)
	disconnectedTimeout := min(4*time.Second, max(2*time.Second, o.Timeout))
	failedTimeout := max(6*time.Second, o.Timeout+time.Second)
	settings.SetICETimeouts(disconnectedTimeout, failedTimeout, time.Second)
	settings.SetSCTPMaxReceiveBufferSize(bufferLimit)
	settings.SetSCTPMaxMessageSize(chunkSize)
	var udpMux ice.UDPMux
	var mappingLease natmap.Lease
	if o.PortMapping {
		var udpSocket *net.UDPConn
		var listenErr error
		udpMux, udpSocket, listenErr = newMappedUDPMux()
		if listenErr == nil {
			settings.SetICEUDPMux(udpMux)
			mapBudget := min(gatherBudget, 900*time.Millisecond)
			mapCtx, cancelMap := context.WithTimeout(ctx, mapBudget)
			localPort := uint16(udpSocket.LocalAddr().(*net.UDPAddr).Port)
			mapping, mapErr := natmap.OpenUDP(mapCtx, udpSocket, natmap.Options{Timeout: mapBudget, Lifetime: 2 * time.Hour, ExternalPort: localPort, Description: "YuDesk P2P"})
			cancelMap()
			if mapErr == nil && mapping.ExternalPort == localPort && usableMappedAddress(mapping.ExternalIP) {
				mappingLease = mapping.Lease
				if rewriteErr := settings.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
					// Publish the mapped endpoint as an appended host candidate so
					// Pion keeps the UDPMux socket/port that the router actually
					// mapped. A synthetic srflx rewrite opens another random socket.
					External: []string{mapping.ExternalIP.String()}, AsCandidateType: webrtc.ICECandidateTypeHost,
					Mode: webrtc.ICEAddressRewriteAppend, Networks: []webrtc.NetworkType{webrtc.NetworkTypeUDP4},
				}); rewriteErr != nil {
					_ = mappingLease.Close()
					mappingLease = nil
				}
			} else if mapErr == nil {
				_ = mapping.Lease.Close()
			}
		}
	}
	if o.configure != nil {
		o.configure(&settings)
	}
	config := webrtc.Configuration{ICEServers: append([]webrtc.ICEServer(nil), o.ICEServers...)}
	if len(o.STUNURLs) != 0 {
		config.ICEServers = append([]webrtc.ICEServer{{URLs: o.STUNURLs}}, config.ICEServers...)
	}
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(config)
	if err != nil {
		if mappingLease != nil {
			_ = mappingLease.Close()
		}
		if udpMux != nil {
			_ = udpMux.Close()
		}
		return nil, err
	}
	a := &attempt{pc: pc, allowRelay: len(o.ICEServers) != 0, udpMux: udpMux, mapping: mappingLease, mapped: mappingLease != nil, opened: make(chan struct{}), failed: make(chan struct{})}
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
		if a.udpMux != nil {
			_ = a.udpMux.Close()
		}
		if a.mapping != nil {
			_ = a.mapping.Close()
		}
	})
}

func newMappedUDPMux() (ice.UDPMux, *net.UDPConn, error) {
	udp4, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return nil, nil, err
	}
	port := udp4.LocalAddr().(*net.UDPAddr).Port
	muxes := []ice.UDPMux{ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: udp4})}
	seen := map[string]bool{}
	if addresses, listErr := net.InterfaceAddrs(); listErr == nil {
		for _, value := range addresses {
			if len(muxes) >= 9 {
				break
			}
			prefix, parseErr := netip.ParsePrefix(value.String())
			if parseErr != nil {
				continue
			}
			ip := prefix.Addr().Unmap()
			if !ip.Is6() || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			udp6, listenErr := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IP(ip.AsSlice()), Port: port})
			if listenErr == nil {
				muxes = append(muxes, ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: udp6}))
			}
		}
	}
	return ice.NewMultiUDPMuxDefault(muxes...), udp4, nil
}

func usableMappedAddress(address netip.Addr) bool {
	address = address.Unmap()
	return address.IsValid() && address.Is4() && !address.IsUnspecified() && !address.IsLoopback() && !address.IsMulticast()
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
	if err = validateSDP(result, a.allowRelay); err != nil {
		return "", "rtc_unavailable"
	}
	return result, ""
}

// Validate before handing untrusted signaling to Pion. Fingerprints are
// authenticated by base's E2E encryption; Pion performs DTLS verification.
func validateSDP(raw string, allowRelay bool) error {
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
			allowedType := c.Type() == ice.CandidateTypeHost || c.Type() == ice.CandidateTypeServerReflexive || allowRelay && c.Type() == ice.CandidateTypeRelay
			if !c.NetworkType().IsUDP() || !allowedType || net.ParseIP(c.Address()) == nil || c.Component() != 1 {
				return errors.New("only authorized UDP ICE candidates are allowed")
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

func (a *attempt) ready(ctx context.Context, allowRelay bool) (*dataConn, Info) {
	select {
	case <-a.opened:
	case <-a.failed:
		return nil, Info{Reason: "ice_failed"}
	case <-ctx.Done():
		return nil, Info{Reason: "ice_timeout"}
	}
	select {
	case <-a.failed:
		return nil, Info{Reason: "ice_failed"}
	default:
	}
	pair, err := a.pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local.Protocol != webrtc.ICEProtocolUDP || pair.Remote.Protocol != webrtc.ICEProtocolUDP || !usableCandidate(pair.Local.Typ, allowRelay) || !usableCandidate(pair.Remote.Typ, allowRelay) {
		return nil, Info{Reason: "ice_failed"}
	}
	info := Info{Mode: "p2p", Reason: "udp_direct"}
	if pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
		info = Info{Mode: "udp-relay", Reason: "turn_udp"}
	} else if isIPv6Candidate(pair.Local.Address) && isIPv6Candidate(pair.Remote.Address) {
		info.Reason = "ipv6_direct"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stream, info
}

func usableCandidate(t webrtc.ICECandidateType, allowRelay bool) bool {
	// ICE can discover a peer-reflexive pair while probing signaled host/srflx
	// candidates. It is still direct UDP. Relay is permitted only when a
	// session-bound TURN policy was delivered by the authenticated relay.
	return t == webrtc.ICECandidateTypeHost || t == webrtc.ICECandidateTypeSrflx || t == webrtc.ICECandidateTypePrflx || allowRelay && t == webrtc.ICECandidateTypeRelay
}

func isIPv6Candidate(address string) bool {
	ip := net.ParseIP(strings.Trim(address, "[]"))
	return ip != nil && ip.To4() == nil
}

// endpointDependentMapping reports the common symmetric-NAT signature: the
// same local UDP endpoint received different public ports from multiple STUN
// destinations. Without UPnP/PCP or TURN, waiting the full ICE timeout cannot
// make that candidate reachable and only delays the TCP fallback.
func endpointDependentMapping(raw string) bool {
	ports := map[string]map[int]bool{}
	for _, line := range strings.Split(raw, "\r\n") {
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		candidate, err := ice.UnmarshalCandidate(strings.TrimPrefix(line, "a=candidate:"))
		if err != nil || candidate.Type() != ice.CandidateTypeServerReflexive || candidate.RelatedAddress() == nil {
			continue
		}
		related := candidate.RelatedAddress()
		key := related.Address + ":" + fmt.Sprint(related.Port)
		if ports[key] == nil {
			ports[key] = map[int]bool{}
		}
		ports[key][candidate.Port()] = true
		if len(ports[key]) > 1 {
			return true
		}
	}
	return false
}
