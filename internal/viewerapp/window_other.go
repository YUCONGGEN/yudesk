//go:build !windows

package viewerapp

import (
	"errors"
	"os/exec"
)

func startBrowserProcess(executable string, args []string) (uint32, chan struct{}, func() error, error) {
	cmd := exec.Command(executable, args...)
	if err := cmd.Start(); err != nil {
		return 0, nil, nil, err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return uint32(cmd.Process.Pid), done, nil, nil
}

type nativeAppWindow struct{}

func (b *appWindow) showNativeWindow() error             { return nil }
func (b *appWindow) closeNativeWindow() error            { return nil }
func (b *appWindow) hideNativeWindow() (bool, error)     { return false, nil }
func (b *appWindow) minimizeNativeWindow() (bool, error) { return false, nil }
func (b *appWindow) Drag() error                         { return errors.New("请拖动 YuDesk 顶部空白区域") }
