package relay

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxConferenceMessageSize = 64 << 10
)

var ErrConferenceMessageTooLarge = errors.New("conference signaling message is too large")

type ConferencePeer struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	JoinedAt   int64  `json:"joinedAt,omitempty"`
	Host       bool   `json:"host,omitempty"`
	Microphone bool   `json:"microphone,omitempty"`
	Camera     bool   `json:"camera,omitempty"`
	Screen     bool   `json:"screen,omitempty"`
	Recording  bool   `json:"recording,omitempty"`
}

type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type RTCPolicy struct {
	ICEServers      []ICEServer `json:"iceServers"`
	DirectTimeoutMS int         `json:"directTimeoutMs"`
	RelayEnabled    bool        `json:"relayEnabled,omitempty"`
}

// ConferenceMessage carries signaling, room state and temporary ICE policy.
// Camera, microphone and screen media use WebRTC/DTLS-SRTP. TURN fallback may
// forward encrypted packets but cannot read the media.
type ConferenceMessage struct {
	Type       string           `json:"type"`
	ID         string           `json:"id,omitempty"`
	From       string           `json:"from,omitempty"`
	To         string           `json:"to,omitempty"`
	Name       string           `json:"name,omitempty"`
	Topic      string           `json:"topic,omitempty"`
	JoinedAt   int64            `json:"joinedAt,omitempty"`
	Host       string           `json:"host,omitempty"`
	Peers      []ConferencePeer `json:"peers,omitempty"`
	Signal     string           `json:"signal,omitempty"`
	SDP        string           `json:"sdp,omitempty"`
	Candidate  string           `json:"candidate,omitempty"`
	Microphone bool             `json:"microphone,omitempty"`
	Camera     bool             `json:"camera,omitempty"`
	Screen     bool             `json:"screen,omitempty"`
	Recording  bool             `json:"recording,omitempty"`
	Message    string           `json:"message,omitempty"`
	RTC        *RTCPolicy       `json:"rtc,omitempty"`
}

func ValidConferenceName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 32 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func ConferenceProof(deviceID, room, action, name string, nonce []byte) []byte {
	prefix := "yudesk-conference-v1\x00" + strings.ToUpper(strings.TrimSpace(deviceID)) + "\x00" +
		strings.TrimSpace(room) + "\x00" + action + "\x00" + strings.TrimSpace(name) + "\x00"
	return append([]byte(prefix), nonce...)
}

// DialConference authenticates the local device to the verified TLS relay and
// returns a newline-delimited JSON signaling connection. The visible 9-digit
// room number is never accepted as proof of host privileges.
func DialConference(ctx context.Context, addr string, options DialOptions, deviceID string, key ed25519.PrivateKey, room, name string, host bool) (net.Conn, error) {
	deviceID = strings.ToUpper(strings.TrimSpace(deviceID))
	room, name = strings.TrimSpace(room), strings.TrimSpace(name)
	action := "join"
	if host {
		action = "host"
	}
	if len(deviceID) != 24 || strings.Trim(deviceID, "0123456789ABCDEF") != "" ||
		len(key) != ed25519.PrivateKeySize || !IsMeetingCode(room) || !ValidConferenceName(name) {
		return nil, errors.New("会议身份、姓名或会议号无效")
	}
	if !options.TLS || options.Insecure {
		return nil, errors.New("会议信令要求已验证的 TLS 中转连接")
	}
	c, err := dialTransport(ctx, addr, options)
	if err != nil {
		return nil, err
	}
	return authenticateConference(ctx, c, deviceID, key, room, name, action)
}

func authenticateConference(ctx context.Context, c net.Conn, deviceID string, key ed25519.PrivateKey, room, name, action string) (net.Conn, error) {
	fail := func(err error) (net.Conn, error) { _ = c.Close(); return nil, err }
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	hello, _ := json.Marshal(Hello{Role: "conference", ID: deviceID, Room: room, Name: name, Action: action, PublicKey: key.Public().(ed25519.PublicKey)})
	if _, err := fmt.Fprintf(c, "YU_RELAY/1 %s\n", hello); err != nil {
		return fail(err)
	}
	reader := bufio.NewReaderSize(c, 16<<10)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fail(err)
	}
	var challenge ControlMessage
	if len(line) > 16<<10 || json.Unmarshal(line, &challenge) != nil || challenge.Type != "challenge" || len(challenge.Nonce) != 32 {
		return fail(errors.New("会议信令身份验证失败"))
	}
	proof := ControlMessage{Type: "proof", Signature: ed25519.Sign(key, ConferenceProof(deviceID, room, action, name, challenge.Nonce))}
	if err := json.NewEncoder(c).Encode(proof); err != nil {
		return fail(err)
	}
	line, err = reader.ReadBytes('\n')
	if err != nil {
		return fail(err)
	}
	line = []byte(strings.TrimSpace(string(line)))
	if string(line) != "OK" {
		var rejection ControlMessage
		if json.Unmarshal(line, &rejection) == nil && rejection.Type == "stop" {
			return fail(&RejectionError{Code: rejection.Code, Message: rejection.Message})
		}
		return fail(errors.New("会议信令被服务器拒绝"))
	}
	if _, err := fmt.Fprintln(c, "READY"); err != nil {
		return fail(err)
	}
	_ = c.SetDeadline(time.Time{})
	// The buffered reader may already contain the first conference message
	// when the server coalesces "OK" and "welcome" into one TCP/TLS record.
	// Returning the bare connection would silently discard those bytes and
	// leave the client waiting forever for a welcome that already arrived.
	return &bufferedConn{Conn: c, reader: reader}, nil
}
