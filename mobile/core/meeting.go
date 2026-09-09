package core

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/yudesk/yudesk/internal/relay"
)

// StartMeeting creates a short-lived, signed relay-directory entry. Android
// screen capture must already be active because only MediaProjection can grant
// that permission and Go must never bypass the system consent UI.
func (e *Engine) StartMeeting() (string, error) {
	e.meetingMu.Lock()
	defer e.meetingMu.Unlock()

	e.mu.Lock()
	if e.state.Meeting && relay.IsMeetingCode(e.state.MeetingCode) && e.state.MeetingUntil.After(time.Now()) {
		code := e.state.MeetingCode
		e.mu.Unlock()
		return code, nil
	}
	if !e.permittedLocked() {
		e.mu.Unlock()
		return "", errors.New("管理通道未连接或设备未激活")
	}
	if !e.state.Sharing {
		e.mu.Unlock()
		return "", errors.New("请先授权共享屏幕")
	}
	if e.state.Connected || e.controller != nil {
		e.mu.Unlock()
		return "", errors.New("当前已有远程连接，请结束后再发起会议")
	}
	deviceID, key := e.identity.ID, e.identity.PrivateKey
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	e.mu.Lock()
	e.meetingRequestCancel = cancel
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		e.meetingRequestCancel = nil
		e.mu.Unlock()
	}()
	var info relay.MeetingInfo
	var err error
	if e.meetingOpen != nil {
		info, err = e.meetingOpen(ctx)
	} else {
		info, err = relay.OpenMeeting(ctx, relayAddress, relayOptions, deviceID, key)
	}
	if err != nil {
		_ = e.closeMeetingDirectory()
		return "", err
	}
	if !info.Active || !relay.IsMeetingCode(info.Code) || !info.ExpiresAt.After(time.Now()) {
		_ = e.closeMeetingDirectory()
		return "", errors.New("服务器未能创建有效会议")
	}

	e.mu.Lock()
	if ctx.Err() != nil || !e.permittedLocked() || !e.state.Sharing || e.state.Connected || e.controller != nil {
		e.mu.Unlock()
		_ = e.closeMeetingDirectory()
		return "", errors.New("设备状态已改变，请稍后重新发起会议")
	}
	e.meetingGeneration++
	generation := e.meetingGeneration
	e.state.Meeting = true
	e.state.MeetingCode = info.Code
	e.state.MeetingUntil = info.ExpiresAt
	e.notifyLocked()
	e.mu.Unlock()

	time.AfterFunc(time.Until(info.ExpiresAt), func() { e.expireMeeting(generation) })
	return info.Code, nil
}

func (e *Engine) CancelMeetingStart() {
	e.mu.Lock()
	if e.meetingRequestCancel != nil {
		e.meetingRequestCancel()
	}
	e.mu.Unlock()
}

// EndMeeting invalidates the local E2E credential before attempting directory
// cleanup. A directory outage therefore cannot leave a usable meeting behind.
func (e *Engine) EndMeeting() error {
	e.meetingMu.Lock()
	defer e.meetingMu.Unlock()
	e.mu.Lock()
	wasOpen := e.clearMeetingLocked(0, true)
	e.notifyLocked()
	e.mu.Unlock()
	if !wasOpen {
		return nil
	}
	return e.closeMeetingDirectory()
}

func (e *Engine) expireMeeting(generation uint64) {
	e.meetingMu.Lock()
	e.mu.Lock()
	wasOpen := e.clearMeetingLocked(generation, true)
	if wasOpen {
		e.notifyLocked()
	}
	e.mu.Unlock()
	if wasOpen {
		_ = e.closeMeetingDirectory()
	}
	e.meetingMu.Unlock()
}

// A zero generation is an explicit stop. Caller holds e.mu.
func (e *Engine) clearMeetingLocked(generation uint64, disconnect bool) bool {
	if generation != 0 && (generation != e.meetingGeneration || e.state.MeetingUntil.After(time.Now())) {
		return false
	}
	wasOpen := e.state.Meeting || e.state.MeetingCode != "" || !e.state.MeetingUntil.IsZero()
	if !wasOpen {
		return false
	}
	e.meetingGeneration++
	e.state.Meeting = false
	e.state.MeetingCode = ""
	e.state.MeetingUntil = time.Time{}
	if disconnect && e.meetingSession {
		e.stopAgentLocked()
		e.reconcileLocked()
	}
	return true
}

func (e *Engine) closeMeetingDirectory() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e.meetingClose != nil {
		return e.meetingClose(ctx)
	}
	return relay.CloseMeeting(ctx, relayAddress, relayOptions, e.identity.ID, e.identity.PrivateKey)
}

// Directory open/close calls are serialized so a delayed cleanup from a prior
// screen-share session cannot remove a newly-created meeting for this device.
func (e *Engine) queueMeetingDirectoryClose() {
	go func() {
		e.meetingMu.Lock()
		defer e.meetingMu.Unlock()
		_ = e.closeMeetingDirectory()
	}()
}

// Caller holds e.mu. The temporary meeting code is separate from the permanent
// device PIN and is rechecked inside the end-to-end encrypted channel.
func (e *Engine) meetingAllowedLocked(code string) bool {
	return e.state.Meeting && e.state.MeetingUntil.After(time.Now()) &&
		len(code) == len(e.state.MeetingCode) &&
		subtle.ConstantTimeCompare([]byte(code), []byte(e.state.MeetingCode)) == 1
}
