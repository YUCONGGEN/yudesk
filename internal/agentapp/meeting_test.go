package agentapp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/approval"
	"github.com/yudesk/yudesk/internal/identity"
	"github.com/yudesk/yudesk/internal/relay"
)

func TestTemporaryMeetingInvitationLifecycle(t *testing.T) {
	directory := t.TempDir()
	deviceIdentity, err := identity.Load(directory, "123456")
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{
		id:               deviceIdentity.ID,
		pin:              deviceIdentity.PIN,
		privateKey:       deviceIdentity.PrivateKey,
		deviceCode:       "123456789",
		managementOnline: true,
		activeUntil:      time.Now().Add(time.Hour),
		approvals:        approval.New(),
		quit:             make(chan struct{}),
	}
	d := &Device{a: a, identityDir: directory}
	var closed atomic.Int32
	d.meetingOpen = func(context.Context) (relay.MeetingInfo, error) {
		return relay.MeetingInfo{ID: a.id, Code: "654321987", Topic: "项目周会", Active: true, ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	d.meetingEnd = func(context.Context) error { closed.Add(1); return nil }
	d.receiving.Store(true)

	invitation, err := d.StartMeeting(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	status := d.Status()
	if invitation != "654321987" || !status.Meeting || status.MeetingCode != invitation || status.MeetingTopic != "项目周会" || !status.MeetingUntil.After(time.Now()) {
		t.Fatalf("invalid meeting state: invitation=%q status=%+v", invitation, status)
	}
	if invitation == deviceIdentity.PIN {
		t.Fatal("meeting secret reused the permanent pairing PIN")
	}
	if !a.meetingAllowed(invitation) || a.meetingAllowed(deviceIdentity.PIN) {
		t.Fatal("meeting authentication did not isolate its temporary secret")
	}

	d.EndMeeting()
	if status = d.Status(); status.Meeting || status.MeetingCode != "" || a.meetingAllowed(invitation) || closed.Load() != 1 {
		t.Fatalf("ended meeting remained usable: %+v", status)
	}
}

func TestTemporaryMeetingInvitationExpires(t *testing.T) {
	directory := t.TempDir()
	deviceIdentity, err := identity.Load(directory, "123456")
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{id: deviceIdentity.ID, pin: deviceIdentity.PIN, privateKey: deviceIdentity.PrivateKey, deviceCode: "987654321", managementOnline: true, activeUntil: time.Now().Add(time.Hour), approvals: approval.New(), quit: make(chan struct{})}
	d := &Device{a: a, identityDir: directory}
	var closed atomic.Int32
	d.meetingOpen = func(context.Context) (relay.MeetingInfo, error) {
		return relay.MeetingInfo{ID: a.id, Code: "123456789", Active: true, ExpiresAt: time.Now().Add(25 * time.Millisecond)}, nil
	}
	d.meetingEnd = func(context.Context) error { closed.Add(1); return nil }
	d.receiving.Store(true)
	invitation, err := d.StartMeeting(25 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for d.Status().Meeting && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if d.Status().Meeting || a.meetingAllowed(invitation) || closed.Load() != 1 {
		t.Fatal("expired meeting invitation remained active")
	}
}
