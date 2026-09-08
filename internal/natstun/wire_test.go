package natstun

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"slices"
	"testing"

	"github.com/pion/stun/v4"
)

func pionRequest(t testing.TB, fingerprint bool) *stun.Message {
	t.Helper()
	m, err := stun.Build(stun.BindingRequest, stun.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint {
		if err := stun.Fingerprint.AddTo(m); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func checkResponse(t testing.TB, request, response []byte) *stun.Message {
	t.Helper()
	// 2.2 is the largest payload expansion: bare IPv6 Binding is 20 -> 44.
	if len(response) < 20 || len(response) > maxResponse || 5*len(response) > 11*len(request) || len(response)%4 != 0 {
		t.Fatalf("response bounds: request=%d response=%d", len(request), len(response))
	}
	if int(binary.BigEndian.Uint16(response[2:])) != len(response)-20 || !bytes.Equal(response[4:20], request[4:20]) {
		t.Fatal("response length/cookie/transaction mismatch")
	}
	m := new(stun.Message)
	if err := stun.Decode(response, m); err != nil {
		t.Fatalf("independent Pion decode: %v", err)
	}
	if m.Type != stun.BindingSuccess && m.Type != stun.BindingError {
		t.Fatalf("unexpected response type: %v", m.Type)
	}
	seen := make(map[stun.AttrType]bool)
	for _, attr := range m.Attributes {
		if seen[attr.Type] {
			t.Fatalf("duplicate response attribute: %v", attr.Type)
		}
		seen[attr.Type] = true
		switch attr.Type {
		case stun.AttrXORMappedAddress, stun.AttrErrorCode, stun.AttrUnknownAttributes, stun.AttrFingerprint:
		default:
			t.Fatalf("unexpected/echoed attribute: %v", attr.Type)
		}
	}
	if m.Type == stun.BindingSuccess {
		var mapped stun.XORMappedAddress
		if err := mapped.GetFrom(m); err != nil || seen[stun.AttrErrorCode] || seen[stun.AttrUnknownAttributes] {
			t.Fatalf("invalid Binding success: %v", err)
		}
	} else {
		var code stun.ErrorCodeAttribute
		if err := code.GetFrom(m); err != nil || (code.Code != 400 && code.Code != 420) || len(code.Reason) != 0 || seen[stun.AttrXORMappedAddress] {
			t.Fatalf("invalid bounded Binding error: %v %v", code, err)
		}
		if (code.Code == 420) != seen[stun.AttrUnknownAttributes] {
			t.Fatal("420 must include UNKNOWN-ATTRIBUTES")
		}
	}
	if seen[stun.AttrFingerprint] {
		if m.Attributes[len(m.Attributes)-1].Type != stun.AttrFingerprint {
			t.Fatal("fingerprint is not last")
		}
		if err := stun.Fingerprint.Check(m); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func appendAttribute(request []byte, kind uint16, value []byte) []byte {
	n := len(request)
	result := append(bytes.Clone(request), make([]byte, 4+(len(value)+3)&^3)...)
	binary.BigEndian.PutUint16(result[n:], kind)
	binary.BigEndian.PutUint16(result[n+2:], uint16(len(value)))
	copy(result[n+4:], value)
	binary.BigEndian.PutUint16(result[2:], uint16(len(result)-20))
	return result
}

func TestPionBindingDecode(t *testing.T) {
	for _, ip := range []string{"192.0.2.1", "::ffff:192.0.2.1", "2001:db8::1234"} {
		for _, fp := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fingerprint=%t", ip, fp), func(t *testing.T) {
				request := pionRequest(t, fp)
				addr := &net.UDPAddr{IP: net.ParseIP(ip), Port: 32853}
				response := bindingResponse(request.Raw, addr)
				m := checkResponse(t, request.Raw, response)
				var mapped stun.XORMappedAddress
				if err := mapped.GetFrom(m); err != nil || !mapped.IP.Equal(addr.IP) || mapped.Port != addr.Port {
					t.Fatalf("mapped=%v err=%v", mapped, err)
				}
				wantSize, wantFamily := 32, byte(1)
				if addr.IP.To4() == nil {
					wantSize, wantFamily = 44, 2
				}
				if fp {
					wantSize += 8
				}
				if len(response) != wantSize || response[24] != 0 || response[25] != wantFamily || m.Contains(stun.AttrFingerprint) != fp {
					t.Fatal("wrong address family, reserved byte or wire size")
				}
			})
		}
	}
}

func TestUnknownRequired420(t *testing.T) {
	for _, fp := range []bool{false, true} {
		for _, count := range []int{1, 2, 3, 137, 139} {
			if fp && count == 139 {
				continue // 139 zero-length attributes already fill the 576-byte request.
			}
			t.Run(fmt.Sprintf("count=%d/fingerprint=%t", count, fp), func(t *testing.T) {
				request := pionRequest(t, false)
				var want stun.UnknownAttributes
				for i := 0; i < count; i++ {
					kind := stun.AttrType(0x4000 + i)
					request.Add(kind, nil)
					want = append(want, kind)
				}
				if count < 137 {
					request.Add(want[0], nil)
				} // Deduplicate, preserve complete list.
				if fp {
					_ = stun.Fingerprint.AddTo(request)
				}
				m := checkResponse(t, request.Raw, bindingResponse(request.Raw, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 12345}))
				var got stun.UnknownAttributes
				if err := got.GetFrom(m); err != nil || !slices.Equal(got, want) || m.Contains(stun.AttrFingerprint) != fp {
					t.Fatalf("unknown list: %v %v", got, err)
				}
			})
		}
	}
}

func TestOptionalAttributesAndLengthBounds(t *testing.T) {
	addr := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 12345}
	for _, size := range []int{0, 1, 2, 3, 4, 552} {
		request := appendAttribute(requestPacket(), 0xc123, bytes.Repeat([]byte{0x55}, size))
		if rem := size % 4; rem != 0 { // Padding contents are ignored, not required to be zero.
			for i := len(request) - (4 - rem); i < len(request); i++ {
				request[i] = 0xa5
			}
		}
		m := checkResponse(t, request, bindingResponse(request, addr))
		if m.Type != stun.BindingSuccess {
			t.Fatal("optional attribute rejected")
		}
	}
	for _, size := range []int{553, 554, 555, 556} {
		if bindingResponse(appendAttribute(requestPacket(), 0xc123, make([]byte, size)), addr) != nil {
			t.Fatal("accepted oversized Binding")
		}
	}
	for _, raw := range [][]byte{
		append(requestPacket(), 0),
		appendAttribute(requestPacket(), 0xc123, []byte{1})[:25],
		appendAttribute(requestPacket(), 0x4000, nil)[:23],
	} {
		if bindingResponse(raw, addr) != nil {
			t.Fatal("accepted truncated or trailing data")
		}
	}
}

