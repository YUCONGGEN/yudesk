//go:build !linux && !darwin

package viewerapp

func protectedInstallFile(string, bool) bool { return false }
