//go:build windows

package desktop

import (
	"context"
	"testing"
)

func TestWindowsDesktopCommandsHideWindows(t *testing.T) {
	cmd := newDesktopCommand(context.Background(), "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", "Get-Clipboard -Raw"}, nil)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("Windows desktop helper can open a visible window")
	}
}