func TestMalformedFingerprintAndCredentials(t *testing.T) {
	addr := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 12345}
	valid := pionRequest(t, true).Raw
	for name, mutate := range map[string]func([]byte) []byte{
		"crc":         func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		"transaction": func(b []byte) []byte { b[8] ^= 1; return b },
		"length":      func(b []byte) []byte { b[23] = 3; return b },
		"not-last":    func(b []byte) []byte { return appendAttribute(b, 0x8022, nil) },
		"duplicate":   func(b []byte) []byte { return appendAttribute(b, 0x8028, make([]byte, 4)) },
	} {
		t.Run(name, func(t *testing.T) {
			if bindingResponse(mutate(bytes.Clone(valid)), addr) != nil {
				t.Fatal("accepted invalid fingerprint")
			}
		})
	}
	// A bad CRC after a required attribute must not elicit even an error response.
	request := pionRequest(t, false)
	request.Add(stun.AttrType(0x4321), nil)
	_ = stun.Fingerprint.AddTo(request)
	request.Raw[len(request.Raw)-1] ^= 1
	if bindingResponse(request.Raw, addr) != nil {
		t.Fatal("420 bypassed CRC validation")
	}
	for _, kind := range []uint16{0x0006, 0x0008, 0x001c, 0x001e, 0x8002} {
		raw := appendAttribute(requestPacket(), kind, []byte("not-authenticated"))
		m := checkResponse(t, raw, bindingResponse(raw, addr))
		var code stun.ErrorCodeAttribute
		if err := code.GetFrom(m); err != nil || code.Code != 400 {
			t.Fatalf("credentials accepted: %v", err)
		}
	}
}
