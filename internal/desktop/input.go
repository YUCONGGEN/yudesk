package desktop

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Bound the allocation for Unicode packets and the size of external commands.
const maxInputTextBytes = 64 * 1024

func validateInputText(text string) error {
	if len(text) > maxInputTextBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return fmt.Errorf("%w: invalid committed text", ErrInputRejected)
	}
	return nil
}

func benignIMEKey(key string) bool {
	switch key {
	case "Process", "Unidentified", "Dead":
		return true
	}
	return false
}
