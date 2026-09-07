package desktop

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

func TestInputEventKeyboardJSONCompatibility(t *testing.T) {
	for _, event := range []InputEvent{
		{Type: "key_down", Key: "!", Code: "Digit1"},
		{Type: "text", Text: "中文😀"},
		{Type: "key", Key: "Enter"},
	} {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var got InputEvent
		if err := json.Unmarshal(encoded, &got); err != nil || got != event {
			t.Fatal("keyboard event did not round trip", err)
		}
		if event.Code == "" && strings.Contains(string(encoded), `"code"`) || event.Text == "" && strings.Contains(string(encoded), `"text"`) {
			t.Fatal("optional fields were not omitted")
		}
	}
}

func TestWindowsPhysicalKeys(t *testing.T) {
	tests := []struct {
		code, key string
		scan      uint16
		extended  bool
	}{
		{"KeyA", "Q", 0x1e, false}, {"KeyZ", "w", 0x2c, false},
		{"Digit0", ")", 0x0b, false}, {"Digit1", "!", 0x02, false},
		{"Digit2", "@", 0x03, false}, {"Digit3", "#", 0x04, false},
		{"Digit4", "$", 0x05, false}, {"Digit5", "%", 0x06, false},
		{"Digit6", "^", 0x07, false}, {"Digit7", "&", 0x08, false},
		{"Digit8", "*", 0x09, false}, {"Digit9", "(", 0x0a, false},
		{"Minus", "_", 0x0c, false}, {"Equal", "+", 0x0d, false},
		{"BracketLeft", "{", 0x1a, false}, {"BracketRight", "}", 0x1b, false},
		{"Backslash", "|", 0x2b, false}, {"Semicolon", ":", 0x27, false},
		{"Quote", `"`, 0x28, false}, {"Backquote", "~", 0x29, false},
		{"Comma", "<", 0x33, false}, {"Period", ">", 0x34, false}, {"Slash", "?", 0x35, false},
		{"CapsLock", "CapsLock", 0x3a, false}, {"NumLock", "NumLock", 0x45, true},
		{"ScrollLock", "ScrollLock", 0x46, false}, {"PrintScreen", "PrintScreen", 0x37, true},
		{"ShiftLeft", "Shift", 0x2a, false}, {"ShiftRight", "Shift", 0x36, false},
		{"ControlLeft", "Control", 0x1d, false}, {"ControlRight", "Control", 0x1d, true},
		{"AltLeft", "Alt", 0x38, false}, {"AltRight", "AltGraph", 0x38, true},
		{"MetaLeft", "Meta", 0x5b, true}, {"MetaRight", "Meta", 0x5c, true},
		{"ContextMenu", "ContextMenu", 0x5d, true},
		{"ArrowLeft", "ArrowLeft", 0x4b, true}, {"ArrowRight", "ArrowRight", 0x4d, true},
		{"ArrowUp", "ArrowUp", 0x48, true}, {"ArrowDown", "ArrowDown", 0x50, true},
		{"Home", "Home", 0x47, true}, {"End", "End", 0x4f, true},
		{"PageUp", "PageUp", 0x49, true}, {"PageDown", "PageDown", 0x51, true},
		{"Insert", "Insert", 0x52, true}, {"Delete", "Delete", 0x53, true},
		{"Enter", "Enter", 0x1c, false}, {"Space", " ", 0x39, false},
		{"NumpadEnter", "Enter", 0x1c, true}, {"NumpadDivide", "/", 0x35, true},
		{"NumpadMultiply", "*", 0x37, false}, {"NumpadAdd", "+", 0x4e, false},
		{"NumpadSubtract", "-", 0x4a, false}, {"NumpadDecimal", "Delete", 0x53, false},
		{"NumpadEqual", "=", 0x59, false}, {"NumpadComma", ",", 0x7e, false},
		{"Numpad0", "Insert", 0x52, false}, {"Numpad1", "End", 0x4f, false},
		{"Numpad2", "ArrowDown", 0x50, false}, {"Numpad3", "PageDown", 0x51, false},
		{"Numpad4", "ArrowLeft", 0x4b, false}, {"Numpad5", "Clear", 0x4c, false},
		{"Numpad6", "ArrowRight", 0x4d, false}, {"Numpad7", "Home", 0x47, false},
		{"Numpad8", "ArrowUp", 0x48, false}, {"Numpad9", "PageUp", 0x49, false},
		{"IntlBackslash", "<", 0x56, false}, {"IntlRo", "\\", 0x73, false}, {"IntlYen", "¥", 0x7d, false},
		{"F1", "F1", 0x3b, false}, {"F10", "F10", 0x44, false},
		{"F11", "F11", 0x57, false}, {"F12", "F12", 0x58, false},
		{"F13", "F13", 0x64, false}, {"F24", "F24", 0x76, false},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			for _, kind := range []string{"key_down", "key_up", "key"} {
				inputs, err := buildWindowsInput(InputEvent{Type: kind, Code: tt.code, Key: tt.key}, 0, 0)
				count := 1
				if kind == "key" {
					count = 2
				}
				if err != nil || len(inputs) != count {
					t.Fatalf("%s: count=%d, error=%v", kind, len(inputs), err)
				}
				for i, in := range inputs {
					flags := uint32(winKeyScanCode)
					if tt.extended {
						flags |= winKeyExtended
					}
					if kind == "key_up" || i == 1 {
						flags |= winKeyUp
					}
					want := keyboardInput{Scan: tt.scan, Flags: flags}
					if got := windowsRecordKey(in); in.Type != winInputKeyboard || got != want {
						t.Fatalf("%s record %d: got %+v, want %+v", kind, i, got, want)
					}
				}
			}
		})
	}
}

