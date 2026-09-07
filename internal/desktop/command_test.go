package desktop

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDesktopCommandBoundedAndLiteral(t *testing.T) {
	args := []string{"--", "$(do not execute); `literal` \"quotes\"\n中文😀"}
	var captured context.Context
	start := time.Now()
	output, err := runDesktopCommandUsing(context.Background(), "unused-desktop-helper", args, strings.NewReader("clipboard\ncontents"), func(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
		captured = ctx
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Before(start.Add(4900*time.Millisecond)) || deadline.After(start.Add(5100*time.Millisecond)) {
			t.Fatal("helper does not have a five-second deadline")
		}
		if !reflect.DeepEqual(cmd.Args, append([]string{"unused-desktop-helper"}, args...)) {
			t.Fatal("helper arguments were interpolated or split")
		}
		stdin, err := io.ReadAll(cmd.Stdin)
		if err != nil || string(stdin) != "clipboard\ncontents" {
			t.Fatal("stdin changed", err)
		}
		if cmd.Cancel == nil || cmd.WaitDelay <= 0 || cmd.WaitDelay > 100*time.Millisecond || cmd.Stderr != io.Discard {
			t.Fatal("helper lacks cancellation, bounded pipe wait, or private diagnostics")
		}
		return []byte("result"), nil
	})
	if err != nil || string(output) != "result" || captured.Err() != context.Canceled {
		t.Fatal("command result or context cleanup failed", err)
	}
}

func TestDesktopCommandPreservesEarlierDeadlineAndReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	output, err := runDesktopCommandUsing(ctx, "unused-desktop-helper", nil, nil, func(child context.Context, _ *exec.Cmd) ([]byte, error) {
		if got, ok := child.Deadline(); !ok || !got.Equal(want) {
			t.Fatal("helper extended the enclosing operation's deadline")
		}
		cancel()
		return []byte("partial"), nil
	})
	if !errors.Is(err, context.Canceled) || len(output) != 0 {
		t.Fatal("cancelled command returned a successful or partial result", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = runDesktopCommandUsing(ctx, "unused-desktop-helper", nil, nil, func(child context.Context, _ *exec.Cmd) ([]byte, error) {
		return nil, child.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("timeout was not preserved", err)
	}
	helperErr := errors.New("mock helper failure")
	_, err = runDesktopCommandUsing(context.Background(), "unused-desktop-helper", nil, nil, func(context.Context, *exec.Cmd) ([]byte, error) {
		return nil, helperErr
	})
	if !errors.Is(err, helperErr) {
		t.Fatal("helper failure was lost", err)
	}
}
