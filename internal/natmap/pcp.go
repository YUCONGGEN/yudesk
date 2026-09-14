package natmap

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

const (
	pcpVersion     = 2
	pcpMapOpcode   = 1
	pcpUDPProtocol = 17
	pcpPort        = 5351
	pcpPacketSize  = 60
)

type pcpMapReply struct {
	lifetime     uint32
	externalIP   netip.Addr
	externalPort uint16
}

type pcpResultError struct {
	Code uint8
}

func (err *pcpResultError) Error() string {
	names := [...]string{"success", "unsupported version", "not authorized", "malformed request", "unsupported opcode", "unsupported option", "malformed option", "network failure", "no resources", "unsupported protocol", "user quota exceeded", "cannot provide external address", "address mismatch", "excessive remote peers"}
	if int(err.Code) < len(names) {
		return fmt.Sprintf("PCP result %d (%s)", err.Code, names[err.Code])
	}
	return fmt.Sprintf("PCP result %d", err.Code)
}

func mapPCP(ctx context.Context, socket *net.UDPConn, gateway netip.Addr, config mapConfig) (Mapping, error) {
	if !gateway.Is4() {
		return Mapping{}, errors.New("natmap: PCP gateway is not IPv4")
	}
	server := net.UDPAddrFromAddrPort(netip.AddrPortFrom(gateway, pcpPort))
	localIP, err := localIPv4(socket, gateway)
	if err != nil {
		return Mapping{}, err
	}
	nonce := [12]byte{}
	if _, err := rand.Read(nonce[:]); err != nil {
		return Mapping{}, fmt.Errorf("natmap: PCP nonce: %w", err)
	}
	externalPort := config.externalPort
	if externalPort == 0 {
		externalPort = config.localPort
	}
	lifetime := durationSeconds(config.lifetime)
	reply, err := exchangePCPMap(ctx, server, localIP, config.localPort, externalPort, lifetime, nonce)
	if err != nil {
		return Mapping{}, err
	}
	lease := &closeOnce{fn: func() error {
		closeCtx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		_, err := exchangePCPMap(closeCtx, server, localIP, config.localPort, reply.externalPort, 0, nonce)
		return err
	}}
	return Mapping{ExternalIP: reply.externalIP, ExternalPort: reply.externalPort, Method: MethodPCP, Lease: lease}, nil
}

func durationSeconds(value time.Duration) uint32 {
	seconds := value / time.Second
	if seconds < 1 {
		seconds = 1
	}
	if seconds > time.Duration(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(seconds)
}

func exchangePCPMap(ctx context.Context, server *net.UDPAddr, clientIP netip.Addr, internalPort, externalPort uint16, lifetime uint32, nonce [12]byte) (pcpMapReply, error) {
	request, err := marshalPCPMapRequest(clientIP, internalPort, externalPort, lifetime, nonce)
	if err != nil {
		return pcpMapReply{}, err
	}
	connection, err := net.DialUDP("udp4", nil, server)
	if err != nil {
		return pcpMapReply{}, fmt.Errorf("natmap: dial PCP gateway: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(600 * time.Millisecond)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	buffer := make([]byte, 1100)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return pcpMapReply{}, err
		}
		_ = connection.SetWriteDeadline(deadline)
		if _, err := connection.Write(request); err != nil {
			return pcpMapReply{}, fmt.Errorf("natmap: write PCP request: %w", err)
		}
		readDeadline := time.Now().Add(time.Duration(attempt+1) * 180 * time.Millisecond)
		if readDeadline.After(deadline) {
			readDeadline = deadline
		}
		_ = connection.SetReadDeadline(readDeadline)
		for {
			n, err := connection.Read(buffer)
			if err != nil {
				lastErr = err
				break
			}
			reply, err := parsePCPMapResponse(buffer[:n], internalPort, lifetime, nonce)
			if err == nil {
				return reply, nil
			}
			var resultErr *pcpResultError
			if errors.As(err, &resultErr) {
				return pcpMapReply{}, err
			}
			lastErr = err
			if time.Now().After(readDeadline) {
				break
			}
		}
	}
	if ctx.Err() != nil {
		return pcpMapReply{}, ctx.Err()
	}
	return pcpMapReply{}, fmt.Errorf("natmap: PCP mapping failed: %w", lastErr)
}

func marshalPCPMapRequest(clientIP netip.Addr, internalPort, externalPort uint16, lifetime uint32, nonce [12]byte) ([]byte, error) {
	clientIP = clientIP.Unmap()
	if !clientIP.IsValid() || (!clientIP.Is4() && !clientIP.Is6()) || internalPort == 0 {
		return nil, errors.New("natmap: invalid PCP mapping parameters")
	}
	packet := make([]byte, pcpPacketSize)
	packet[0] = pcpVersion
	packet[1] = pcpMapOpcode
	binary.BigEndian.PutUint32(packet[4:8], lifetime)
	address := clientIP.As16()
	copy(packet[8:24], address[:])
	copy(packet[24:36], nonce[:])
	packet[36] = pcpUDPProtocol
	binary.BigEndian.PutUint16(packet[40:42], internalPort)
	binary.BigEndian.PutUint16(packet[42:44], externalPort)
	return packet, nil
}

func parsePCPMapResponse(packet []byte, internalPort uint16, requestedLifetime uint32, nonce [12]byte) (pcpMapReply, error) {
	if len(packet) < pcpPacketSize {
		return pcpMapReply{}, errors.New("natmap: short PCP MAP response")
	}
	if packet[0] != pcpVersion || packet[1] != 0x80|pcpMapOpcode {
		return pcpMapReply{}, errors.New("natmap: unexpected PCP response opcode")
	}
	if packet[3] != 0 {
		return pcpMapReply{}, &pcpResultError{Code: packet[3]}
	}
	if string(packet[24:36]) != string(nonce[:]) || packet[36] != pcpUDPProtocol || binary.BigEndian.Uint16(packet[40:42]) != internalPort {
		return pcpMapReply{}, errors.New("natmap: PCP response does not match request")
	}
	address := [16]byte{}
	copy(address[:], packet[44:60])
	externalIP := netip.AddrFrom16(address).Unmap()
	externalPort := binary.BigEndian.Uint16(packet[42:44])
	lifetime := binary.BigEndian.Uint32(packet[4:8])
	if requestedLifetime != 0 && (!externalIP.IsValid() || externalIP.IsUnspecified() || externalPort == 0 || lifetime == 0) {
		return pcpMapReply{}, errors.New("natmap: PCP returned an empty mapping")
	}
	return pcpMapReply{lifetime: lifetime, externalIP: externalIP, externalPort: externalPort}, nil
}
