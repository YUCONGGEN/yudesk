//go:build windows

package viewerapp

import (
	"os/exec"
	"syscall"
)

func configureBrowserProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}
