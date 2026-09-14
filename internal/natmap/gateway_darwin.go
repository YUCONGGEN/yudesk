//go:build darwin

package natmap

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"strings"
)

func defaultGateway(ctx context.Context) (netip.Addr, error) {
	output, err := exec.CommandContext(ctx, "/sbin/route", "-n", "get", "default").Output()
	if err != nil {
		return netip.Addr{}, errors.Join(ErrNoGateway, err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimSuffix(fields[0], ":") == "gateway" {
			if gateway, err := netip.ParseAddr(strings.TrimSpace(fields[1])); err == nil && gateway.Is4() {
				return gateway, nil
			}
		}
	}
	return netip.Addr{}, ErrNoGateway
}
