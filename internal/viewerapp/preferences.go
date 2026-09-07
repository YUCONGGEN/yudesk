package viewerapp

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
	o := stream.Options{ProfileVersion: 2, FPS: fps, Quality: quality, Mode: "adaptive", MaxWidth: 1280, MaxMbps: 8, SaveIdle: true, FrameAck: true}
	if data, err := os.ReadFile(filepath.Join(directory, "stream-options.json")); err == nil {
		var saved stream.Options
		if json.Unmarshal(data, &saved) == nil {
			// Upgrade the exact former factory preset. Deliberately customized
			// settings remain untouched.
			oldFast := saved.Quality == 60 && saved.Mode == "adaptive" && saved.MaxWidth == 960 && saved.MaxMbps == 12
			oldSource := saved.Quality == 82 && saved.Mode == "fixed" && saved.MaxWidth == 0 && saved.MaxMbps == 0
			if saved.ProfileVersion < 2 && saved.FPS == 30 && saved.SaveIdle && (oldFast || oldSource) {
				o.Quality = 70
			} else {
				o = saved
			}
		}
	}
	o.FrameAck = true
	o.TileDelta = false // negotiated with each agent, not a persisted capability
	return o.Normalized()
}

func saveStreamOptions(directory string, o stream.Options) error {
	optionsMu.Lock()
	defer optionsMu.Unlock()
	o.ProfileVersion = 2
	data, err := json.MarshalIndent(o.Normalized(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "stream-options.json"), data, 0600)
}
