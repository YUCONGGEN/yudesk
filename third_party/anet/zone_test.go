package anet

import (
	"net"
	"testing"
)

func TestAddressWithNumericZone(t *testing.T) {
	for _, test := range []struct {
		cidr   string
		scoped bool
	}{
		{"192.168.2.3/24", false},
		{"169.254.2.3/16", false},
		{"2001:db8::42/64", false},
		{"fd00::42/64", false},
		{"::1/128", false},
		{"fe80::42/64", true},
		{"ff02::1/128", true},
	} {
		t.Run(test.cidr, func(t *testing.T) {
			ip, subnet, err := net.ParseCIDR(test.cidr)
			if err != nil {
				t.Fatal(err)
			}
			subnet.IP = ip
			got := addressWithNumericZone(subnet, 17)
			if !test.scoped {
				if got != subnet {
					t.Fatal("changed unscoped address or prefix")
				}
				return
			}
			scoped, ok := got.(*net.IPAddr)
			if !ok || scoped.Zone != "17" || !scoped.IP.Equal(ip) {
				t.Fatalf("invalid scoped address: %#v", got)
			}
			// Verify the public socket-address path retains the numeric scope.
			udp, err := net.ResolveUDPAddr("udp6", net.JoinHostPort(scoped.String(), "1234"))
			if err != nil || udp.Zone != "17" || !udp.IP.Equal(ip) {
				t.Fatalf("numeric zone resolution: %+v %v", udp, err)
			}
		})
	}
}
