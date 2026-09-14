package natmap

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func pcpResponse(request []byte, result uint8, externalIP netip.Addr, externalPort uint16) []byte {
	response := make([]byte, pcpPacketSize)
	response[0] = pcpVersion
	response[1] = 0x80 | pcpMapOpcode
	response[3] = result
	binary.BigEndian.PutUint32(response[4:8], binary.BigEndian.Uint32(request[4:8]))
	copy(response[24:42], request[24:42])
	binary.BigEndian.PutUint16(response[42:44], externalPort)
	address := externalIP.As16()
	copy(response[44:60], address[:])
	return response
}

func TestPCPCodec(t *testing.T) {
	nonce := [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	request, err := marshalPCPMapRequest(netip.MustParseAddr("192.0.2.40"), 32100, 45600, 7200, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if len(request) != 60 || request[0] != 2 || request[1] != 1 || request[36] != 17 {
		t.Fatalf("invalid PCP request header: %x", request)
	}
	if got := binary.BigEndian.Uint16(request[40:42]); got != 32100 {
		t.Fatalf("internal port = %d", got)
	}
	response := pcpResponse(request, 0, netip.MustParseAddr("203.0.113.19"), 45600)
	reply, err := parsePCPMapResponse(response, 32100, 7200, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if reply.externalIP.String() != "203.0.113.19" || reply.externalPort != 45600 || reply.lifetime != 7200 {
		t.Fatalf("unexpected reply: %+v", reply)
	}
}

func TestPCPCodecRejectsErrorsAndMismatches(t *testing.T) {
	nonce := [12]byte{9, 8, 7}
	request, _ := marshalPCPMapRequest(netip.MustParseAddr("192.0.2.4"), 31000, 31000, 120, nonce)
	response := pcpResponse(request, 8, netip.MustParseAddr("203.0.113.9"), 31000)
	_, err := parsePCPMapResponse(response, 31000, 120, nonce)
	var resultErr *pcpResultError
	if !errors.As(err, &resultErr) || resultErr.Code != 8 {
		t.Fatalf("expected PCP result error, got %v", err)
	}
	response = pcpResponse(request, 0, netip.MustParseAddr("203.0.113.9"), 31000)
	response[24] ^= 0xff
	if _, err := parsePCPMapResponse(response, 31000, 120, nonce); err == nil {
		t.Fatal("mismatched nonce accepted")
	}
	if _, err := parsePCPMapResponse(response[:20], 31000, 120, nonce); err == nil {
		t.Fatal("short response accepted")
	}
}

func TestPCPTimeoutReturnsPromptly(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = exchangePCPMap(ctx, server.LocalAddr().(*net.UDPAddr), netip.MustParseAddr("127.0.0.1"), 30000, 30000, 120, [12]byte{1})
	if err == nil || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("PCP timeout was not bounded: err=%v elapsed=%s", err, time.Since(started))
	}
}

func TestPCPMappingAndIdempotentDelete(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var creates, deletes atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 1500)
		for {
			n, client, readErr := server.ReadFromUDP(buffer)
			if readErr != nil {
				return
			}
			request := append([]byte(nil), buffer[:n]...)
			if binary.BigEndian.Uint32(request[4:8]) == 0 {
				deletes.Add(1)
			} else {
				creates.Add(1)
			}
			_, _ = server.WriteToUDP(pcpResponse(request, 0, netip.MustParseAddr("203.0.113.44"), 45000), client)
		}
	}()
	application, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	config := mapConfig{localPort: uint16(application.LocalAddr().(*net.UDPAddr).Port), lifetime: time.Hour}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	localIP := netip.MustParseAddr("127.0.0.1")
	nonce := [12]byte{1}
	reply, err := exchangePCPMap(ctx, server.LocalAddr().(*net.UDPAddr), localIP, config.localPort, config.localPort, 3600, nonce)
	if err != nil {
		t.Fatal(err)
	}
	lease := &closeOnce{fn: func() error {
		_, err := exchangePCPMap(ctx, server.LocalAddr().(*net.UDPAddr), localIP, config.localPort, reply.externalPort, 0, nonce)
		return err
	}}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if creates.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("create/delete counts = %d/%d", creates.Load(), deletes.Load())
	}
	_ = server.Close()
	<-done
}
