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
	Type               string          `json:"type"`
	Nonce              []byte          `json:"nonce,omitempty"`
	Signature          []byte          `json:"signature,omitempty"`
	Active             bool            `json:"active"`
	ActiveUntil        time.Time       `json:"activeUntil,omitempty"`
	Code               string          `json:"code,omitempty"`
	Message            string          `json:"message,omitempty"`
	Topic              string          `json:"topic,omitempty"`
	DeviceCode         string          `json:"deviceCode,omitempty"`
	PIN                string          `json:"pin,omitempty"`
	PortMaps           []PortMap       `json:"portMaps,omitempty"`
	PortMapCommand     *PortMapCommand `json:"portMapCommand,omitempty"`
	PortMapResult      *PortMapResult  `json:"portMapResult,omitempty"`
	PortMapServer      string          `json:"portMapServer,omitempty"`
	PortMapSession     string          `json:"portMapSession,omitempty"`
	PortMapCertificate string          `json:"portMapCertificate,omitempty"`
}

// PortMap is a server-issued TCP mapping. The public port is never selected by
// the client, which lets the relay enforce global uniqueness and per-device
// limits atomically.
type PortMap struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Protocol      string    `json:"protocol"`
	LocalPort     int       `json:"localPort"`
	RemotePort    int       `json:"remotePort"`
	PublicAddress string    `json:"publicAddress"`
	Secret        string    `json:"secret,omitempty"`
	Online        bool      `json:"online"`
	CreatedAt     time.Time `json:"createdAt"`
}

type PortMapCommand struct {
	RequestID string `json:"requestID"`
	Action    string `json:"action"`
	MapID     string `json:"mapID,omitempty"`
	Name      string `json:"name,omitempty"`
	LocalPort int    `json:"localPort,omitempty"`
}

type PortMapResult struct {
	RequestID string `json:"requestID"`
	OK        bool   `json:"ok"`
	Message   string `json:"message,omitempty"`
}

func ControlProof(deviceID string, nonce []byte) []byte {
	return append([]byte("yudesk-management-v1:"+deviceID+":"), nonce...)
}

// WatchDevice is separate from the encrypted desktop stream, but runs inside
// the same process. The relay is authenticated by TLS and the device proves
// possession of its identity key with a fresh challenge.
func WatchDevice(ctx context.Context, addr string, options DialOptions, hello Hello, key ed25519.PrivateKey, status func(ControlMessage)) error {
	return WatchDeviceWithPIN(ctx, addr, options, hello, key, nil, status)
}

// The live PIN travels only over the authenticated management connection. A
// change does not close that connection or interrupt an established desktop.
func WatchDeviceWithPIN(ctx context.Context, addr string, options DialOptions, hello Hello, key ed25519.PrivateKey, currentPIN func() string, status func(ControlMessage)) error {
	return WatchManagedDevice(ctx, addr, options, hello, key, func() ControlMessage {
		reply := ControlMessage{Type: "pong"}
		if currentPIN != nil {
			reply.PIN = currentPIN()
		}
		return reply
	}, status)
}

// WatchManagedDevice carries device state and small control commands. It is
// intentionally separate from desktop/video traffic and remains protected by
// the relay certificate plus the device's Ed25519 identity.
func WatchManagedDevice(ctx context.Context, addr string, options DialOptions, hello Hello, key ed25519.PrivateKey, replyState func() ControlMessage, status func(ControlMessage)) error {
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
		reply := ControlMessage{Type: "pong"}
		if replyState != nil {
			reply = replyState()
			reply.Type = "pong"
		}
		if err := json.NewEncoder(c).Encode(reply); err != nil {
			return err
		}
	}
}
