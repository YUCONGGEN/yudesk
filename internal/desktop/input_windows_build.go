package desktop

import (
	"errors"
	"fmt"
	"strconv"
	"unicode/utf16"
	"unsafe"
)

// These builders have no OS dependencies, so tests can inspect the actual
// SendInput records on Linux without ever loading user32 or injecting input.
const (
	winInputKeyboard = 1
	winKeyExtended   = 0x0001
	winKeyUp         = 0x0002
	winKeyUnicode    = 0x0004
	winKeyScanCode   = 0x0008
)

type mouseInput struct {
	DX, DY                 int32
	MouseData, Flags, Time uint32
	Extra                  uintptr
}

type keyboardInput struct {
	VK, Scan    uint16
	Flags, Time uint32
	Extra       uintptr
}

type winInput struct {
	Type uint32
	// INPUT's union has pointer alignment: sizeof(INPUT) is 40 on 64-bit
	// Windows and 28 on 32-bit Windows. Do not hardcode either size.
	_    [0]uintptr
	Data [unsafe.Sizeof(mouseInput{})]byte
}

func windowsKeyboardRecord(key keyboardInput) winInput {
	in := winInput{Type: winInputKeyboard}
	*(*keyboardInput)(unsafe.Pointer(&in.Data[0])) = key
	return in
}

func windowsMouseRecord(mouse mouseInput) winInput {
	in := winInput{}
	*(*mouseInput)(unsafe.Pointer(&in.Data[0])) = mouse
	return in
}

func windowsRecordKey(in winInput) keyboardInput {
	return *(*keyboardInput)(unsafe.Pointer(&in.Data[0]))
}

func buildWindowsInput(event InputEvent, width, height int) ([]winInput, error) {
	switch event.Type {
	case "move":
		return []winInput{windowsMouseRecord(mouseInput{
			DX: absolutePixel(event.X, width), DY: absolutePixel(event.Y, height), Flags: 0x0001 | 0x8000,
		})}, nil
	case "down", "up":
		flags := mouseButtonFlags(event.Button, event.Type == "up")
		if flags == 0 {
			return nil, fmt.Errorf("%w: mouse button", ErrInputRejected)
		}
		return []winInput{windowsMouseRecord(mouseInput{Flags: flags})}, nil
	case "wheel":
		return []winInput{windowsMouseRecord(mouseInput{MouseData: uint32(-int64(event.DeltaY)), Flags: 0x0800})}, nil
	case "key", "key_down", "key_up":
		key, ignored, err := windowsEventKey(event)
		if err != nil || ignored {
			return nil, err
		}
		if event.Type == "key_up" {
			key.Flags |= winKeyUp
		}
		inputs := []winInput{windowsKeyboardRecord(key)}
		if event.Type == "key" {
			key.Flags |= winKeyUp
			inputs = append(inputs, windowsKeyboardRecord(key))
		}
		return inputs, nil
	case "text":
		if err := validateInputText(event.Text); err != nil {
			return nil, err
		}
		units := utf16.Encode([]rune(event.Text))
		inputs := make([]winInput, 0, len(units)*2)
		for _, unit := range units {
			// SendInput accepts UTF-16 units, including both halves of a
			// supplementary character. Unicode always uses wVk=0.
			inputs = append(inputs,
				windowsKeyboardRecord(keyboardInput{Scan: unit, Flags: winKeyUnicode}),
				windowsKeyboardRecord(keyboardInput{Scan: unit, Flags: winKeyUnicode | winKeyUp}),
			)
		}
		return inputs, nil
	default:
		return nil, fmt.Errorf("%w: event type", ErrInputRejected)
	}
}

func windowsEventKey(event InputEvent) (keyboardInput, bool, error) {
	if event.Code != "" && event.Code != "Unidentified" {
		if key, ok := windowsPhysicalKey(event.Code); ok {
			return key, false, nil
		}
		if benignIMEKey(event.Key) {
			return keyboardInput{}, true, nil
		}
		return keyboardInput{}, false, fmt.Errorf("%w: physical key", ErrInputRejected)
	}
	if benignIMEKey(event.Key) {
		return keyboardInput{}, true, nil
	}
	if key, ok := windowsLegacyKey(event.Key); ok {
		return key, false, nil
	}
	return keyboardInput{}, false, fmt.Errorf("%w: key", ErrInputRejected)
}

func scanKey(scan uint16, extended bool) keyboardInput {
	key := keyboardInput{Scan: scan, Flags: winKeyScanCode}
	if extended {
		key.Flags |= winKeyExtended
	}
	return key
}