func TestWindowsLegacyKeysAndStrictValidation(t *testing.T) {
	tests := []struct {
		key      string
		vk       uint16
		extended bool
	}{
		{"a", 0x41, false}, {"Z", 0x5a, false}, {"1", 0x31, false},
		{"CapsLock", 0x14, false}, {"ArrowLeft", 0x25, true}, {"NumLock", 0x90, true},
		{"PrintScreen", 0x2c, true}, {"ContextMenu", 0x5d, true}, {"Meta", 0x5b, true},
		{"Control", 0x11, false}, {"AltGraph", 0xa5, true}, {"F24", 0x87, false},
	}
	for _, punctuation := range []struct {
		chars string
		vk    uint16
	}{
		{"!", '1'}, {"@", '2'}, {"#", '3'}, {"$", '4'}, {"%", '5'},
		{"^", '6'}, {"&", '7'}, {"*", '8'}, {"(", '9'}, {")", '0'},
		{";:", 0xba}, {"=+", 0xbb}, {",<", 0xbc}, {"-_", 0xbd}, {".>", 0xbe},
		{"/?", 0xbf}, {"`~", 0xc0}, {"[{", 0xdb}, {"\\|", 0xdc}, {"]}", 0xdd}, {"'\"", 0xde},
	} {
		for _, c := range punctuation.chars {
			tests = append(tests, struct {
				key      string
				vk       uint16
				extended bool
			}{string(c), punctuation.vk, false})
		}
	}
	for _, tt := range tests {
		for _, kind := range []string{"key_down", "key_up"} {
			inputs, err := buildWindowsInput(InputEvent{Type: kind, Key: tt.key}, 0, 0)
			if err != nil || len(inputs) != 1 {
				t.Fatalf("legacy key %q: %v", tt.key, err)
			}
			flags := uint32(0)
			if tt.extended {
				flags |= winKeyExtended
			}
			if kind == "key_up" {
				flags |= winKeyUp
			}
			if got := windowsRecordKey(inputs[0]); got != (keyboardInput{VK: tt.vk, Flags: flags}) {
				t.Fatalf("legacy key %q: %+v", tt.key, got)
			}
		}
	}
	for _, key := range []string{"", "F0", "F25", "F01", "F1junk", "F+1", "F 1", "中文", "😀", "PRIVATE-UNSUPPORTED-KEY"} {
		inputs, err := buildWindowsInput(InputEvent{Type: "key", Key: key}, 0, 0)
		if len(inputs) != 0 || !errors.Is(err, ErrUnsupportedInput) || !errors.Is(err, ErrInputRejected) {
			t.Fatalf("invalid key not safely rejected: count=%d error=%v", len(inputs), err)
		}
		if len(key) > 3 && strings.Contains(err.Error(), key) {
			t.Fatal("validation error leaks key data")
		}
	}
	for _, event := range []InputEvent{
		{Type: "key_down", Code: "PRIVATE-CODE", Key: "a"},
		{Type: "PRIVATE-TYPE"}, {Type: "down", Button: 42},
	} {
		inputs, err := buildWindowsInput(event, 0, 0)
		if len(inputs) != 0 || !errors.Is(err, ErrInputRejected) || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("invalid input not safely rejected: %v", err)
		}
	}
}

