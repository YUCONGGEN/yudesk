//go:build !windows

package main

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// Kill the complete hook process group on timeout. Without this, a timed-out
// shell could leave upnpc running and open an untracked router port later.
func portMapHookCommandContext(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = 2 * time.Second
	return command
}
