package natstun

import (
	"errors"

	"golang.org/x/sys/windows"
)

func packetError(err error) bool {
	// syscall.ECONNRESET/EMSGSIZE are synthetic values on Windows, not WSA errors.
	return errors.Is(err, windows.WSAECONNRESET) || errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, windows.WSAEMSGSIZE)
}
