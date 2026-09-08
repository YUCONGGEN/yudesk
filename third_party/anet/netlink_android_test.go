package anet

import (
	"encoding/binary"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

func replyPacket(size int, kind uint16) []byte {
	b := make([]byte, size)
	binary.NativeEndian.PutUint32(b, uint32(size))
	binary.NativeEndian.PutUint16(b[4:], kind)
	binary.NativeEndian.PutUint32(b[8:], 1)
	binary.NativeEndian.PutUint32(b[12:], 9)
	return b
}

func TestNetlinkReceiveSocketTimeout(t *testing.T) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if err = syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = receiveNetlink(fd, make([]byte, 64), 30*time.Millisecond)
	if !errors.Is(err, syscall.ETIMEDOUT) {
		t.Fatalf("socket did not time out: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("socket receive exceeded bound")
	}
}

func TestNetlinkTotalBounds(t *testing.T) {
	t.Run("cumulative-size", func(t *testing.T) {
		packet := replyPacket(64<<10, syscall.NLMSG_NOOP)
		calls := 0
		_, err := readNetlinkRIB(9, time.Second, func(b []byte, remaining time.Duration) (int, error) {
			calls++
			return copy(b, packet), nil
		})
		if !errors.Is(err, syscall.EMSGSIZE) || calls != 17 {
			t.Fatalf("unbounded response: calls=%d err=%v", calls, err)
		}
	})
	t.Run("truncated-datagram", func(t *testing.T) {
		_, err := readNetlinkRIB(9, time.Second, func(b []byte, _ time.Duration) (int, error) { return len(b) + 1, nil })
		if !errors.Is(err, syscall.EMSGSIZE) {
			t.Fatal(err)
		}
	})
	t.Run("total-deadline", func(t *testing.T) {
		packet := replyPacket(16, syscall.NLMSG_NOOP)
		calls := 0
		_, err := readNetlinkRIB(9, 10*time.Millisecond, func(b []byte, _ time.Duration) (int, error) {
			calls++
			time.Sleep(6 * time.Millisecond)
			return copy(b, packet), nil
		})
		if !errors.Is(err, syscall.ETIMEDOUT) || calls > 2 {
			t.Fatalf("deadline extended by replies: calls=%d err=%v", calls, err)
		}
	})
	t.Run("completion", func(t *testing.T) {
		packet := replyPacket(16, syscall.NLMSG_DONE)
		got, err := readNetlinkRIB(9, time.Second, func(b []byte, _ time.Duration) (int, error) { return copy(b, packet), nil })
		if err != nil || len(got) != len(packet) {
			t.Fatalf("completion: %d %v", len(got), err)
		}
	})
}

func TestNetlinkMalformedBoundaries(t *testing.T) {
	for n := 1; n < syscall.NLMSG_HDRLEN; n++ {
		if _, err := ParseNetlinkMessage(make([]byte, n)); err == nil {
			t.Fatalf("accepted short header %d", n)
		}
	}
	for _, length := range []uint32{0, 15, 17, ^uint32(0)} {
		b := make([]byte, 17)
		binary.NativeEndian.PutUint32(b, length)
		if _, err := ParseNetlinkMessage(b); err == nil {
			t.Fatalf("accepted invalid/alignment length %d", length)
		}
	}
	short := NetlinkMessage{Header: syscall.NlMsghdr{Type: syscall.RTM_NEWADDR}, Data: make([]byte, syscall.SizeofIfAddrmsg-1)}
	if _, err := addrTable(nil, []NetlinkMessage{short}); err == nil {
		t.Fatal("accepted short IfAddrmsg")
	}
	if _, err := ParseNetlinkRouteAttr(&short); err == nil {
		t.Fatal("accepted short address header")
	}
	if _, err := ParseNetlinkRouteAttr(nil); err == nil {
		t.Fatal("accepted nil message")
	}
	for _, size := range []int{1, 2, 3, 5, 7} {
		m := NetlinkMessage{Header: syscall.NlMsghdr{Type: syscall.RTM_NEWADDR}, Data: make([]byte, syscall.SizeofIfAddrmsg+size)}
		if size >= 4 {
			binary.NativeEndian.PutUint16(m.Data[syscall.SizeofIfAddrmsg:], uint16(size))
		}
		if _, err := ParseNetlinkRouteAttr(&m); err == nil {
			t.Fatalf("accepted short/unaligned attribute %d", size)
		}
	}
}

func TestAddressAttributeBoundaries(t *testing.T) {
	for _, family := range []uint8{syscall.AF_INET, syscall.AF_INET6} {
		validLength := net.IPv4len
		if family == syscall.AF_INET6 {
			validLength = net.IPv6len
		}
		msg := &syscall.IfAddrmsg{Family: family, Prefixlen: 24, Index: 7}
		for n := 0; n < validLength; n++ {
			attrs := []NetlinkRouteAttr{{Attr: syscall.RtAttr{Type: syscall.IFA_ADDRESS}, Value: make([]byte, n)}}
			if addr := newAddr(msg, attrs); addr != nil {
				t.Fatalf("accepted short address: family=%d bytes=%d", family, n)
			}
		}
	}
	attrs := []NetlinkRouteAttr{
		{Attr: syscall.RtAttr{Type: syscall.IFA_LABEL}, Value: []byte("wlan0")},
		{Attr: syscall.RtAttr{Type: syscall.IFA_ADDRESS}, Value: []byte{192, 0, 2, 1}},
	}
	addr := newAddr(&syscall.IfAddrmsg{Family: syscall.AF_INET, Prefixlen: 24, Index: 7}, attrs)
	if addr == nil || addr.String() != "192.0.2.1/24" {
		t.Fatalf("non-address attribute used as IP: %v", addr)
	}
	attrs[1].Value = net.ParseIP("fe80::1").To16()
	addr = newAddr(&syscall.IfAddrmsg{Family: syscall.AF_INET6, Prefixlen: 64, Index: 7}, attrs)
	if addr == nil || addr.String() != "fe80::1%7" {
		t.Fatalf("scoped IPv6: %v", addr)
	}
}
