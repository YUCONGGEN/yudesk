package relay

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type ControlMessage struct {
	Type        string    `json:"type"`
	Nonce       []byte    `json:"nonce,omitempty"`
	Signature   []byte    `json:"signature,omitempty"`
	Active      bool      `json:"active"`
	ActiveUntil time.Time `json:"activeUntil,omitempty"`
	Code        string    `json:"code,omitempty"`
	Message     string    `json:"message,omitempty"`
}

func ControlProof(deviceID string, nonce []byte) []byte {
	return append([]byte("yudesk-management-v1:"+deviceID+":"), nonce...)
}

// WatchDevice is separate from the encrypted desktop stream, but runs inside
// the same process. The relay is authenticated by TLS and the device proves
// possession of its identity key with a fresh challenge.
func WatchDevice(ctx context.Context, addr string, options DialOptions, hello Hello, key ed25519.PrivateKey, status func(ControlMessage)) error {
	c, err := dialTransport(ctx, addr, options)
	if err != nil {
		return err
	}
	defer c.Close()
	stopCancel := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopCancel()
	hello.Role = "control"
	b, _ := json.Marshal(hello)
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err = fmt.Fprintf(c, "YU_RELAY/1 %s\n", b); err != nil {
		return err
	}
	scanner := bufio.NewScanner(c)
	scanner.Buffer(make([]byte, 1024), 16<<10)
	read := func() (ControlMessage, error) {
		var m ControlMessage
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return m, err
			}
			return m, errors.New("management connection closed")
		}
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			return m, err
		}
		if m.Type == "stop" {
			return m, &RejectionError{Code: m.Code, Message: m.Message}
		}
		return m, nil
	}
	challenge, err := read()
	if err != nil {
		return err
	}
	if challenge.Type != "challenge" || len(challenge.Nonce) != 32 {
		return errors.New("invalid management challenge")
	}
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("invalid device identity key")
	}
	if err := json.NewEncoder(c).Encode(ControlMessage{Type: "proof", Signature: ed25519.Sign(key, ControlProof(hello.ID, challenge.Nonce))}); err != nil {
		return err
	}
	for {
		_ = c.SetDeadline(time.Now().Add(15 * time.Second))
		m, err := read()
		if err != nil {
			return err
		}
		if m.Type != "status" {
			return errors.New("invalid management status")
		}
		status(m)
		if err := json.NewEncoder(c).Encode(ControlMessage{Type: "pong"}); err != nil {
			return err
		}
	}
}
