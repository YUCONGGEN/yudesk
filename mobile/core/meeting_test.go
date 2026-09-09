package core

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/peerpath"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func meetingAuth(code, mode string) protocol.Message {
	params, _ := json.Marshal(map[string]any{"pin": code, "mode": mode, "meeting": true})
	return protocol.Message{Kind: "request", ID: "meeting-auth", Method: "auth", Params: params}
}

func TestMeetingLifecycleUsesSeparateTemporaryCredential(t *testing.T) {
	e := testEngine(t)
	var closed atomic.Int32
	e.meetingOpen = func(context.Context) (relay.MeetingInfo, error) {
		return relay.MeetingInfo{ID: e.identity.ID, Code: "654321987", Active: true, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	e.meetingClose = func(context.Context) error { closed.Add(1); return nil }

	code, err := e.StartMeeting()
	if err != nil || code != "654321987" {
		t.Fatalf("start meeting: code=%q err=%v", code, err)
	}
	var state status
	if err = json.Unmarshal([]byte(e.StatusJSON()), &state); err != nil || !state.Meeting || state.MeetingCode != code || !state.MeetingUntil.After(time.Now()) {
		t.Fatalf("invalid meeting state: %+v err=%v", state, err)
	}
	e.mu.Lock()
	permanentAllowed := e.meetingAllowedLocked(e.identity.PIN)
	temporaryAllowed := e.meetingAllowedLocked(code)
	e.mu.Unlock()
	if permanentAllowed || !temporaryAllowed {
		t.Fatal("meeting reused the permanent PIN or rejected its temporary credential")
	}
	if err = e.EndMeeting(); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 1 {
		t.Fatalf("meeting directory close count=%d", closed.Load())
	}
	state = status{}
	if err = json.Unmarshal([]byte(e.StatusJSON()), &state); err != nil || state.Meeting || state.MeetingCode != "" {
		t.Fatalf("meeting remained active: %+v err=%v", state, err)
	}
}

func TestMeetingJoinNeedsNoApprovalAndIsViewOnly(t *testing.T) {
	e := testEngine(t)
	e.mu.Lock()
	e.state.Meeting = true
	e.state.MeetingCode = "654321987"
	e.state.MeetingUntil = time.Now().Add(time.Minute)
	e.mu.Unlock()

	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { e.serveAgent(e.ctx, a); close(done) }()
	secured, err := secureconn.Connect(b, e.identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSessionWithMode(e.ctx, secured, "654321987", true, true, peerpath.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if e.approvals.Pending() != nil {
		t.Fatal("meeting unexpectedly requested host confirmation")
	}
	var session map[string]any
	if err = json.Unmarshal([]byte(s.StatusJSON()), &session); err != nil || session["meeting"] != true || session["control"] != false {
		t.Fatalf("meeting session was not view-only: %s err=%v", s.StatusJSON(), err)
	}
	if err = s.SendInputJSON(`{"events":[{"type":"down","x":1,"y":1,"button":1}],"width":10,"height":10}`); err == nil {
		t.Fatal("meeting participant could send control input")
	}
	s.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("meeting agent did not close")
	}
}

func TestMeetingRejectsControlAndExpiredCode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  string
		until time.Time
	}{
		{name: "control", mode: "control", until: time.Now().Add(time.Minute)},
		{name: "expired", mode: "view", until: time.Now().Add(-time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := testEngine(t)
			e.mu.Lock()
			e.state.Meeting = true
			e.state.MeetingCode = "654321987"
			e.state.MeetingUntil = tc.until
			e.mu.Unlock()
			c, _ := rawPeer(t, e)
			if err := c.WriteMessage(meetingAuth("654321987", tc.mode)); err != nil {
				t.Fatal(err)
			}
			response, err := c.ReadMessage()
			if err != nil || response.OK {
				t.Fatalf("unsafe meeting accepted: %+v err=%v", response, err)
			}
		})
	}
}

func TestMeetingStartCanBeCanceled(t *testing.T) {
	e := testEngine(t)
	started := make(chan struct{})
	e.meetingOpen = func(ctx context.Context) (relay.MeetingInfo, error) {
		close(started)
		<-ctx.Done()
		return relay.MeetingInfo{}, ctx.Err()
	}
	e.meetingClose = func(context.Context) error { return nil }
	result := make(chan error, 1)
	go func() { _, err := e.StartMeeting(); result <- err }()
	<-started
	e.CancelMeetingStart()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled meeting start succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled meeting start remained blocked")
	}
}

func TestStoppingScreenShareInvalidatesMeeting(t *testing.T) {
	e := testEngine(t)
	closed := make(chan struct{}, 1)
	e.meetingOpen = func(context.Context) (relay.MeetingInfo, error) {
		return relay.MeetingInfo{ID: e.identity.ID, Code: "654321987", Active: true, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	e.meetingClose = func(context.Context) error { closed <- struct{}{}; return nil }
	if _, err := e.StartMeeting(); err != nil {
		t.Fatal(err)
	}
	e.SetSharing(false)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("meeting directory was not closed with screen sharing")
	}
	var state status
	if err := json.Unmarshal([]byte(e.StatusJSON()), &state); err != nil || state.Meeting || state.Sharing {
		t.Fatalf("screen sharing stop left meeting active: %+v err=%v", state, err)
	}
}
