//go:build windows

package systemaudio

import (
	"context"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

// This test is opt-in because unattended Windows builders may not expose an
// audio endpoint. It validates the native WASAPI initialization on a real PC.
func TestWindowsLoopbackCapture(t *testing.T) {
	if os.Getenv("YUDESK_TEST_AUDIO") != "1" {
		t.Skip("set YUDESK_TEST_AUDIO=1 on a Windows desktop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	var chunks atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- Capture(ctx, func(format Format, data []byte) error {
			if format.SampleRate != 48000 || format.Channels != 2 || format.Bits != 16 {
				t.Errorf("unexpected audio format: %+v", format)
			}
			if len(data) > 0 {
				chunks.Add(1)
			}
			return nil
		})
	}()
	time.Sleep(250 * time.Millisecond)
	_ = exec.Command("powershell", "-NoProfile", "-Command", `[System.Media.SystemSounds]::Asterisk.Play(); Start-Sleep -Milliseconds 300`).Run()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if chunks.Load() == 0 {
		t.Fatal("WASAPI opened but produced no loopback packets")
	}
}
