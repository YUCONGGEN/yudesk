//go:build !linux && !android && !darwin && !windows

package natmap

import (
	"context"
	"net/netip"
)

func defaultGateway(context.Context) (netip.Addr, error) {
	return netip.Addr{}, ErrNoGateway
}
