//go:build darwin || linux

package desktop

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestUnixCommittedTextLiteralArguments(t *testing.T) {
	for _, text := range []string{"中文😀e\u0301", "--help", "-file /tmp/file", "$(touch NEVER); `command`\n\"quotes\"\nkp:delete"} {
		for _, platform := range []string{"linux", "darwin"} {
			name, args, err := unixInputCommand(InputEvent{Type: "text", Text: text, Key: "unused", Code: "KeyA"}, platform)
			wantName, wantArgs := "xdotool", []string{"type", "--clearmodifiers", "--delay", "0", "--", text}
			if platform == "darwin" {
				wantName, wantArgs = "cliclick", []string{"t:" + text}
			}
			if err != nil || name != wantName || !reflect.DeepEqual(args, wantArgs) {
				t.Fatal("text is not passed as one literal argument", err)
			}
		}
	}
}

func TestUnixInputNoopsAndValidation(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		for _, event := range []InputEvent{
			{Type: "text"},
			{Type: "key_down", Key: "Process"}, {Type: "key_up", Key: "Process"},
			{Type: "key_down", Key: "Unidentified"}, {Type: "key_up", Key: "Unidentified"},
			{Type: "key_down", Key: "Dead"}, {Type: "key_up", Key: "Dead"},
		} {
			_, args, err := unixInputCommand(event, platform)
			if err != nil || len(args) != 0 {
				t.Fatal("empty text or IME key invoked a helper", err)
			}
		}
		for _, event := range []InputEvent{
			{Type: "text", Text: "PRIVATE\x00TEXT"}, {Type: "text", Text: "PRIVATE\xffTEXT"},
			{Type: "text", Text: strings.Repeat("A", maxInputTextBytes+1)},
			{Type: "key_down"}, {Type: "PRIVATE-EVENT-TYPE"},
		} {
			_, args, err := unixInputCommand(event, platform)
			if len(args) != 0 || !errors.Is(err, ErrInputRejected) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("invalid text/key/type not safely rejected", err)
			}
		}
	}
}

func TestUnixPhysicalKeyArguments(t *testing.T) {
	for _, event := range []InputEvent{
		{Type: "key_down", Key: "Control", Code: "ControlLeft"},
		{Type: "key_up", Key: "Control", Code: "ControlLeft"},
		{Type: "key", Key: "Enter", Code: "Enter"},
	} {
		for _, platform := range []string{"linux", "darwin"} {
			name, args, err := unixInputCommand(event, platform)
			if err != nil || name == "" || len(args) == 0 {
				t.Fatal("existing physical event no longer builds", err)
			}
			if platform == "linux" && (len(args) != 3 || args[1] != "--") {
				t.Fatal("physical key can be parsed as a helper option")
			}
		}
	}
}
