//go:build !windows

package viewerapp

import (
	"errors"
	"os/exec"
)

func configureBrowserProcess(cmd *exec.Cmd)         {}
func newBrowserGuard(cmd *exec.Cmd) (func(), error) { return nil, nil }

func (b *appWindow) removeNativeCaption() {}
func (b *appWindow) Drag() error          { return errors.New("请拖动系统窗口标题栏") }
