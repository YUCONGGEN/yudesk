// Package natstun provides a bounded, binding-only RFC 8489 endpoint. It
// discovers a client's UDP mapping; it cannot relay data or open router ports.
package natstun

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"net"
	"slices"
	"sync"
	"time"
)

const (
	cookie     uint32 = 0x2112a442
	maxRequest        = 576
	maxUnknown        = (maxRequest - 20) / 4
	// Header, minimal ERROR-CODE, all possible unknown types, and FINGERPRINT.
	maxResponse = 20 + 8 + 4 + (2*maxUnknown+3)&^3 + 8
)

// Serve owns conn and closes it on cancellation. No per-datagram goroutines,
// DNS resolution, destination attributes, TURN allocation or external writes.
// conn must be bound to a concrete local IP so replies use the request's
// destination address. Use ListenAndServe for wildcard listening addresses.
func Serve(ctx context.Context, conn *net.UDPConn) error {
	return serve(ctx, conn, &responseLimiter{limiter: limiter{hosts: make(map[string]window)}})
}

func serve(ctx context.Context, conn *net.UDPConn, limits *responseLimiter) error {
	if conn == nil {
		return errors.New("STUN requires a UDP socket")
	}
	defer conn.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if addr := conn.LocalAddr().(*net.UDPAddr); addr.IP.IsUnspecified() {
		return errors.New("STUN requires a concrete local IP; use natstun.ListenAndServe for wildcard addresses")
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	// A large datagram must be discarded, not terminate the Windows listener
	// with WSAEMSGSIZE. Application parsing still accepts at most 576 bytes.
	var packet [65535]byte
	for {
		n, addr, err := conn.ReadFromUDP(packet[:])
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if packetError(err) {
				continue
			}
			return err
		}
		if n > maxRequest {
			continue
		}
		response := bindingResponse(packet[:n], addr)
		if response != nil && limits.allow(sourceKey(addr.IP), time.Now()) {
			// Send only to the observed source, never a supplied alternate target.
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = conn.WriteToUDP(response, addr)
		}
	}
}

func bindingResponse(request []byte, addr *net.UDPAddr) []byte {
	if len(request) < 20 || len(request) > maxRequest || binary.BigEndian.Uint16(request) != 1 ||
		binary.BigEndian.Uint32(request[4:]) != cookie ||
		int(binary.BigEndian.Uint16(request[2:])) != len(request)-20 || len(request)%4 != 0 ||
		addr == nil || addr.Port <= 0 || addr.Port > 65535 {
		return nil
	}
	fingerprint := false
	var unknown [maxUnknown]uint16
	unknownCount := 0
	credentials := false
	for offset := 20; offset < len(request); {
		if offset+4 > len(request) {
			return nil
		}
		kind := binary.BigEndian.Uint16(request[offset:])
		size := int(binary.BigEndian.Uint16(request[offset+2:]))
		next := offset + 4 + (size+3)&^3
		if next > len(request) {
			return nil
		}
		switch kind {
		case 0x8028:
			if size != 4 || next != len(request) || binary.BigEndian.Uint32(request[offset+4:]) != crc32.ChecksumIEEE(request[:offset])^0x5354554e {
				return nil
			}
			fingerprint = true
		case 0x0006, 0x0008, 0x0014, 0x0015, 0x001c, 0x001d, 0x001e, 0x8002:
			// This public Binding endpoint does not implement credentials. Return
			// 400 rather than imply that an integrity attribute was authenticated.
			credentials = true
		default:
			if kind < 0x8000 && !slices.Contains(unknown[:unknownCount], kind) {
				unknown[unknownCount] = kind
				unknownCount++
			}
		}
		offset = next
	}
	ip := addr.IP.To4()
	family := byte(1)
	if ip == nil {
		ip, family = addr.IP.To16(), 2
	}
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return nil
	}
	if credentials {
		return bindingError(request, 400, nil, fingerprint)
	}
	if unknownCount > 0 {
		return bindingError(request, 420, unknown[:unknownCount], fingerprint)
	}
	size := 28 + len(ip)
	if fingerprint {
		size += 8
	}
	response := make([]byte, size)
	binary.BigEndian.PutUint16(response[20:], 0x0020)
	binary.BigEndian.PutUint16(response[22:], uint16(4+len(ip)))
	response[25] = family
	binary.BigEndian.PutUint16(response[26:], uint16(addr.Port)^uint16(cookie>>16))
	for i, b := range ip {
		response[28+i] = b ^ request[4+i]
	}
	finishResponse(response, request, 0x0101, fingerprint)
	return response
}

// Error responses contain no echoed values or diagnostic text. The bounded
// request limits the complete, deduplicated UNKNOWN-ATTRIBUTES list to 139.
func bindingError(request []byte, code uint16, unknown []uint16, fingerprint bool) []byte {
	size := 28
	if len(unknown) > 0 {
		size += 4 + (2*len(unknown)+3)&^3
	}
	if fingerprint {
		size += 8
	}
	response := make([]byte, size)
	binary.BigEndian.PutUint16(response[20:], 0x0009)
	binary.BigEndian.PutUint16(response[22:], 4)
	response[26], response[27] = byte(code/100), byte(code%100)
	if len(unknown) > 0 {
		binary.BigEndian.PutUint16(response[28:], 0x000a)
		binary.BigEndian.PutUint16(response[30:], uint16(2*len(unknown)))
		for i, kind := range unknown {
			binary.BigEndian.PutUint16(response[32+2*i:], kind)
		}
	}
	finishResponse(response, request, 0x0111, fingerprint)
	return response
}

func finishResponse(response, request []byte, kind uint16, fingerprint bool) {
	binary.BigEndian.PutUint16(response, kind)
	binary.BigEndian.PutUint16(response[2:], uint16(len(response)-20))
	copy(response[4:20], request[4:20])
	if fingerprint {
		offset := len(response) - 8
		binary.BigEndian.PutUint16(response[offset:], 0x8028)
		binary.BigEndian.PutUint16(response[offset+2:], 4)
		binary.BigEndian.PutUint32(response[offset+4:], crc32.ChecksumIEEE(response[:offset])^0x5354554e)
	}
}

// All concrete listeners belonging to one endpoint share the response budget.
type responseLimiter struct {
	mu sync.Mutex
	limiter
}

func (l *responseLimiter) allow(host string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limiter.allow(host, now)
}

type window struct {
	second int64
	count  int
}
type limiter struct {
	hosts map[string]window
	all   window
}

func sourceKey(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	var prefix [16]byte
	copy(prefix[:8], ip.To16())
	return net.IP(prefix[:]).String() + "/64"
}

// Callers serialize access; hard-capped state limits abuse of a public endpoint.
func (l *limiter) allow(host string, now time.Time) bool {
	second := now.Unix()
	if l.all.second != second {
		l.all = window{second: second}
		// Fixed one-second windows bound the table to <=1000 accepted sources,
		// and prevent a burst of addresses locking out new clients for a minute.
		clear(l.hosts)
	}
	if l.all.count >= 1000 {
		return false
	}
	w, exists := l.hosts[host]
	if !exists && len(l.hosts) >= 1000 {
		return false
	}
	if w.second != second {
		w = window{second: second}
	}
	if w.count >= 20 {
		return false
	}
	l.all.count++
	w.count++
	l.hosts[host] = w
	return true
}
