//go:build darwin || linux

package desktop

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

func runOutput(names ...string) ([]byte, error) {
	for _, name := range names {
		var cmd *exec.Cmd
		switch name {
		case "screencapture":
			if out, err := captureToTemporaryFile(name, "-x", "-t", "png"); err == nil {
				return out, nil
			}
			continue
		case "import":
			cmd = exec.Command(name, "-window", "root", "png:-")
		case "gnome-screenshot":
			if out, err := captureToTemporaryFile(name, "-f"); err == nil {
				return out, nil
			}
			continue
		case "maim":
			cmd = exec.Command(name, "-u", "-")
		case "grim":
			cmd = exec.Command(name, "-")
		}
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			return out, nil
		}
	}
	return nil, ErrUnsupported
}

func captureToTemporaryFile(command string, beforePath ...string) ([]byte, error) {
	directory, err := os.MkdirTemp("", "yudesk-capture-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "screen.png")
	args := append(append([]string{}, beforePath...), path)
	if err := exec.Command(command, args...).Run(); err != nil {
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
	if runtime.GOOS == "linux" {
		for _, e := range events {
			var args []string
			key := linuxKeyName(e.Key)
			switch e.Type {
			case "move":
				args = []string{"mousemove", strconv.Itoa(e.X), strconv.Itoa(e.Y)}
			case "down":
				args = []string{"mousedown", strconv.Itoa(e.Button)}
			case "up":
				args = []string{"mouseup", strconv.Itoa(e.Button)}
			case "key":
				args = []string{"key", key}
			case "key_down":
				args = []string{"keydown", key}
			case "key_up":
				args = []string{"keyup", key}
			case "wheel":
				button := "5"
				if e.DeltaY < 0 {
					button = "4"
				}
				args = []string{"click", button}
			default:
				return fmt.Errorf("unknown input event %q", e.Type)
			}
			if err := exec.Command("xdotool", args...).Run(); err != nil {
				return fmt.Errorf("xdotool: %w", err)
			}
		}
		return nil
	}
	if runtime.GOOS == "darwin" {
		for _, e := range events {
			var args []string
			key := macKeyName(e.Key)
			switch e.Type {
			case "move":
				args = []string{"m:" + strconv.Itoa(e.X) + "," + strconv.Itoa(e.Y)}
			case "down":
				if e.Button == 1 {
					args = []string{"dd:" + strconv.Itoa(e.X) + "," + strconv.Itoa(e.Y)}
				}
			case "up":
				if e.Button == 1 {
					args = []string{"du:" + strconv.Itoa(e.X) + "," + strconv.Itoa(e.Y)}
				} else if e.Button == 3 {
					args = []string{"rc:" + strconv.Itoa(e.X) + "," + strconv.Itoa(e.Y)}
				}
			case "key":
				args = []string{"kp:" + key}
			case "key_down":
				args = []string{"kd:" + key}
			case "key_up":
				args = []string{"ku:" + key}
			case "wheel":
				args = []string{"w:" + strconv.Itoa(e.DeltaY)}
			default:
				return fmt.Errorf("unknown input event %q", e.Type)
			}
			if len(args) == 0 {
				continue
			}
			if err := exec.Command("cliclick", args...).Run(); err != nil {
				return fmt.Errorf("cliclick: %w", err)
			}
		}
		return nil
	}
	return ErrUnsupported
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
	if runtime.GOOS == "darwin" {
		b, err := exec.Command("pbpaste").Output()
		return string(b), err
	}
	for _, cmd := range [][]string{{"xclip", "-selection", "clipboard", "-o"}, {"xsel", "--clipboard", "--output"}} {
		b, err := exec.Command(cmd[0], cmd[1:]...).Output()
		if err == nil {
			return string(b), nil
		}
	}
	return "", ErrUnsupported
}

func clipboardSetPlatform(value string) error {
	if runtime.GOOS == "darwin" {
		c := exec.Command("pbcopy")
		c.Stdin = bytes.NewBufferString(value)
		return c.Run()
	}
	for _, cmd := range [][]string{{"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}} {
		c := exec.Command(cmd[0], cmd[1:]...)
		c.Stdin = bytes.NewBufferString(value)
		if err := c.Run(); err == nil {
			return nil
		}
	}
	return ErrUnsupported
}
