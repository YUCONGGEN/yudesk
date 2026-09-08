package anet

import (
	"net"
	"strconv"
)

// addressWithNumericZone preserves ordinary addresses and gives scoped IPv6
// addresses an interface index that net and x/net can resolve without a cached
// interface name. In particular, Android 11 may deny their RTM_GETLINK refresh.
// Pion accepts IPAddr and retains its explicit Zone when creating UDP listeners.
func addressWithNumericZone(addr *net.IPNet, index int) net.Addr {
	if addr.IP.To4() == nil && (addr.IP.IsLinkLocalUnicast() || addr.IP.IsLinkLocalMulticast()) {
		return &net.IPAddr{IP: addr.IP, Zone: strconv.Itoa(index)}
	}
	return addr
}
