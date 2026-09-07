//go:build !windows

package viewerapp

import "os/exec"

func configureBrowserProcess(cmd *exec.Cmd) {}
