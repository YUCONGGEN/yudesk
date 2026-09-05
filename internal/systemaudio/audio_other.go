//go:build !windows && !linux

package systemaudio

import (
	"context"
	"errors"
)

func available() (bool, string) { return false, "当前平台暂不支持系统声音采集" }

func capture(context.Context, Sink) error { return errors.New("system audio capture is unavailable") }
