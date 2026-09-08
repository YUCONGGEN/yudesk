package viewerapp

import (
	"context"
	"errors"
	"time"
)

var errWindowTargetMissing = errors.New("app window has not opened")

// A monitor socket is not the window. In particular an IPC read error is NOT
// user intent to exit. One cancellable worker owns all retries; Hide/Show/Exit
// retire it before replacing a profile or a renderer.
func (b *appWindow) monitor(id string) error {
	b.stopMonitor()
	c, err := b.connection()
	if err != nil {
		return err
	}
	if err = windowCommand(c, "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		c.Close()
		return err
	}
	_ = c.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	b.cancelMonitor = cancel
	b.managed.Store(true)
	// Copy immutable lookup data: Show can replace b.profile during retirement.
	lookup := &appWindow{profile: b.profile, url: b.url}
	processDone, onClose, journal := b.processDone, b.onClose, b.journal
	closed := func(reason string) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		journal.record(reason, nil)
		if onClose != nil {
			onClose()
		}
	}
	go func() {
		defer cancel()
		defer func() {
			if c != nil {
				c.Close()
			}
		}()
		for {
			// Cancellation also closes a replacement socket, not just the first.
			current := c
			stop := context.AfterFunc(ctx, func() { current.Close() })
			var readErr error
			for {
				var event struct {
					Method string
					Params struct{ TargetID string }
				}
				readErr = c.ReadJSON(&event)
				if ctx.Err() != nil {
					stop()
					return
				}
				if readErr != nil {
					break
				}
				if event.Method == "Target.targetDestroyed" && event.Params.TargetID == id {
					stop()
					closed("window_target_closed")
					return
				}
			}
			stop()
			c.Close()
			c = nil
			journal.record("window_monitor_lost", readErr)
			lostAt := time.Now()
			backoff := 100 * time.Millisecond
			missing := 0
			for c == nil {
				timer := time.NewTimer(backoff)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				next, err := lookup.connection()
				if err == nil {
					stopAttempt := context.AfterFunc(ctx, func() { next.Close() })
					err = windowCommand(next, "Target.setDiscoverTargets", map[string]any{"discover": true}, nil)
					var found string
					if err == nil {
						found, err = lookup.target(next)
					}
					stopAttempt()
					if ctx.Err() != nil {
						next.Close()
						return
					}
					if err == nil {
						id, c = found, next
						_ = c.SetReadDeadline(time.Time{})
						journal.record("window_monitor_restored", nil)
						break
					}
					next.Close()
				}
				if errors.Is(err, errWindowTargetMissing) {
					missing++
					// Successful independent target inventories confirm a gone window;
					// failed IPC or incomplete JSON never count as this evidence.
					if missing >= 2 {
						closed("window_target_closed")
						return
					}
				} else {
					missing = 0
				}
				// Some browsers close IPC before emitting targetDestroyed on X.
				// Require actual termination of our owned process AND failed recovery.
				if time.Since(lostAt) >= 2*time.Second && processDone != nil {
					select {
					case <-processDone:
						closed("window_process_exited")
						return
					default:
					}
				}
				if backoff < 2*time.Second {
					backoff *= 2
					if backoff > 2*time.Second {
						backoff = 2 * time.Second
					}
				}
			}
		}
	}()
	return nil
}

func (b *appWindow) stopMonitor() {
	if b.cancelMonitor != nil {
		b.cancelMonitor()
		b.cancelMonitor = nil
	}
}
