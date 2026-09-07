//go:build !windows

package desktop

import "os/exec"

func configureDesktopCommand(*exec.Cmd) {}
