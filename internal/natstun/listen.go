package natstun

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// ListenAndServe serves Binding on address until cancellation or a listener
// fails. Wildcards expand to the local unicast addresses present at startup;
// restart after adding addresses. Concrete sockets preserve the reply source
// on Windows too, where x/net packet-info control is not implemented.
// All sockets share one limiter. No socket survives a partial bind failure.
func ListenAndServe(ctx context.Context, address string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return err
	}
	addresses, err := localAddresses(addr)
	if err != nil {
		return err
	}
	connections, err := listenAddresses(ctx, addresses)
	if err != nil {
		return err
	}
	defer closeConnections(connections)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	limits := &responseLimiter{limiter: limiter{hosts: make(map[string]window)}}
	done := make(chan error, len(connections))
	for _, conn := range connections {
		go func() { done <- serve(ctx, conn, limits) }()
	}
	result := <-done
	cancel()
	for i := 1; i < len(connections); i++ {
		<-done
	}
	return result
}

func localAddresses(addr *net.UDPAddr) ([]*net.UDPAddr, error) {
	if len(addr.IP) > 0 && !addr.IP.IsUnspecified() {
		return []*net.UDPAddr{addr}, nil
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var addresses []*net.UDPAddr
	seen := make(map[string]bool)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		values, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			ip, _, err := net.ParseCIDR(value.String())
			if err != nil || ip.IsUnspecified() || ip.IsMulticast() || (addr.IP.To4() != nil && ip.To4() == nil) {
				continue
			}
			local := &net.UDPAddr{IP: ip, Port: addr.Port}
			if ip.To4() == nil && ip.IsLinkLocalUnicast() {
				local.Zone = iface.Name
			}
			if key := local.String(); !seen[key] {
				seen[key] = true
				addresses = append(addresses, local)
			}
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("STUN found no local unicast addresses")
	}
	return addresses, nil
}

func listenAddresses(ctx context.Context, addresses []*net.UDPAddr) ([]*net.UDPConn, error) {
	var connections []*net.UDPConn
	port := 0
	for _, addr := range addresses {
		if err := ctx.Err(); err != nil {
			closeConnections(connections)
			return nil, err
		}
		local := *addr
		if local.Port == 0 {
			local.Port = port
		}
		network := "udp6"
		if local.IP.To4() != nil {
			network = "udp4"
		}
		conn, err := net.ListenUDP(network, &local)
		if err != nil {
			closeConnections(connections)
			return nil, fmt.Errorf("STUN listen %s: %w", &local, err)
		}
		port = conn.LocalAddr().(*net.UDPAddr).Port
		connections = append(connections, conn)
	}
	return connections, nil
}

func closeConnections(connections []*net.UDPConn) {
	for _, conn := range connections {
		_ = conn.Close()
	}
}
