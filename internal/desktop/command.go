package desktop

import (
	"context"
	"io"
	"os/exec"
	"time"
)

const desktopCommandTimeout = 5 * time.Second

func newDesktopCommand(ctx context.Context, name string, args []string, input io.Reader) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = input
	cmd.Stderr = io.Discard // Helpers may echo key/text arguments in diagnostics.
	// A descendant retaining a pipe must not keep Output blocked after the
	// helper exits or the deadline kills it.
	cmd.WaitDelay = 100 * time.Millisecond
	configureDesktopCommand(cmd)
	return cmd
}

func runDesktopCommand(ctx context.Context, name string, args []string, input io.Reader) ([]byte, error) {
	return runDesktopCommandUsing(ctx, name, args, input, func(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
		return cmd.Output()
	})
}

// The runner seam lets deadline/command tests remain entirely side-effect free.
func runDesktopCommandUsing(ctx context.Context, name string, args []string, input io.Reader, run func(context.Context, *exec.Cmd) ([]byte, error)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, desktopCommandTimeout)
	defer cancel()
	output, err := run(ctx, newDesktopCommand(ctx, name, args, input))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output, err
}
