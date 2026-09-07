//go:build !windows

package desktop

import "encoding/json"

func PrivilegedOperation(string, json.RawMessage) ([]byte, map[string]any, error) {
	return nil, nil, ErrUnsupported
}
