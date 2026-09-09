package viewerapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"time"
)

// The renderer describes only its local non-interactive header areas. Native
// Windows receives the actual mouse-down synchronously, without an HTTP hop.
type windowDragRect struct{ X, Y, Width, Height float64 }
type windowDragRegions struct {
	Width, Height, Scale float64
	Rects                []windowDragRect
	received             time.Time
}

func readWindowDragRegions(r io.Reader) (*windowDragRegions, error) {
	var value windowDragRegions
	data, err := io.ReadAll(io.LimitReader(r, 8193))
	if err != nil || len(data) > 8192 {
		return nil, errors.New("drag regions too large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid drag regions")
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if !finite(value.Width) || !finite(value.Height) || !finite(value.Scale) || value.Width < 1 || value.Height < 1 || value.Width > 32768 || value.Height > 32768 || value.Scale < 0.5 || value.Scale > 8 || len(value.Rects) > 16 {
		return nil, errors.New("invalid drag viewport")
	}
	for _, r := range value.Rects {
		if !finite(r.X) || !finite(r.Y) || !finite(r.Width) || !finite(r.Height) || r.X < 0 || r.Y < 0 || r.Width <= 0 || r.Height <= 0 || r.X+r.Width > value.Width+1 || r.Y+r.Height > math.Min(100, value.Height)+1 {
			return nil, errors.New("invalid drag rectangle")
		}
	}
	value.received = time.Now()
	return &value, nil
}
