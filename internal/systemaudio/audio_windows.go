//go:build windows

package systemaudio

import (
	"context"
	"errors"
	"runtime"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
)

func available() (bool, string) { return true, "Windows WASAPI 系统声音" }

func capture(ctx context.Context, sink Sink) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		return err
	}
	defer ole.CoUninitialize()

	var enumerator *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &enumerator); err != nil {
		return err
	}
	defer enumerator.Release()
	var device *wca.IMMDevice
	if err := enumerator.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &device); err != nil {
		return err
	}
	defer device.Release()
	var client *wca.IAudioClient
	if err := device.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &client); err != nil {
		return err
	}
	defer client.Release()

	const sampleRate = 48000
	const channels = 2
	const bits = 16
	format := &wca.WAVEFORMATEX{
		WFormatTag:      wca.WAVE_FORMAT_PCM,
		NChannels:       channels,
		NSamplesPerSec:  sampleRate,
		NBlockAlign:     channels * (bits / 8),
		WBitsPerSample:  bits,
		NAvgBytesPerSec: sampleRate * channels * (bits / 8),
	}
	flags := uint32(wca.AUDCLNT_STREAMFLAGS_LOOPBACK | wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM | wca.AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY)
	if err := client.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, flags, wca.REFERENCE_TIME(200*10000), 0, format, nil); err != nil {
		return err
	}
	var captureClient *wca.IAudioCaptureClient
	if err := client.GetService(wca.IID_IAudioCaptureClient, &captureClient); err != nil {
		return err
	}
	defer captureClient.Release()
	if err := client.Start(); err != nil {
		return err
	}
	defer client.Stop()

	streamFormat := Format{SampleRate: sampleRate, Channels: channels, Bits: bits}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		for {
			var frames uint32
			if err := captureClient.GetNextPacketSize(&frames); err != nil {
				return err
			}
			if frames == 0 {
				break
			}
			var data *byte
			var flags uint32
			var devicePosition, qpcPosition uint64
			if err := captureClient.GetBuffer(&data, &frames, &flags, &devicePosition, &qpcPosition); err != nil {
				return err
			}
			size := int(frames) * int(format.NBlockAlign)
			chunk := make([]byte, size)
			if flags&wca.AUDCLNT_BUFFERFLAGS_SILENT == 0 && data != nil && size > 0 {
				copy(chunk, unsafe.Slice(data, size))
			}
			releaseErr := captureClient.ReleaseBuffer(frames)
			if releaseErr != nil {
				return releaseErr
			}
			if len(chunk) > 0 {
				if err := sink(streamFormat, chunk); err != nil {
					if errors.Is(err, context.Canceled) {
						return nil
					}
					return err
				}
			}
		}
	}
}
