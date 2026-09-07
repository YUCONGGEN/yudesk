package desktop

import (
	"errors"
	"image"
)

var ErrUnsupported = errors.New("desktop operation is unavailable on this system or requires a desktop helper")

// ErrInputRejected identifies invalid or unsupported input, not an OS injection
// failure. Errors never include key names or committed text.
var ErrInputRejected = errors.New("desktop input is invalid or unsupported")

// ErrUnsupportedInput is an alias for callers distinguishing validation errors
// from OS injection failures with errors.Is.
var ErrUnsupportedInput = ErrInputRejected

type Screenshot struct {
	JPEG                      []byte
	Pixels                    *image.RGBA // owned pixels, populated by Raw captures
	Width                     int
	Height                    int
	SourceWidth, SourceHeight int
}

type CaptureOptions struct {
	Quality  int
	MaxWidth int
	Raw      bool        // compare pixels before encoding; avoids JPEG work on idle desktops
	Buffer   *image.RGBA // optional exclusively-owned reusable Raw destination
}

type InputEvent struct {
	Type   string `json:"type"` // move, down, up, wheel, key, key_down, key_up, text
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Button int    `json:"button,omitempty"`
	Key    string `json:"key,omitempty"`
	Code   string `json:"code,omitempty"` // KeyboardEvent.code; preferred for physical Windows keys
	Text   string `json:"text,omitempty"` // committed UTF-8 text, only for type=text
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
