package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/yudesk/yudesk/internal/relay"
)

// Conference is the authenticated, TLS-protected signaling channel used by
// Android's native WebRTC stack. Audio, camera and screen frames never pass
// through this connection.
type Conference struct {
	conn      net.Conn
	reader    *bufio.Reader
	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
	onClose   func()
}

// OpenConference joins an existing room or enters this device's own room as
// its cryptographically authenticated initial host.
func (e *Engine) OpenConference(code, name string, host bool) (*Conference, error) {
	e.conferenceMu.Lock()
	defer e.conferenceMu.Unlock()
	code, name = strings.TrimSpace(code), strings.TrimSpace(name)
	e.mu.Lock()
	if !e.permittedLocked() {
		e.mu.Unlock()
		return nil, errors.New("管理通道未连接或设备未激活")
	}
	if host && (!e.state.Meeting || e.state.MeetingCode != code || !e.state.MeetingUntil.After(time.Now())) {
		e.mu.Unlock()
		return nil, errors.New("本机没有有效的主持会议")
	}
	deviceID, key, old := e.identity.ID, e.identity.PrivateKey, e.conference
	e.mu.Unlock()
	if old != nil {
		old.Close()
	}
	ctx, cancel := context.WithTimeout(e.ctx, 12*time.Second)
	conn, err := relay.DialConference(ctx, relayAddress, relayOptions, deviceID, key, code, name, host)
	cancel()
	if err != nil {
		return nil, err
	}
	c := &Conference{conn: conn, reader: bufio.NewReaderSize(conn, 16<<10), done: make(chan struct{})}
	c.onClose = func() {
		e.mu.Lock()
		if e.conference == c {
			e.conference = nil
		}
		e.mu.Unlock()
	}
	e.mu.Lock()
	if !e.permittedLocked() {
		e.mu.Unlock()
		c.Close()
		return nil, errors.New("设备授权状态已改变")
	}
	e.conference = c
	e.mu.Unlock()
	go c.heartbeat()
	return c, nil
}

// Read blocks until one bounded JSON signaling message is available.
func (c *Conference) Read() (string, error) {
	line, err := readConferenceLine(c.reader)
	if err != nil {
		c.Close()
		return "", err
	}
	var message relay.ConferenceMessage
	if json.Unmarshal(line, &message) != nil || message.Type == "" {
		c.Close()
		return "", errors.New("服务器返回无效会议信令")
	}
	clean, _ := json.Marshal(message)
	return string(clean), nil
}

// Send validates and serializes one signaling message. Calls are safe from the
// UI/signaling threads and the internal heartbeat at the same time.
func (c *Conference) Send(raw string) error {
	if len(raw) == 0 || len(raw) > relay.MaxConferenceMessageSize {
		return errors.New("会议信令大小无效")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var message relay.ConferenceMessage
	if decoder.Decode(&message) != nil || message.Type == "" {
		return errors.New("会议信令格式无效")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("会议信令包含多余数据")
	}
	payload, _ := json.Marshal(message)
	payload = append(payload, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	for len(payload) > 0 {
		n, err := c.conn.Write(payload)
		if err != nil {
			c.Close()
			return err
		}
		if n == 0 {
			c.Close()
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}

func (c *Conference) heartbeat() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if c.Send(`{"type":"ping"}`) != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *Conference) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
}

func readConferenceLine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 4096)
	for len(line) <= relay.MaxConferenceMessageSize {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > relay.MaxConferenceMessageSize {
			return nil, relay.ErrConferenceMessageTooLarge
		}
		line = append(line, fragment...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
	return nil, relay.ErrConferenceMessageTooLarge
}