// Code describes the physical position, independently of the viewer's layout
// or whether Shift/CapsLock changed KeyboardEvent.key. The host layout decides
// the resulting character; committed IME text uses Unicode instead.
func windowsPhysicalKey(code string) (keyboardInput, bool) {
	if len(code) == 4 && code[:3] == "Key" && code[3] >= 'A' && code[3] <= 'Z' {
		scans := [...]uint16{0x1e, 0x30, 0x2e, 0x20, 0x12, 0x21, 0x22, 0x23, 0x17, 0x24, 0x25, 0x26, 0x32, 0x31, 0x18, 0x19, 0x10, 0x13, 0x1f, 0x14, 0x16, 0x2f, 0x11, 0x2d, 0x15, 0x2c}
		return scanKey(scans[code[3]-'A'], false), true
	}
	if len(code) == 6 && code[:5] == "Digit" && code[5] >= '0' && code[5] <= '9' {
		scans := [...]uint16{0x0b, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a}
		return scanKey(scans[code[5]-'0'], false), true
	}
	if len(code) == 7 && code[:6] == "Numpad" && code[6] >= '0' && code[6] <= '9' {
		scans := [...]uint16{0x52, 0x4f, 0x50, 0x51, 0x4b, 0x4c, 0x4d, 0x47, 0x48, 0x49}
		return scanKey(scans[code[6]-'0'], false), true
	}
	if n := functionKeyNumber(code); n != 0 {
		scans := [...]uint16{0x3b, 0x3c, 0x3d, 0x3e, 0x3f, 0x40, 0x41, 0x42, 0x43, 0x44, 0x57, 0x58, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x76}
		return scanKey(scans[n-1], false), true
	}
	if scan, ok := windowsCodeScans[code]; ok {
		return scanKey(scan&0xff, scan&0xe000 != 0), true
	}
	switch code {
	case "Pause":
		// Pause uses an E1 sequence, which KEYEVENTF_EXTENDEDKEY (E0) cannot
		// represent. Let Windows expand VK_PAUSE rather than send NumLock.
		return keyboardInput{VK: 0x13}, true
	case "NumpadClear":
		// Clear has no distinct Windows scan code. Sending the Numpad5
		// position here would type a digit when NumLock is on.
		return keyboardInput{VK: 0x0c}, true
	}
	return keyboardInput{}, false
}

// Windows scan codes match the browser's physical Code positions:
// https://chromium.googlesource.com/chromium/src/+/main/ui/events/keycodes/dom/dom_code_data.inc
var windowsCodeScans = map[string]uint16{
	"Escape": 0x01, "Backspace": 0x0e, "Tab": 0x0f, "Enter": 0x1c, "Space": 0x39,
	"Minus": 0x0c, "Equal": 0x0d, "BracketLeft": 0x1a, "BracketRight": 0x1b,
	"Backslash": 0x2b, "Semicolon": 0x27, "Quote": 0x28, "Backquote": 0x29,
	"Comma": 0x33, "Period": 0x34, "Slash": 0x35,
	"IntlBackslash": 0x56, "IntlRo": 0x73, "IntlYen": 0x7d,
	"ControlLeft": 0x1d, "ControlRight": 0xe01d, "ShiftLeft": 0x2a, "ShiftRight": 0x36,
	"AltLeft": 0x38, "AltRight": 0xe038, "MetaLeft": 0xe05b, "MetaRight": 0xe05c,
	"OSLeft": 0xe05b, "OSRight": 0xe05c, "ContextMenu": 0xe05d,
	"CapsLock": 0x3a, "NumLock": 0xe045, "ScrollLock": 0x46, "PrintScreen": 0xe037,
	"Insert": 0xe052, "Delete": 0xe053, "Home": 0xe047, "End": 0xe04f,
	"PageUp": 0xe049, "PageDown": 0xe051, "ArrowLeft": 0xe04b, "ArrowRight": 0xe04d,
	"ArrowUp": 0xe048, "ArrowDown": 0xe050,
	"NumpadDecimal": 0x53, "NumpadAdd": 0x4e, "NumpadSubtract": 0x4a,
	"NumpadMultiply": 0x37, "NumpadDivide": 0xe035, "NumpadEnter": 0xe01c,
	"NumpadComma": 0x7e, "NumpadEqual": 0x59,
}

func functionKeyNumber(key string) int {
	if len(key) < 2 || len(key) > 3 || key[0] != 'F' {
		return 0
	}
	n, err := strconv.Atoi(key[1:])
	if err != nil || n < 1 || n > 24 || strconv.Itoa(n) != key[1:] {
		return 0
	}
	return n
}

