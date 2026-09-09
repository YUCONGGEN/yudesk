package relay

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type MeetingInfo struct {
	ID        string    `json:"id,omitempty"`
	Code      string    `json:"code"`
	Active    bool      `json:"active"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func IsMeetingCode(value string) bool {
	if len(value) != 9 || value[0] == '0' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func MeetingProof(deviceID, action string, nonce []byte) []byte {
	return append([]byte("yudesk-meeting-v1:"+strings.ToUpper(deviceID)+":"+action+":"), nonce...)
}

func OpenMeeting(ctx context.Context, addr string, options DialOptions, deviceID string, key ed25519.PrivateKey) (MeetingInfo, error) {
	return manageMeeting(ctx, addr, options, deviceID, "open", key)
}

func CloseMeeting(ctx context.Context, addr string, options DialOptions, deviceID string, key ed25519.PrivateKey) error {
	_, err := manageMeeting(ctx, addr, options, deviceID, "close", key)
	return err
}

func manageMeeting(ctx context.Context, addr string, options DialOptions, deviceID, action string, key ed25519.PrivateKey) (MeetingInfo, error) {
	var result MeetingInfo
	deviceID = strings.ToUpper(strings.TrimSpace(deviceID))
	if len(deviceID) != 24 || strings.Trim(deviceID, "0123456789ABCDEF") != "" || len(key) != ed25519.PrivateKeySize || (action != "open" && action != "close") {
		return result, errors.New("会议设备身份或操作无效")
	}
	if !options.TLS || options.Insecure {
		return result, errors.New("会议目录要求已验证的 TLS 中转连接")
	}
	c, err := dialTransport(ctx, addr, options)
	if err != nil {
		return result, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	hello, _ := json.Marshal(Hello{Role: "meeting", ID: deviceID, Action: action, PublicKey: key.Public().(ed25519.PublicKey)})
	if _, err = fmt.Fprintf(c, "YU_RELAY/1 %s\n", hello); err != nil {
		return result, err
	}
	scanner := bufio.NewScanner(c)
	scanner.Buffer(make([]byte, 1024), 16<<10)
	read := func() (ControlMessage, error) {
		var message ControlMessage
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return message, err
			}
			return message, errors.New("会议目录连接已关闭")
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return message, err
		}
		if message.Type == "stop" {
			return message, &RejectionError{Code: message.Code, Message: message.Message}
		}
		return message, nil
	}
	challenge, err := read()
	if err != nil {
		return result, err
	}
	if challenge.Type != "challenge" || len(challenge.Nonce) != 32 {
		return result, errors.New("会议目录身份验证失败")
	}
	proof := ControlMessage{Type: "proof", Signature: ed25519.Sign(key, MeetingProof(deviceID, action, challenge.Nonce))}
	if err := json.NewEncoder(c).Encode(proof); err != nil {
		return result, err
	}
	message, err := read()
	if err != nil {
		return result, err
	}
	if message.Type != "meeting" || (message.Active && (!IsMeetingCode(message.Code) || !message.ActiveUntil.After(time.Now()))) {
		return result, errors.New("服务器返回了无效会议状态")
	}
	return MeetingInfo{ID: deviceID, Code: message.Code, Active: message.Active, ExpiresAt: message.ActiveUntil}, nil
}

func ResolveMeeting(ctx context.Context, addr string, options DialOptions, code string) (MeetingInfo, error) {
	var result MeetingInfo
	code = strings.TrimSpace(code)
	if !IsMeetingCode(code) {
		return result, errors.New("会议号应为 9 位数字")
	}
	if !options.TLS || options.Insecure {
		return result, errors.New("会议目录要求已验证的 TLS 中转连接")
	}
	c, err := dialTransport(ctx, addr, options)
	if err != nil {
		return result, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	hello, _ := json.Marshal(Hello{Role: "meeting-resolve", ID: code})
	if _, err = fmt.Fprintf(c, "YU_RELAY/1 %s\n", hello); err != nil {
		return result, err
	}
	scanner := bufio.NewScanner(c)
	scanner.Buffer(make([]byte, 1024), 16<<10)
	if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &result) != nil {
		return MeetingInfo{}, errors.New("会议目录暂时不可用")
	}
	if result.Error != "" || !result.Active || result.Code != code || len(result.ID) != 24 || strings.Trim(result.ID, "0123456789ABCDEF") != "" || !result.ExpiresAt.After(time.Now()) {
		return MeetingInfo{}, errors.New("会议不存在、已结束或已过期")
	}
	return result, nil
}
