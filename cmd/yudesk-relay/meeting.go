package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
)

type meetingRoom struct {
	deviceID   string
	expires    time.Time
	conference *conferenceRoom
}

func (b *broker) handleMeeting(c net.Conn, reader *bufio.Reader, hello relay.Hello) {
	encoder := json.NewEncoder(c)
	send := func(message relay.ControlMessage) error {
		_ = c.SetWriteDeadline(time.Now().Add(3 * time.Second))
		return encoder.Encode(message)
	}
	stop := func(code, message string) { _ = send(relay.ControlMessage{Type: "stop", Code: code, Message: message}) }
	if !b.deviceLicenses || b.accounts == nil {
		stop("DENIED", "meeting directory is unavailable")
		return
	}
	hello.ID = strings.ToUpper(strings.TrimSpace(hello.ID))
	if (hello.Action != "open" && hello.Action != "close") || len(hello.PublicKey) != ed25519.PublicKeySize || secureconn.DeviceID(hello.PublicKey) != hello.ID {
		stop("DENIED", "invalid meeting request")
		return
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil || send(relay.ControlMessage{Type: "challenge", Nonce: nonce}) != nil {
		return
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 16<<10)
	_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
	if !scanner.Scan() {
		return
	}
	var proof relay.ControlMessage
	if json.Unmarshal(scanner.Bytes(), &proof) != nil || proof.Type != "proof" || !ed25519.Verify(hello.PublicKey, relay.MeetingProof(hello.ID, hello.Action, nonce), proof.Signature) {
		stop("DENIED", "device identity authentication failed")
		return
	}
	if err := b.accounts.RegisterLicensedDeviceNamed(hello.ID, hello.PublicKey, ""); err != nil {
		stop("DISABLED", err.Error())
		return
	}
	licenseExpiry, err := b.accounts.DeviceLicenseExpiry(hello.ID)
	if err != nil || !licenseExpiry.After(time.Now()) {
		stop("LICENSE_REQUIRED", "device activation is required or has expired")
		return
	}
	if hello.Action == "close" {
		b.Lock()
		b.removeMeetingLocked(hello.ID)
		b.Unlock()
		b.accounts.Audit(account.AuditEntry{Action: "meeting_close", DeviceID: hello.ID, RemoteAddr: c.RemoteAddr().String()})
		_ = send(relay.ControlMessage{Type: "meeting", Active: false})
		return
	}
	deviceCode, _ := b.accounts.EnsureDeviceCode(hello.ID)
	now := time.Now()
	expires := now.Add(2 * time.Hour)
	if licenseExpiry.Before(expires) {
		expires = licenseExpiry
	}
	b.Lock()
	control := b.controls[hello.ID]
	_, connected := b.active[hello.ID]
	if control == nil || control.stopping || now.Sub(time.Unix(control.lastSeen.Load(), 0)) > 15*time.Second || connected {
		b.Unlock()
		stop("BUSY", "device is offline or already connected")
		return
	}
	b.cleanupMeetingsLocked(now)
	b.removeMeetingLocked(hello.ID)
	if b.meetings == nil {
		b.meetings = make(map[string]meetingRoom)
	}
	if b.meetingDevices == nil {
		b.meetingDevices = make(map[string]string)
	}
	code := ""
	for attempts := 0; attempts < 64; attempts++ {
		value, randomErr := rand.Int(rand.Reader, big.NewInt(900000000))
		if randomErr != nil {
			break
		}
		candidate := big.NewInt(0).Add(value, big.NewInt(100000000)).String()
		if _, exists := b.meetings[candidate]; !exists && candidate != deviceCode {
			code = candidate
			break
		}
	}
	if code != "" {
		b.meetings[code] = meetingRoom{deviceID: hello.ID, expires: expires}
		b.meetingDevices[hello.ID] = code
	}
	b.Unlock()
	if code == "" {
		stop("DENIED", "cannot allocate meeting code")
		return
	}
	b.accounts.Audit(account.AuditEntry{Action: "meeting_open", DeviceID: hello.ID, RemoteAddr: c.RemoteAddr().String()})
	_ = send(relay.ControlMessage{Type: "meeting", Active: true, ActiveUntil: expires, Code: code})
}

func (b *broker) handleMeetingResolve(c net.Conn, code string) {
	now := time.Now()
	result := relay.MeetingInfo{Code: code, Error: "meeting unavailable"}
	if !relay.IsMeetingCode(code) || !b.permitLookup(c.RemoteAddr().String(), 12) {
		_ = json.NewEncoder(c).Encode(result)
		return
	}
	b.Lock()
	b.cleanupMeetingsLocked(now)
	room, exists := b.meetings[code]
	control := b.controls[room.deviceID]
	_, connected := b.active[room.deviceID]
	available := exists && control != nil && !control.stopping && now.Sub(time.Unix(control.lastSeen.Load(), 0)) <= 15*time.Second && !connected
	b.Unlock()
	if available {
		if expiry, err := b.accounts.DeviceLicenseExpiry(room.deviceID); err == nil && expiry.After(now) {
			result.ID = room.deviceID
			result.Active = true
			result.ExpiresAt = room.expires
			result.Error = ""
		}
	}
	_ = json.NewEncoder(c).Encode(result)
}

func (b *broker) cleanupMeetingsLocked(now time.Time) {
	for code, room := range b.meetings {
		if !room.expires.After(now) {
			b.closeConferenceLocked(room.conference, "会议已到期")
			delete(b.meetings, code)
			if b.meetingDevices[room.deviceID] == code {
				delete(b.meetingDevices, room.deviceID)
			}
		}
	}
}

func (b *broker) removeMeetingLocked(deviceID string) {
	if code := b.meetingDevices[deviceID]; code != "" {
		if room, ok := b.meetings[code]; ok {
			b.closeConferenceLocked(room.conference, "主持人已结束会议")
		}
		delete(b.meetingDevices, deviceID)
		delete(b.meetings, code)
	}
}
