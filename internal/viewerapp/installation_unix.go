//go:build linux || darwin

package viewerapp

import (
	"os"
	"syscall"
)

func protectedInstallFile(path string, executable bool) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return false
	}
	if executable && info.Mode().Perm()&0111 == 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
