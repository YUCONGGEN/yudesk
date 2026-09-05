package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/yudesk/yudesk/internal/stream"
)

var optionsMu sync.Mutex

func loadStreamOptions(directory string, fps, quality int) stream.Options {
	optionsMu.Lock()
	defer optionsMu.Unlock()
	o := stream.Options{FPS: fps, Quality: quality, Mode: "fixed", MaxWidth: 0, MaxMbps: 0, SaveIdle: true, FrameAck: true}
	if data, err := os.ReadFile(filepath.Join(directory, "stream-options.json")); err == nil {
		var saved stream.Options
		if json.Unmarshal(data, &saved) == nil {
			// Upgrade the exact former factory preset. Deliberately customized
			// settings remain untouched.
			if saved.FPS == 30 && saved.Quality == 60 && saved.Mode == "adaptive" && saved.MaxWidth == 960 && saved.MaxMbps == 12 && saved.SaveIdle {
				o = stream.Options{FPS: 30, Quality: 82, Mode: "fixed", MaxWidth: 0, MaxMbps: 0, SaveIdle: true, FrameAck: true}
			} else {
				o = saved
			}
		}
	}
	o.FrameAck = true
	return o.Normalized()
}

func saveStreamOptions(directory string, o stream.Options) error {
	optionsMu.Lock()
	defer optionsMu.Unlock()
	data, err := json.MarshalIndent(o.Normalized(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "stream-options.json"), data, 0600)
}
