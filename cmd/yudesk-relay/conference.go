package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
)

type conferenceRoom struct {
	participants map[string]*conferenceParticipant
	removed      map[string]struct{}
	hostID       string
}

type conferenceParticipant struct {
	id, deviceID, name string
	conn               net.Conn
	send               chan relay.ConferenceMessage
	done               chan struct{}
	closeOnce          sync.Once
	joined             uint64
	joinedAt           int64
	microphone         bool
	camera             bool
	screen             bool
	recording          bool
}

func (p *conferenceParticipant) peer(host string) relay.ConferencePeer {
	return relay.ConferencePeer{ID: p.id, Name: p.name, JoinedAt: p.joinedAt, Host: p.id == host, Microphone: p.microphone, Camera: p.camera, Screen: p.screen, Recording: p.recording}
}

func (p *conferenceParticipant) close() {
	p.closeOnce.Do(func() {
		close(p.done)
		_ = p.conn.Close()
	})
}

func (b *broker) handleConference(c net.Conn, reader *bufio.Reader, hello relay.Hello) {
	encoder := json.NewEncoder(c)
	sendControl := func(message relay.ControlMessage) error {
		_ = c.SetWriteDeadline(time.Now().Add(3 * time.Second))
		return encoder.Encode(message)
	}
	stop := func(code, message string) {
		_ = sendControl(relay.ControlMessage{Type: "stop", Code: code, Message: message})
	}
	hello.ID = strings.ToUpper(strings.TrimSpace(hello.ID))
	hello.Room, hello.Name = strings.TrimSpace(hello.Room), strings.TrimSpace(hello.Name)
	if b.accounts == nil || !b.deviceLicenses || !relay.IsMeetingCode(hello.Room) || !relay.ValidConferenceName(hello.Name) ||
		(hello.Action != "host" && hello.Action != "join") || len(hello.PublicKey) != ed25519.PublicKeySize || secureconn.DeviceID(hello.PublicKey) != hello.ID {
		stop("DENIED", "invalid conference request")
		return
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil || sendControl(relay.ControlMessage{Type: "challenge", Nonce: nonce}) != nil {
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
	line, err := readConferenceLine(reader, 16<<10)
	if err != nil {
		return
	}
	var proof relay.ControlMessage
	if json.Unmarshal(line, &proof) != nil || proof.Type != "proof" ||
		!ed25519.Verify(hello.PublicKey, relay.ConferenceProof(hello.ID, hello.Room, hello.Action, hello.Name, nonce), proof.Signature) {
		stop("DENIED", "conference identity authentication failed")
		return
	}

	now := time.Now()
	b.Lock()
	b.cleanupMeetingsLocked(now)
	room, exists := b.meetings[hello.Room]
	validHost := hello.Action != "host" || room.deviceID == hello.ID
	conferenceReady := hello.Action == "host" || room.conference != nil && room.conference.hostID != ""
	participantCount := 0
	duplicateDevice := false
	removedDevice := false
	if room.conference != nil {
		participantCount = len(room.conference.participants)
		_, removedDevice = room.conference.removed[hello.ID]
		for _, participant := range room.conference.participants {
			if participant.deviceID == hello.ID {
				duplicateDevice = true
			}
		}
	}
	b.Unlock()
	if !exists || !room.expires.After(now) {
		stop("NOT_FOUND", "meeting does not exist or has expired")
		return
	}
	if expiry, licenseErr := b.accounts.DeviceLicenseExpiry(room.deviceID); licenseErr != nil || !expiry.After(now) {
		stop("LICENSE_REQUIRED", "meeting host activation has expired")
		return
	}
	if !validHost {
		stop("DENIED", "only the meeting owner can enter as the initial host")
		return
	}
	if !conferenceReady {
		stop("BUSY", "host has not entered the meeting yet")
		return
	}
	if duplicateDevice {
		stop("BUSY", "this device is already in the meeting")
		return
	}
	if removedDevice {
		stop("DENIED", "this device was removed from the meeting")
		return
	}
	if participantCount >= relay.MaxConferenceParticipants {
		stop("BUSY", "meeting is full")
		return
	}
	if expiry, licenseErr := b.accounts.DeviceLicenseExpiry(hello.ID); licenseErr != nil || !expiry.After(now) {
		stop("LICENSE_REQUIRED", "participant activation has expired")
		return
	}
	_ = c.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte("OK\n")); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err = readConferenceLine(reader, 32)
	if err != nil || strings.TrimSpace(string(line)) != "READY" {
		return
	}
	_ = c.SetDeadline(time.Time{})

	participantID, err := newConferenceParticipantID()
	if err != nil {
		return
	}
	p := &conferenceParticipant{id: participantID, deviceID: hello.ID, name: hello.Name, joinedAt: time.Now().UnixMilli(), conn: c, send: make(chan relay.ConferenceMessage, 64), done: make(chan struct{})}
	b.Lock()
	room, exists = b.meetings[hello.Room]
	if !exists || !room.expires.After(time.Now()) {
		b.Unlock()
		p.close()
		return
	}
	if room.conference == nil {
		if hello.Action != "host" {
			b.Unlock()
			p.close()
			return
		}
		room.conference = &conferenceRoom{participants: make(map[string]*conferenceParticipant), removed: make(map[string]struct{})}
	}
	if len(room.conference.participants) >= relay.MaxConferenceParticipants || (hello.Action == "host" && room.deviceID != hello.ID) || (hello.Action == "join" && room.conference.hostID == "") {
		b.Unlock()
		p.close()
		return
	}
	for _, existing := range room.conference.participants {
		if existing.deviceID == hello.ID {
			b.Unlock()
			p.close()
			return
		}
	}
	if _, removed := room.conference.removed[hello.ID]; removed {
		b.Unlock()
		p.close()
		return
	}
	b.conferenceSeq++
	p.joined = b.conferenceSeq
	peers := make([]relay.ConferencePeer, 0, len(room.conference.participants))
	for _, existing := range room.conference.participants {
		peers = append(peers, existing.peer(room.conference.hostID))
	}
	room.conference.participants[p.id] = p
	if room.conference.hostID == "" {
		room.conference.hostID = p.id
	}
	b.meetings[hello.Room] = room
	hostID := room.conference.hostID
	b.broadcastConferenceLocked(room.conference, relay.ConferenceMessage{Type: "peer-joined", ID: p.id, Name: p.name, JoinedAt: p.joinedAt, Host: hostID}, p.id)
	welcome := relay.ConferenceMessage{Type: "welcome", ID: p.id, Name: p.name, JoinedAt: p.joinedAt, Host: hostID, Peers: peers}
	if b.conferenceTURN != nil {
		welcome.RTC = b.conferenceTURN.policy(hello.Room, p.id, room.expires, b.conferenceSTUN, b.conferenceDirectTimeoutMS)
	} else if b.conferenceSTUN != "" {
		welcome.RTC = &relay.RTCPolicy{ICEServers: []relay.ICEServer{{URLs: []string{b.conferenceSTUN}}}, DirectTimeoutMS: b.conferenceDirectTimeoutMS}
	}
	b.queueConferenceLocked(p, welcome)
	b.Unlock()

	go writeConferenceMessages(p)
	b.readConferenceMessages(hello.Room, p, reader)
	b.leaveConference(hello.Room, p)
}

func readConferenceLine(reader *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, min(limit, 16<<10))
	for len(line) <= limit {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, relay.ErrConferenceMessageTooLarge
		}
		line = append(line, fragment...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
	return nil, relay.ErrConferenceMessageTooLarge
}

func newConferenceParticipantID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func writeConferenceMessages(p *conferenceParticipant) {
	encoder := json.NewEncoder(p.conn)
	for {
		select {
		case message := <-p.send:
			_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if encoder.Encode(message) != nil {
				p.close()
				return
			}
		case <-p.done:
			return
		}
	}
}

func (b *broker) readConferenceMessages(code string, p *conferenceParticipant, reader *bufio.Reader) {
	for {
		_ = p.conn.SetReadDeadline(time.Now().Add(45 * time.Second))
		line, err := readConferenceLine(reader, relay.MaxConferenceMessageSize)
		if err != nil {
			return
		}
		var message relay.ConferenceMessage
		if json.Unmarshal(line, &message) != nil {
			return
		}
		if !b.applyConferenceMessage(code, p, message) {
			return
		}
	}
}

func (b *broker) applyConferenceMessage(code string, p *conferenceParticipant, message relay.ConferenceMessage) bool {
	b.Lock()
	defer b.Unlock()
	room, exists := b.meetings[code]
	if !exists || room.conference == nil || room.conference.participants[p.id] != p {
		return false
	}
	conference := room.conference
	switch message.Type {
	case "ping":
		b.queueConferenceLocked(p, relay.ConferenceMessage{Type: "pong"})
	case "signal":
		validSignal := message.Signal == "offer" || message.Signal == "answer" || message.Signal == "candidate"
		if !validSignal || len(message.SDP) > 32<<10 || len(message.Candidate) > 8<<10 ||
			(message.Signal != "candidate" && message.SDP == "") || (message.Signal == "candidate" && message.Candidate == "") {
			return false
		}
		target := conference.participants[message.To]
		if target == nil {
			// ICE candidates and renegotiation can already be in flight when a
			// peer leaves. Dropping that stale message keeps the sender in the
			// meeting; it is not a protocol violation by the remaining peer.
			return true
		}
		if target == p {
			return false
		}
		b.queueConferenceLocked(target, relay.ConferenceMessage{Type: "signal", From: p.id, Signal: message.Signal, SDP: message.SDP, Candidate: message.Candidate})
	case "state":
		p.microphone, p.camera = message.Microphone, message.Camera
		if conference.hostID == p.id {
			p.screen, p.recording = message.Screen, message.Recording
		} else {
			p.screen, p.recording = false, false
		}
		b.broadcastConferenceLocked(conference, relay.ConferenceMessage{Type: "state", ID: p.id, Microphone: p.microphone, Camera: p.camera, Screen: p.screen, Recording: p.recording}, "")
	case "transfer":
		if conference.hostID != p.id || conference.participants[message.To] == nil || message.To == p.id {
			return false
		}
		p.screen, p.recording = false, false
		b.broadcastConferenceLocked(conference, relay.ConferenceMessage{Type: "state", ID: p.id, Microphone: p.microphone, Camera: p.camera}, "")
		conference.hostID = message.To
		b.broadcastConferenceLocked(conference, relay.ConferenceMessage{Type: "host", Host: conference.hostID}, "")
	case "kick":
		if conference.hostID != p.id || message.To == p.id {
			return false
		}
		target := conference.participants[message.To]
		if target == nil {
			return true
		}
		if conference.removed == nil {
			conference.removed = make(map[string]struct{})
		}
		conference.removed[target.deviceID] = struct{}{}
		b.queueConferenceLocked(target, relay.ConferenceMessage{Type: "removed", Message: "你已被主持人移出本次会议"})
		// Let the participant's single writer flush the styled reason before the
		// connection is closed. leaveConference then broadcasts peer-left.
		time.AfterFunc(300*time.Millisecond, target.close)
	case "end":
		if conference.hostID != p.id {
			return false
		}
		b.removeMeetingCodeLocked(code, "主持人已结束会议")
		return false
	default:
		return false
	}
	return true
}

func (b *broker) leaveConference(code string, p *conferenceParticipant) {
	b.Lock()
	room, exists := b.meetings[code]
	if !exists || room.conference == nil || room.conference.participants[p.id] != p {
		b.Unlock()
		// End/expiry queues a final styled reason before removing the room.
		// Give the single writer a bounded chance to flush it.
		time.AfterFunc(time.Second, p.close)
		return
	}
	conference := room.conference
	delete(conference.participants, p.id)
	b.broadcastConferenceLocked(conference, relay.ConferenceMessage{Type: "peer-left", ID: p.id}, "")
	if conference.hostID == p.id {
		conference.hostID = ""
		var next *conferenceParticipant
		for _, candidate := range conference.participants {
			if next == nil || candidate.joined < next.joined {
				next = candidate
			}
		}
		if next != nil {
			conference.hostID = next.id
			b.broadcastConferenceLocked(conference, relay.ConferenceMessage{Type: "host", Host: next.id}, "")
		}
	}
	b.Unlock()
	p.close()
}

func (b *broker) queueConferenceLocked(p *conferenceParticipant, message relay.ConferenceMessage) {
	select {
	case <-p.done:
		return
	default:
	}
	select {
	case p.send <- message:
	default:
		p.close()
	}
}

func (b *broker) broadcastConferenceLocked(room *conferenceRoom, message relay.ConferenceMessage, except string) {
	if room == nil {
		return
	}
	for id, participant := range room.participants {
		if id != except {
			b.queueConferenceLocked(participant, message)
		}
	}
}

func (b *broker) closeConferenceLocked(room *conferenceRoom, reason string) {
	if room == nil {
		return
	}
	for _, participant := range room.participants {
		b.queueConferenceLocked(participant, relay.ConferenceMessage{Type: "ended", Message: reason})
		time.AfterFunc(time.Second, participant.close)
	}
	room.participants = nil
	room.hostID = ""
}

func (b *broker) removeMeetingCodeLocked(code, reason string) {
	room, exists := b.meetings[code]
	if !exists {
		return
	}
	b.closeConferenceLocked(room.conference, reason)
	delete(b.meetings, code)
	if b.meetingDevices[room.deviceID] == code {
		delete(b.meetingDevices, room.deviceID)
	}
}
