package natstun

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"testing"
	"time"
)

func requestPacket() []byte {
	b, _ := hex.DecodeString("000100002112a442b7e7a701bc34d686fa87dfae")
	return b
}

func TestBindingAddressAndFingerprint(t *testing.T) {
	for _, address := range []string{"192.0.2.1", "2001:db8::1234"} {
		for _, withFP := range []bool{false, true} {
			request := requestPacket()
			if withFP {
				request = append(request, 0x80, 0x28, 0, 4, 0, 0, 0, 0)
				binary.BigEndian.PutUint16(request[2:], 8)
				binary.BigEndian.PutUint32(request[24:], crc32.ChecksumIEEE(request[:20])^0x5354554e)
			}
			addr := &net.UDPAddr{IP: net.ParseIP(address), Port: 32853}
			response := bindingResponse(request, addr)
			if response == nil {
				t.Fatal("valid request rejected")
			}
			if !bytes.Equal(response[4:20], request[4:20]) || binary.BigEndian.Uint16(response) != 0x101 {
				t.Fatal("wrong transaction")
			}
			n := int(binary.BigEndian.Uint16(response[22:])) - 4
			ip := make(net.IP, n)
			for i := range ip {
				ip[i] = response[28+i] ^ request[4+i]
			}
			if !ip.Equal(addr.IP) || int(binary.BigEndian.Uint16(response[26:])^uint16(cookie>>16)) != addr.Port {
				t.Fatalf("bad mapped address %v", ip)
			}
			if withFP && binary.BigEndian.Uint32(response[len(response)-4:]) != crc32.ChecksumIEEE(response[:len(response)-8])^0x5354554e {
				t.Fatal("bad response fingerprint")
			}
		}
	}
}

func TestRejectMalformed(t *testing.T) {
	addr := &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 12345}
	for _, change := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:19] },
		func(b []byte) []byte { b[0] = 1; return b },
		func(b []byte) []byte { b[4] = 0; return b },
		func(b []byte) []byte { b[3] = 4; return b },
		func(b []byte) []byte { b[3] = 4; return append(b, 0x80, 0x22, 0, 8) },
		func(b []byte) []byte { b[3] = 8; return append(b, 0x80, 0x28, 0, 4, 0, 0, 0, 0) },
	} {
		if bindingResponse(change(requestPacket()), addr) != nil {
			t.Fatal("accepted malformed datagram")
		}
	}
}

func TestServeAndCancel(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, server) }()
	client, err := net.DialUDP("udp4", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	// On Windows this previously caused WSAEMSGSIZE and killed the listener.
	if _, err = client.Write(make([]byte, 8192)); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Write(requestPacket()); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 100)
	n, err := client.Read(b)
	if err != nil || n != 32 {
		t.Fatalf("binding response %d %v", n, err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server leaked")
	}
}

func TestLimiter(t *testing.T) {
	l := limiter{hosts: map[string]window{}}
	now := time.Unix(1000, 0)
	for i := 0; i < 20; i++ {
		if !l.allow("a", now) {
			t.Fatal("early limit")
		}
	}
	if l.allow("a", now) {
		t.Fatal("host limit")
	}
	if !l.allow("a", now.Add(time.Second)) {
		t.Fatal("window reset")
	}
	for i := 0; i < 5000; i++ {
		l.allow(string(rune(i)), now.Add(time.Duration(i)*time.Second))
	}
	if len(l.hosts) > 1000 {
		t.Fatal("unbounded hosts")
	}
}

func TestLimiterBurstAndFairness(t *testing.T) {
	l := limiter{hosts: map[string]window{}}
	now := time.Unix(1000, 0)
	for i := 0; i < 2000; i++ {
		l.allow("attacker", now)
	}
	if !l.allow("valid-client", now) {
		t.Fatal("rejected packets exhausted response budget")
	}
	for i := 0; i < 5000; i++ {
		l.allow(fmt.Sprint(i), now)
	}
	if len(l.hosts) > 1000 {
		t.Fatal("unbounded state")
	}
	if l.allow("new-client", now) {
		t.Fatal("global response limit exceeded")
	}
	if !l.allow("new-client", now.Add(time.Second)) {
		t.Fatal("new sources remain locked out")
	}
	if sourceKey(net.ParseIP("2001:db8:12:34::1")) != sourceKey(net.ParseIP("2001:db8:12:34:abcd::99")) {
		t.Fatal("IPv6 rotation bypasses prefix budget")
	}
}

func FuzzBindingResponse(f *testing.F) {
	f.Add(requestPacket())
	f.Add(appendAttribute(requestPacket(), 0x4321, nil))
	f.Add(appendAttribute(requestPacket(), 0x8022, []byte{1, 2, 3}))
	f.Add(pionRequest(f, true).Raw)
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, ip := range []string{"192.0.2.1", "2001:db8::1"} {
			response := bindingResponse(b, &net.UDPAddr{IP: net.ParseIP(ip), Port: 12345})
			if response != nil {
				checkResponse(t, b, response)
			}
		}
	})
}
