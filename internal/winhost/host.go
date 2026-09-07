package winhost

import (
	"encoding/json"
)

// Only capture and input are accepted. No filesystem, commands, tokens, process
// creation or credential APIs are exposed by the privileged desktop worker.
type Handler func(string, json.RawMessage) ([]byte, map[string]any, error)
type State struct {
	Supported     bool   `json:"supported"`
	Installed     bool   `json:"installed"`
	Running       bool   `json:"running"`
	Ready         bool   `json:"ready"`
	TrustedClient bool   `json:"trustedClient"`
	Path          string `json:"path"`
	Message       string `json:"message"`
}