func TestWindowsIMEAndPhysicalShortcuts(t *testing.T) {
	for _, key := range []string{"Process", "Unidentified", "Dead"} {
		for _, code := range []string{"", "Unidentified", "UnknownIMECode"} {
			for _, kind := range []string{"key_down", "key_up", "key"} {
				inputs, err := buildWindowsInput(InputEvent{Type: kind, Key: key, Code: code}, 0, 0)
				if err != nil || len(inputs) != 0 {
					t.Fatal("benign IME key must be ignored, including release", err)
				}
				if err := injectWindowsInput(inputs, func([]winInput) (int, error) {
					t.Fatal("ignored IME key reached the injector")
					return 0, nil
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, event := range []InputEvent{
		{Type: "key_down", Code: "ControlLeft", Key: "Control"},
		{Type: "key_down", Code: "AltRight", Key: "AltGraph"},
		{Type: "key_down", Code: "KeyC", Key: "ç"},
		{Type: "key_down", Code: "KeyC", Key: "Process"},
		{Type: "key_down", Code: "Quote", Key: "Dead"},
	} {
		inputs, err := buildWindowsInput(event, 0, 0)
		if err != nil || len(inputs) != 1 || windowsRecordKey(inputs[0]).Flags&winKeyScanCode == 0 {
			t.Fatal("physical Code was not preserved", err)
		}
	}
	inputs, err := buildWindowsInput(InputEvent{Type: "key", Code: "Pause"}, 0, 0)
	if err != nil || len(inputs) != 2 || windowsRecordKey(inputs[0]) != (keyboardInput{VK: 0x13}) {
		t.Fatal("Pause must use its VK, not an E0/NumLock scan", err)
	}
	inputs, err = buildWindowsInput(InputEvent{Type: "key", Code: "NumpadClear"}, 0, 0)
	if err != nil || len(inputs) != 2 || windowsRecordKey(inputs[0]) != (keyboardInput{VK: 0x0c}) {
		t.Fatal("Clear must not become Numpad5", err)
	}
}

func TestWindowsUnicodeCommittedText(t *testing.T) {
	inputs, err := buildWindowsInput(InputEvent{Type: "text", Text: "A中😀e\u0301\n", Code: "KeyZ", Key: "z"}, 0, 0)
	units := []uint16{0x0041, 0x4e2d, 0xd83d, 0xde00, 0x0065, 0x0301, 0x000a}
	if err != nil || len(inputs) != len(units)*2 {
		t.Fatalf("unicode count=%d error=%v", len(inputs), err)
	}
	for i, unit := range units {
		for j := 0; j < 2; j++ {
			flags := uint32(winKeyUnicode)
			if j == 1 {
				flags |= winKeyUp
			}
			if got := windowsRecordKey(inputs[i*2+j]); got != (keyboardInput{Scan: unit, Flags: flags}) {
				t.Fatalf("unit %d record %d: %+v", i, j, got)
			}
		}
	}
	if inputs, err := buildWindowsInput(InputEvent{Type: "text"}, 0, 0); err != nil || len(inputs) != 0 {
		t.Fatal("empty committed text is not a no-op")
	}
	for _, text := range []string{"PRIVATE\x00TEXT", "PRIVATE\xffTEXT", strings.Repeat("A", maxInputTextBytes+1)} {
		inputs, err := buildWindowsInput(InputEvent{Type: "text", Text: text}, 0, 0)
		if len(inputs) != 0 || !errors.Is(err, ErrInputRejected) || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("invalid committed text was not safely rejected", err)
		}
	}
	if err := validateInputText(strings.Repeat("A", maxInputTextBytes)); err != nil {
		t.Fatal("maximum allowed text rejected", err)
	}
}

func TestWindowsInjectionPartialUnicodeAndTapCleanup(t *testing.T) {
	for _, event := range []InputEvent{
		{Type: "text", Text: "中😀"},
		{Type: "key", Code: "ControlRight"},
	} {
		inputs, err := buildWindowsInput(event, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		for inserted := 0; inserted < len(inputs); inserted++ {
			osErr := errors.New("mock OS failure")
			calls := 0
			err := injectWindowsInput(inputs, func(packet []winInput) (int, error) {
				calls++
				if calls == 1 {
					if !reflect.DeepEqual(packet, inputs) {
						t.Fatal("packet changed before sending")
					}
					return inserted, osErr
				}
				if calls != 2 || inserted%2 != 1 || len(packet) != 1 {
					t.Fatalf("unexpected cleanup: prefix=%d calls=%d size=%d", inserted, calls, len(packet))
				}
				want := windowsRecordKey(inputs[inserted-1])
				want.Flags |= winKeyUp
				if got := windowsRecordKey(packet[0]); got != want {
					t.Fatalf("wrong release: %+v, want %+v", got, want)
				}
				return len(packet), nil
			})
			if !errors.Is(err, osErr) || errors.Is(err, ErrInputRejected) {
				t.Fatal("OS injection failure confused with validation", err)
			}
			if calls != 1+inserted%2 {
				t.Fatal("partial packet was retried or left unreleased")
			}
		}
	}
}

func TestWindowsInjectionSinglePhysicalEventAndErrorEdges(t *testing.T) {
	for _, event := range []InputEvent{
		{Type: "key_down", Code: "AltRight"}, {Type: "key_up", Code: "AltRight"},
		{Type: "key", Code: "AltRight"}, {Type: "text", Text: "中😀"},
	} {
		inputs, err := buildWindowsInput(event, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		err = injectWindowsInput(inputs, func(packet []winInput) (int, error) {
			calls++
			if event.Type != "text" && event.Type != "key" && len(packet) != 1 {
				t.Fatal("physical key event split into multiple records")
			}
			return len(packet), errors.New("stale last error")
		})
		if err != nil || calls != 1 {
			t.Fatal("successful physical event did not use one call", err)
		}
	}
	inputs, _ := buildWindowsInput(InputEvent{Type: "text", Text: "😀"}, 0, 0)
	calls := 0
	cleanupErr := errors.New("mock cleanup failure")
	err := injectWindowsInput(inputs, func([]winInput) (int, error) {
		calls++
		if calls == 1 {
			return 1, nil // UIPI may leave GetLastError at zero.
		}
		return 0, cleanupErr
	})
	if !errors.Is(err, cleanupErr) || calls != 2 || !strings.Contains(err.Error(), "cleanup") || strings.Contains(err.Error(), "😀") {
		t.Fatal("cleanup failure missing or unsafe", err)
	}
	for _, inserted := range []int{-1, len(inputs) + 1} {
		err := injectWindowsInput(inputs, func([]winInput) (int, error) { return inserted, nil })
		if err == nil {
			t.Fatal("invalid injector count accepted")
		}
	}
}

func TestWindowsPendingReleasesOnlyUnbalancedDowns(t *testing.T) {
	ctrl := windowsKeyboardRecord(scanKey(0x1d, true))
	alt := windowsKeyboardRecord(scanKey(0x38, true))
	a := windowsKeyboardRecord(scanKey(0x1e, false))
	aUp := windowsRecordKey(a)
	aUp.Flags |= winKeyUp
	packet := []winInput{ctrl, ctrl, alt, a, windowsKeyboardRecord(aUp), windowsMouseRecord(mouseInput{Flags: 2})}
	releases := windowsPendingReleases(packet)
	if len(releases) != 2 {
		t.Fatal("repeated/already released/non-key records became extra releases")
	}
	for i, original := range []winInput{alt, ctrl} {
		want := windowsRecordKey(original)
		want.Flags |= winKeyUp
		if windowsRecordKey(releases[i]) != want {
			t.Fatal("cleanup did not release held keys in reverse order")
		}
	}
}

func TestWindowsInputABILayout(t *testing.T) {
	var in winInput
	word := unsafe.Sizeof(uintptr(0))
	wantSize, wantDataOffset := uintptr(28), uintptr(4)
	wantExtraOffset := uintptr(12)
	if word == 8 {
		wantSize, wantDataOffset, wantExtraOffset = 40, 8, 16
	}
	var key keyboardInput
	if unsafe.Sizeof(in) != wantSize || unsafe.Offsetof(in.Data) != wantDataOffset || unsafe.Offsetof(key.Extra) != wantExtraOffset {
		t.Fatalf("incorrect INPUT ABI: size=%d data=%d extra=%d", unsafe.Sizeof(in), unsafe.Offsetof(in.Data), unsafe.Offsetof(key.Extra))
	}
	key = keyboardInput{VK: 0xa3, Scan: 0x1d, Flags: winKeyExtended, Time: 123, Extra: 456}
	if windowsRecordKey(windowsKeyboardRecord(key)) != key {
		t.Fatal("keyboard union record changed")
	}
	mouse := mouseInput{DX: 123, DY: 456, MouseData: 789, Flags: 2, Time: 10, Extra: 11}
	in = windowsMouseRecord(mouse)
	if got := *(*mouseInput)(unsafe.Pointer(&in.Data[0])); got != mouse || in.Type != 0 {
		t.Fatal("mouse union record changed")
	}
}
