package desktop

import "errors"

var ErrUnsupported = errors.New("desktop operation is unavailable on this system or requires a desktop helper")

type Screenshot struct {
	JPEG                      []byte
	Width                     int
	Height                    int
	SourceWidth, SourceHeight int
}

type CaptureOptions struct {
	Quality  int
	MaxWidth int
}

type InputEvent struct {
	Type   string `json:"type"` // move, down, up, key
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Button int    `json:"button,omitempty"`
	Key    string `json:"key,omitempty"`
	DeltaX int    `json:"deltaX,omitempty"`
	DeltaY int    `json:"deltaY,omitempty"`
}

func Capture() (Screenshot, error) { return CaptureWithOptions(CaptureOptions{Quality: 72}) }
func CaptureWithOptions(options CaptureOptions) (Screenshot, error) {
	if options.Quality < 20 {
		options.Quality = 20
	}
	if options.Quality > 95 {
		options.Quality = 95
	}
	return capturePlatform(options)
}
func ApplyInput(events []InputEvent) error { return applyInputPlatform(events) }
func ClipboardGet() (string, error)        { return clipboardGetPlatform() }
func ClipboardSet(value string) error      { return clipboardSetPlatform(value) }
