package viewerapp

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yudesk/yudesk/internal/relay"
)

// conferenceJS is appended to the dashboard bundle from this meeting-specific
// file so conference fixes stay isolated from the rest of the desktop UI.
//
//go:embed ui/conference.js
var conferenceJS string

func init() {
	dashboardJS += "\n" + conferenceJS
}

func (d *unifiedDesk) serveConference(w http.ResponseWriter, r *http.Request) {
	code, name := strings.TrimSpace(r.URL.Query().Get("code")), strings.TrimSpace(r.URL.Query().Get("name"))
	host := r.URL.Query().Get("host") == "1"
	if !relay.IsMeetingCode(code) || !relay.ValidConferenceName(name) {
		http.Error(w, "会议号应为 9 位数字，姓名为 1 到 32 个字符", http.StatusBadRequest)
		return
	}
	ctx, cancelDial := context.WithTimeout(d.host.ctx, 10*time.Second)
	remote, err := d.device.DialConference(ctx, code, name, host)
	cancelDial()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	upgrader := websocket.Upgrader{HandshakeTimeout: 5 * time.Second, CheckOrigin: func(request *http.Request) bool {
		origin, parseErr := url.Parse(request.Header.Get("Origin"))
		return parseErr == nil && origin.Scheme == "http" && origin.Host == request.Host && origin.Path == "" && origin.RawQuery == "" && origin.Fragment == ""
	}}
	local, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = remote.Close()
		return
	}
	defer local.Close()
	defer remote.Close()
	local.SetReadLimit(relay.MaxConferenceMessageSize)
	stop := context.AfterFunc(d.host.ctx, func() {
		_ = remote.Close()
		_ = local.Close()
	})
	defer stop()

	remoteDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(remote)
		scanner.Buffer(make([]byte, 4096), relay.MaxConferenceMessageSize)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			var message relay.ConferenceMessage
			if json.Unmarshal(line, &message) != nil || message.Type == "" {
				remoteDone <- errors.New("服务器返回无效会议信令")
				_ = local.Close()
				return
			}
			_ = local.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := local.WriteMessage(websocket.TextMessage, line); err != nil {
				remoteDone <- err
				_ = remote.Close()
				return
			}
		}
		remoteDone <- scanner.Err()
		_ = local.Close()
	}()

	for {
		messageType, payload, readErr := local.ReadMessage()
		if readErr != nil {
			break
		}
		if messageType != websocket.TextMessage || len(payload) == 0 || len(payload) > relay.MaxConferenceMessageSize || !json.Valid(payload) {
			break
		}
		payload = append(payload, '\n')
		_ = remote.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := writeConferencePayload(remote, payload); err != nil {
			break
		}
	}
	_ = remote.Close()
	select {
	case <-remoteDone:
	case <-time.After(500 * time.Millisecond):
	}
}

func writeConferencePayload(w io.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := w.Write(payload)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}
