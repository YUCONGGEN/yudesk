//go:build windows

package desktop

import (
	"encoding/json"
	"errors"
	"image"

	"github.com/yudesk/yudesk/internal/winhost"
)

func PrivilegedOperation(method string, raw json.RawMessage) ([]byte, map[string]any, error) {
	if !winhost.IsWorker() {
		return nil, nil, errors.New("not a privileged desktop worker")
	}
	switch method {
	case "capture":
		var p struct {
			Quality, MaxWidth int
			Raw               bool
		}
		if json.Unmarshal(raw, &p) != nil {
			return nil, nil, errors.New("invalid capture options")
		}
		if p.MaxWidth <= 0 || p.MaxWidth > 3840 {
			p.MaxWidth = 3840
		}
		shot, err := CaptureWithOptions(CaptureOptions{Quality: p.Quality, MaxWidth: p.MaxWidth, Raw: p.Raw})
		if err != nil {
			return nil, nil, err
		}
		data := shot.JPEG
		if p.Raw {
			data = shot.Pixels.Pix
		}
		return data, map[string]any{"width": shot.Width, "height": shot.Height, "sourceWidth": shot.SourceWidth, "sourceHeight": shot.SourceHeight}, nil
	case "input":
		var p struct{ Events []InputEvent }
		if json.Unmarshal(raw, &p) != nil || len(p.Events) > 64 {
			return nil, nil, ErrInputRejected
		}
		return nil, nil, applyInputPlatform(p.Events)
	}
	return nil, nil, ErrUnsupported
}

func captureThroughService(o CaptureOptions) (Screenshot, error) {
	m, err := winhost.Call("capture", struct {
		Quality, MaxWidth int
		Raw               bool
	}{o.Quality, o.MaxWidth, o.Raw})
	if err != nil {
		return Screenshot{}, err
	}
	number := func(key string) int { n, _ := m.Meta[key].(float64); return int(n) }
	w, h := number("width"), number("height")
	if w < 1 || h < 1 || w > 16384 || h > 16384 || w*h > 8388608 {
		return Screenshot{}, errors.New("invalid service frame geometry")
	}
	s := Screenshot{Width: w, Height: h, SourceWidth: number("sourceWidth"), SourceHeight: number("sourceHeight")}
	if s.SourceWidth < w || s.SourceHeight < h || s.SourceWidth > 32768 || s.SourceHeight > 32768 {
		return Screenshot{}, errors.New("invalid service source geometry")
	}
	if o.Raw {
		if len(m.Data) != w*h*4 {
			return Screenshot{}, errors.New("incomplete service frame")
		}
		s.Pixels = &image.RGBA{Pix: m.Data, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	} else {
		s.JPEG = m.Data
	}
	return s, nil
}
