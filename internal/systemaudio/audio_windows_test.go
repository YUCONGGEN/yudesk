//go:build windows

package systemaudio

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This test is opt-in because unattended Windows builders may not expose an
// audio endpoint. It validates the native WASAPI initialization on a real PC.
func TestWindowsLoopbackCapture(t *testing.T) {
	if os.Getenv("YUDESK_TEST_AUDIO") != "1" {
		t.Skip("set YUDESK_TEST_AUDIO=1 on a Windows desktop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2200*time.Millisecond)
	defer cancel()
	var chunks atomic.Int64
	var nonzero atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- Capture(ctx, func(format Format, data []byte) error {
			if format.SampleRate != 48000 || format.Channels != 2 || format.Bits != 16 {
				t.Errorf("unexpected audio format: %+v", format)
			}
			if len(data) > 0 {
				chunks.Add(1)
				for i := 0; i+1 < len(data); i += 2 {
					if v := int16(binary.LittleEndian.Uint16(data[i:])); v > 50 || v < -50 {
						nonzero.Add(1)
					}
				}
			}
			return nil
		})
	}()
	time.Sleep(250 * time.Millisecond)
	playTestTone(t, 600*time.Millisecond)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if chunks.Load() == 0 {
		t.Fatal("WASAPI opened but produced no loopback packets")
	}
	if nonzero.Load() < 480 {
		t.Fatal("loopback produced no audible test samples")
	}
	t.Logf("native 48 kHz stereo capture: %d packets, %d non-silent samples", chunks.Load(), nonzero.Load())
}

// Browser integration opts in to a generated tone, never a microphone source.
func TestWindowsPlayTestTone(t *testing.T) {
	if os.Getenv("YUDESK_TEST_AUDIO_TONE") != "1" {
		t.Skip("opt-in test tone")
	}
	playTestTone(t, 2500*time.Millisecond)
}

func playTestTone(t *testing.T, duration time.Duration) {
	t.Helper()
	const rate = 48000
	frames := int(duration.Seconds() * rate)
	wav := make([]byte, 44+frames*4)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 2)
	binary.LittleEndian.PutUint32(wav[24:], rate)
	binary.LittleEndian.PutUint32(wav[28:], rate*4)
	binary.LittleEndian.PutUint16(wav[32:], 4)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(frames*4))
	for i := range frames {
		fade := math.Min(1, math.Min(float64(i)/480, float64(frames-i)/480))
		sample := int16(3500 * fade * math.Sin(2*math.Pi*997*float64(i)/rate))
		binary.LittleEndian.PutUint16(wav[44+i*4:], uint16(sample))
		binary.LittleEndian.PutUint16(wav[46+i*4:], uint16(sample))
	}
	proc := windows.NewLazySystemDLL("winmm.dll").NewProc("PlaySoundW")
	result, _, err := proc.Call(uintptr(unsafe.Pointer(&wav[0])), 0, 0x0004|0x0002) // SND_MEMORY | SND_NODEFAULT; synchronous.
	runtime.KeepAlive(wav)
	if result == 0 {
		t.Fatalf("play generated test tone: %v", err)
	}
}
