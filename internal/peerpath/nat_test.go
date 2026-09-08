package peerpath

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v4/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/yudesk/yudesk/internal/protocol"
)

// Topology follows Pion ICE's connectivity_vnet_test.go: two isolated private
// LANs, each behind its own NAT, and a STUN-only server on a virtual WAN. No
// host interface, port forwarding, TURN allocation or real server is involved.
func natOptions(t *testing.T, natType vnet.NATType) [2]Options {
	t.Helper()
	logger := logging.NewDefaultLoggerFactory()
	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "0.0.0.0/0", LoggerFactory: logger})
	if err != nil {
		t.Fatal(err)
	}
	serverNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"1.2.3.4"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = wan.AddNet(serverNet); err != nil {
		t.Fatal(err)
	}
	var options [2]Options
	for i, addresses := range [][3]string{{"192.168.1.0/24", "192.168.1.10", "27.1.1.1"}, {"10.2.0.0/24", "10.2.0.10", "28.1.1.1"}} {
		lan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: addresses[0], StaticIPs: []string{addresses[2]}, NATType: &natType, LoggerFactory: logger})
		if err != nil {
			t.Fatal(err)
		}
		network, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{addresses[1]}})
		if err != nil {
			t.Fatal(err)
		}
		if err = lan.AddNet(network); err != nil {
			t.Fatal(err)
		}
		if err = wan.AddRouter(lan); err != nil {
			t.Fatal(err)
		}
		options[i] = Options{STUNURLs: []string{"stun:1.2.3.4:3478"}, Timeout: 2 * time.Second, configure: func(s *webrtc.SettingEngine) {
			s.SetNet(network)
			s.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
		}}
	}
	if err = wan.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wan.Stop() })
	server, err := serverNet.ListenPacket("udp4", "1.2.3.4:3478")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1500)
		for {
			n, address, err := server.ReadFrom(buf)
			if err != nil {
				return
			}
			request := &stun.Message{Raw: buf[:n]}
			if request.Decode() != nil || request.Type != stun.BindingRequest {
				continue
			}
			remote := address.(*net.UDPAddr)
			response, err := stun.Build(stun.NewTransactionIDSetter(request.TransactionID), stun.BindingSuccess, &stun.XORMappedAddress{IP: remote.IP, Port: remote.Port})
			if err == nil {
				_, _ = server.WriteTo(response.Raw, address)
			}
		}
	}()
	t.Cleanup(func() { _ = server.Close(); await(t, done) })
	return options
}

func TestDualNATHolePunchAndSymmetricFallback(t *testing.T) {
	for _, test := range []struct {
		name string
		nat  vnet.NATType
		mode string
	}{
		{"full-cone", vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointIndependent}, "p2p"},
		{"port-restricted", vnet.NATType{MappingBehavior: vnet.EndpointIndependent, FilteringBehavior: vnet.EndpointAddrPortDependent}, "p2p"},
		{"symmetric", vnet.NATType{MappingBehavior: vnet.EndpointAddrPortDependent, FilteringBehavior: vnet.EndpointAddrPortDependent}, "relay"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := natOptions(t, test.nat)
			a, b := encryptedPair(t)
			bases := [2]*protocol.Conn{protocol.NewConn(a), protocol.NewConn(b)}
			results := runPair(t, context.Background(), bases, options, [2]func() (protocol.Message, error){})
			for _, r := range results {
				if r.err != nil || r.info.Mode != test.mode {
					t.Fatalf("NAT negotiation: %+v", r)
				}
			}
			if results[0].info != results[1].info {
				t.Fatal("NAT decisions differ")
			}
			if test.mode == "p2p" {
				for _, r := range results {
					s := r.c.Conn.(*sessionConn)
					pair, err := s.a.pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
					if err != nil || pair == nil {
						t.Fatalf("candidate pair: %v", err)
					}
					if !strings.Contains(s.a.pc.LocalDescription().SDP, "typ srflx") {
						t.Fatal("STUN never discovered NAT mapping")
					}
					if pair.Remote.Address != "27.1.1.1" && pair.Remote.Address != "28.1.1.1" {
						t.Fatalf("did not cross NAT: %+v", pair)
					}
					if pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
						t.Fatal("TURN used")
					}
				}
			}
			roundTrip(t, results[0].c, results[1].c, 150_007)
			roundTrip(t, results[1].c, results[0].c, 80_003)
		})
	}
}
