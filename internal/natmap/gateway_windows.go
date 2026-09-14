//go:build windows

package natmap

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func defaultGateway(ctx context.Context) (netip.Addr, error) {
	command := exec.CommandContext(ctx, "route.exe", "print", "-4")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.Output()
	if err != nil {
		return netip.Addr{}, errors.Join(ErrNoGateway, err)
	}
	bestMetric := uint64(^uint32(0))
	var best netip.Addr
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "0.0.0.0" || fields[1] != "0.0.0.0" {
			continue
		}
		gateway, gatewayErr := netip.ParseAddr(fields[2])
		metric, metricErr := strconv.ParseUint(fields[len(fields)-1], 10, 32)
		if gatewayErr != nil || metricErr != nil || !gateway.Is4() || gateway.IsUnspecified() {
			continue
		}
		if !best.IsValid() || metric < bestMetric {
			best, bestMetric = gateway, metric
		}
	}
	if !best.IsValid() {
		return netip.Addr{}, ErrNoGateway
	}
	return best, nil
}
