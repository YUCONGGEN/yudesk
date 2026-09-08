package natstun

import (
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPacketErrors(t *testing.T) {
	for _, code := range []error{windows.WSAECONNRESET, windows.WSAECONNREFUSED, windows.WSAEMSGSIZE} {
		err := &net.OpError{Op: "read", Net: "udp", Err: &os.SyscallError{Syscall: "wsarecvfrom", Err: code}}
		if !packetError(err) {
			t.Fatalf("did not recognize WSA packet error: %v", err)
		}
	}
	if packetError(net.ErrClosed) {
		t.Fatal("closure treated as recoverable packet error")
	}
}