func windowsLegacyKey(key string) (keyboardInput, bool) {
	if len(key) == 1 {
		c := key[0]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			return keyboardInput{VK: uint16(c)}, true
		}
		if vk := windowsPunctuationVK[c]; vk != 0 {
			// Modifiers arrive as their own events. Do not synthesize Shift
			// from the key label and accidentally disturb a held modifier.
			return keyboardInput{VK: vk}, true
		}
	}
	if n := functionKeyNumber(key); n != 0 {
		return keyboardInput{VK: uint16(0x6f + n)}, true
	}
	if vk, ok := windowsNamedVK[key]; ok {
		flags := uint32(0)
		if vk&0x100 != 0 {
			flags = winKeyExtended
		}
		return keyboardInput{VK: vk & 0xff, Flags: flags}, true
	}
	return keyboardInput{}, false
}

var windowsPunctuationVK = map[byte]uint16{
	' ': 0x20, '!': '1', '@': '2', '#': '3', '$': '4', '%': '5',
	'^': '6', '&': '7', '*': '8', '(': '9', ')': '0',
	';': 0xba, ':': 0xba, '=': 0xbb, '+': 0xbb, ',': 0xbc, '<': 0xbc,
	'-': 0xbd, '_': 0xbd, '.': 0xbe, '>': 0xbe, '/': 0xbf, '?': 0xbf,
	'`': 0xc0, '~': 0xc0, '[': 0xdb, '{': 0xdb, '\\': 0xdc, '|': 0xdc,
	']': 0xdd, '}': 0xdd, '\'': 0xde, '"': 0xde,
}

// Bit 8 is internal metadata for KEYEVENTF_EXTENDEDKEY, not part of the VK.
var windowsNamedVK = map[string]uint16{
	"Enter": 0x0d, "Escape": 0x1b, "Esc": 0x1b, "Backspace": 0x08, "Tab": 0x09,
	"Space": 0x20, "Spacebar": 0x20, "Control": 0x11, "Shift": 0x10, "Alt": 0x12,
	"Meta": 0x15b, "OS": 0x15b, "AltGraph": 0x1a5,
	"ControlLeft": 0xa2, "ControlRight": 0x1a3, "ShiftLeft": 0xa0, "ShiftRight": 0xa1,
	"AltLeft": 0xa4, "AltRight": 0x1a5, "MetaLeft": 0x15b, "MetaRight": 0x15c,
	"CapsLock": 0x14, "NumLock": 0x190, "ScrollLock": 0x91, "Pause": 0x13,
	"PrintScreen": 0x12c, "ContextMenu": 0x15d, "Clear": 0x0c,
	"ArrowLeft": 0x125, "ArrowUp": 0x126, "ArrowRight": 0x127, "ArrowDown": 0x128,
	"Delete": 0x12e, "Insert": 0x12d, "Home": 0x124, "End": 0x123,
	"PageUp": 0x121, "PageDown": 0x122,
}

type windowsInputSender func([]winInput) (int, error)

func injectWindowsInput(inputs []winInput, send windowsInputSender) error {
	if len(inputs) == 0 {
		return nil
	}
	inserted, cause := send(inputs)
	if inserted == len(inputs) {
		// SendInput's last-error value can be stale even after success.
		return nil
	}
	// A failed text packet or key tap can have inserted a down but not its
	// matching up. Release only outstanding downs in the accepted prefix;
	// never retry the whole packet, which would duplicate committed text.
	releases := windowsPendingReleases(inputs[:min(max(inserted, 0), len(inputs))])
	var releaseErr error
	if len(releases) > 0 {
		count, err := send(releases)
		if count != len(releases) {
			releaseErr = windowsInjectionError("SendInput cleanup", count, len(releases), err)
		}
	}
	err := windowsInjectionError("SendInput", inserted, len(inputs), cause)
	return errors.Join(err, releaseErr)
}

func windowsInjectionError(operation string, inserted, requested int, cause error) error {
	if cause != nil {
		return fmt.Errorf("%s inserted %d of %d events: %w", operation, inserted, requested, cause)
	}
	return fmt.Errorf("%s inserted %d of %d events", operation, inserted, requested)
}

func windowsPendingReleases(inputs []winInput) []winInput {
	var pending []keyboardInput
	for _, in := range inputs {
		if in.Type != winInputKeyboard {
			continue
		}
		key := windowsRecordKey(in)
		release := key.Flags&winKeyUp != 0
		key.Flags &^= winKeyUp
		index := -1
		for i, held := range pending {
			if key.VK == held.VK && key.Scan == held.Scan && key.Flags == held.Flags {
				index = i
				break
			}
		}
		if release && index >= 0 {
			pending = append(pending[:index], pending[index+1:]...)
		} else if !release && index < 0 {
			pending = append(pending, key)
		}
	}
	releases := make([]winInput, 0, len(pending))
	for i := len(pending) - 1; i >= 0; i-- {
		key := pending[i]
		key.Flags |= winKeyUp
		releases = append(releases, windowsKeyboardRecord(key))
	}
	return releases
}
