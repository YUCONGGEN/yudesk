//go:build linux && !android

package natmap

import (
	"context"
	"errors"
	"net/netip"
	"os"
)

func defaultGateway(ctx context.Context) (netip.Addr, error) {
	if err := ctx.Err(); err != nil {
		return netip.Addr{}, err
	}
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return netip.Addr{}, errors.Join(ErrNoGateway, err)
	}
	defer file.Close()
	return parseLinuxRoutes(file)
}
