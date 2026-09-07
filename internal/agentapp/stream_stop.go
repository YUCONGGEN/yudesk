package agentapp

import (
	"context"
	"net"
	"time"
)

func waitStreamStopped(ctx context.Context, done <-chan struct{}, conn net.Conn, timeout time.Duration) bool {
	// Finish an in-flight frame before reconfiguration so its encrypted record
	// is not cut in half. A stuck socket must not hold the file/settings worker
	// forever, though: close it after the grace period to unblock all writers.
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		_ = conn.Close()
		return false
	}
}
