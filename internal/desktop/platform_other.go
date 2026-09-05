//go:build !windows && !darwin && !linux

package desktop

func capturePlatform(CaptureOptions) (Screenshot, error) { return Screenshot{}, ErrUnsupported }
func applyInputPlatform([]InputEvent) error              { return ErrUnsupported }
func clipboardGetPlatform() (string, error)              { return "", ErrUnsupported }
func clipboardSetPlatform(string) error                  { return ErrUnsupported }
