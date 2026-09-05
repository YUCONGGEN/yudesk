//go:build linux

package systemaudio

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
)

func available() (bool, string) {
	if _, err := exec.LookPath("parec"); err != nil {
		return false, "未安装 PulseAudio parec"
	}
	if _, err := exec.LookPath("pactl"); err != nil {
		return false, "未安装 PulseAudio pactl"
	}
	return true, "PulseAudio 系统声音"
}

func capture(ctx context.Context, sink Sink) error {
	defaultSink, err := exec.CommandContext(ctx, "pactl", "get-default-sink").Output()
	if err != nil {
		return err
	}
	device := strings.TrimSpace(string(defaultSink)) + ".monitor"
	cmd := exec.CommandContext(ctx, "parec", "--raw", "--format=s16le", "--rate=48000", "--channels=2", "--device="+device)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	format := Format{SampleRate: 48000, Channels: 2, Bits: 16}
	buffer := make([]byte, 3840) // 20 ms of 48 kHz stereo s16le.
	for {
		n, readErr := io.ReadFull(stdout, buffer)
		if n > 0 {
			if err := sink(format, buffer[:n]); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return err
			}
		}
		if readErr != nil {
			waitErr := cmd.Wait()
			if ctx.Err() != nil || errors.Is(readErr, context.Canceled) {
				return nil
			}
			if waitErr != nil {
				return waitErr
			}
			return readErr
		}
	}
}
