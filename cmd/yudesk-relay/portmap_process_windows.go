//go:build windows

package main

import (
	"context"
	"os/exec"
)

func portMapHookCommandContext(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
	return exec.CommandContext(ctx, executable, arguments...)
}
