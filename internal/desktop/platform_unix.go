//go:build darwin || linux

package desktop

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

func runOutput(names ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), desktopCommandTimeout)
	defer cancel()
	for _, name := range names {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var args []string
		switch name {
		case "screencapture":
			if out, err := captureToTemporaryFile(ctx, name, "-x", "-t", "png"); err == nil {
				return out, nil
			}
			continue
		case "import":
			args = []string{"-window", "root", "png:-"}
		case "gnome-screenshot":
			if out, err := captureToTemporaryFile(ctx, name, "-f"); err == nil {
				return out, nil
			}
			continue
		case "maim":
			args = []string{"-u", "-"}
		case "grim":
			args = []string{"-"}
		default:
			continue
		}
		out, err := runDesktopCommand(ctx, name, args, nil)
		if err == nil && len(out) > 0 {
			return out, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, ErrUnsupported
}

func captureToTemporaryFile(ctx context.Context, command string, beforePath ...string) ([]byte, error) {
	directory, err := os.MkdirTemp("", "yudesk-capture-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "screen.png")
	args := append(append([]string{}, beforePath...), path)
	if _, err := runDesktopCommand(ctx, command, args, nil); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func capturePlatform(options CaptureOptions) (Screenshot, error) {
	names := []string{"grim", "import", "maim", "gnome-screenshot"}
	if runtime.GOOS == "darwin" {
		names = []string{"screencapture"}
	}
	pngBytes, err := runOutput(names...)
	if err != nil {
		return Screenshot{}, fmt.Errorf("capture screen: %w", err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return Screenshot{}, fmt.Errorf("decode screen image: %w", err)
	}
	return encodeScreen(img, options)
}

func applyInputPlatform(events []InputEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), desktopCommandTimeout)
	defer cancel()
	for _, event := range events {
		name, args, err := unixInputCommand(event, runtime.GOOS)
		if err != nil {
			return err
		}
		if len(args) == 0 {
			continue
		}
		if _, err := runDesktopCommand(ctx, name, args, nil); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// Build command arguments separately so tests never execute input helpers.
func unixInputCommand(event InputEvent, platform string) (string, []string, error) {
	if platform != "linux" && platform != "darwin" {
		return "", nil, ErrUnsupported
	}
	switch event.Type {
	case "key", "key_down", "key_up":
		if benignIMEKey(event.Key) {
			return "", nil, nil
		}
		if event.Key == "" || validateInputText(event.Key) != nil {
			return "", nil, fmt.Errorf("%w: key", ErrInputRejected)
		}
	case "text":
		if err := validateInputText(event.Text); err != nil {
			return "", nil, err
		}
		if event.Text == "" {
			return "", nil, nil
		}
	}
	var args []string
	if platform == "linux" {
		switch event.Type {
		case "move":
			args = []string{"mousemove", strconv.Itoa(event.X), strconv.Itoa(event.Y)}
		case "down":
			args = []string{"mousedown", strconv.Itoa(event.Button)}
		case "up":
			args = []string{"mouseup", strconv.Itoa(event.Button)}
		case "key":
			args = []string{"key", "--", linuxKeyName(event.Key)}
		case "key_down":
			args = []string{"keydown", "--", linuxKeyName(event.Key)}
		case "key_up":
			args = []string{"keyup", "--", linuxKeyName(event.Key)}
		case "text":
			args = []string{"type", "--clearmodifiers", "--delay", "0", "--", event.Text}
		case "wheel":
			button := "5"
			if event.DeltaY < 0 {
				button = "4"
			}
			args = []string{"click", button}
		default:
			return "", nil, fmt.Errorf("%w: event type", ErrInputRejected)
		}
		return "xdotool", args, nil
	}
	switch event.Type {
	case "move":
		args = []string{"m:" + strconv.Itoa(event.X) + "," + strconv.Itoa(event.Y)}
	case "down":
		if event.Button == 1 {
			args = []string{"dd:" + strconv.Itoa(event.X) + "," + strconv.Itoa(event.Y)}
		}
	case "up":
		if event.Button == 1 {
			args = []string{"du:" + strconv.Itoa(event.X) + "," + strconv.Itoa(event.Y)}
		} else if event.Button == 3 {
			args = []string{"rc:" + strconv.Itoa(event.X) + "," + strconv.Itoa(event.Y)}
		}
	case "key":
		args = []string{"kp:" + macKeyName(event.Key)}
	case "key_down":
		args = []string{"kd:" + macKeyName(event.Key)}
	case "key_up":
		args = []string{"ku:" + macKeyName(event.Key)}
	case "text":
		args = []string{"t:" + event.Text}
	case "wheel":
		args = []string{"w:" + strconv.Itoa(event.DeltaY)}
	default:
		return "", nil, fmt.Errorf("%w: event type", ErrInputRejected)
	}
	return "cliclick", args, nil
}

func linuxKeyName(key string) string {
	if mapped := map[string]string{" ": "space", "Space": "space", "Meta": "Super_L", "Control": "Control_L", "Shift": "Shift_L", "Alt": "Alt_L", "ArrowLeft": "Left", "ArrowRight": "Right", "ArrowUp": "Up", "ArrowDown": "Down", "Escape": "Escape", "Enter": "Return"}[key]; mapped != "" {
		return mapped
	}
	return key
}

func macKeyName(key string) string {
	if mapped := map[string]string{" ": "space", "Space": "space", "Meta": "cmd", "Control": "ctrl", "Shift": "shift", "Alt": "alt", "ArrowLeft": "arrow-left", "ArrowRight": "arrow-right", "ArrowUp": "arrow-up", "ArrowDown": "arrow-down", "Escape": "esc", "Enter": "return", "PageUp": "page-up", "PageDown": "page-down"}[key]; mapped != "" {
		return mapped
	}
	return key
}

func clipboardGetPlatform() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), desktopCommandTimeout)
	defer cancel()
	if runtime.GOOS == "darwin" {
		b, err := runDesktopCommand(ctx, "pbpaste", nil, nil)
		return string(b), err
	}
	for _, cmd := range [][]string{{"xclip", "-selection", "clipboard", "-o"}, {"xsel", "--clipboard", "--output"}} {
		b, err := runDesktopCommand(ctx, cmd[0], cmd[1:], nil)
		if err == nil {
			return string(b), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", ErrUnsupported
}

func clipboardSetPlatform(value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), desktopCommandTimeout)
	defer cancel()
	if runtime.GOOS == "darwin" {
		_, err := runDesktopCommand(ctx, "pbcopy", nil, bytes.NewBufferString(value))
		return err
	}
	for _, cmd := range [][]string{{"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}} {
		if _, err := runDesktopCommand(ctx, cmd[0], cmd[1:], bytes.NewBufferString(value)); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return ErrUnsupported
}
