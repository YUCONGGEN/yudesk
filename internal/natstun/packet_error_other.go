//go:build !windows

package natstun

import (
	"errors"
	"syscall"
)

func packetError(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EMSGSIZE)
}
