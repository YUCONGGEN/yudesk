package natmap

import (
	"bufio"
	"encoding/hex"
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

func parseLinuxRoutes(file interface{ Read([]byte) (int, error) }) (netip.Addr, error) {
	scanner := bufio.NewScanner(file)
	bestMetric := uint64(^uint32(0))
	var best netip.Addr
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		flags, flagErr := strconv.ParseUint(fields[3], 16, 32)
		metric, metricErr := strconv.ParseUint(fields[6], 10, 32)
		gateway, gatewayErr := decodeLinuxIPv4(fields[2])
		if flagErr != nil || metricErr != nil || gatewayErr != nil || flags&0x3 != 0x3 || gateway.IsUnspecified() {
			continue
		}
		if !best.IsValid() || metric < bestMetric {
			best, bestMetric = gateway, metric
		}
	}
	if err := scanner.Err(); err != nil {
		return netip.Addr{}, errors.Join(ErrNoGateway, err)
	}
	if !best.IsValid() {
		return netip.Addr{}, ErrNoGateway
	}
	return best, nil
}

func decodeLinuxIPv4(value string) (netip.Addr, error) {
	bytes, err := hex.DecodeString(value)
	if err != nil || len(bytes) != 4 {
		return netip.Addr{}, ErrNoGateway
	}
	return netip.AddrFrom4([4]byte{bytes[3], bytes[2], bytes[1], bytes[0]}), nil
}
