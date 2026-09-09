package agentapp

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/approval"
	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func consentAgent(t *testing.T) (*agent, *protocol.Conn, <-chan struct{}) {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	a := &agent{id: secureconn.DeviceID(pub), privateKey: key, pin: "123456", allowControl: true, quit: make(chan struct{}), approvals: approval.New(), inputFactory: func() *inputSession {
		s := newInputSession()
		s.apply = func([]desktop.InputEvent) error { return nil }
		return s
	}}
	x, y := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); a.handle(x) }()
	wire, err := secureconn.Connect(y, a.id)
	if err != nil {
		t.Fatal(err)
	}
	c := protocol.NewConn(wire)
	t.Cleanup(func() {
		y.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("session worker leaked")
		}
	})
	return a, c, done
}
func TestPINLessConsentGrantsOnlyRequestedMode(t *testing.T) {
	for _, mode := range []string{"control", "view"} {
		t.Run(mode, func(t *testing.T) {
			a, c, _ := consentAgent(t)
			c.WriteMessage(protocol.Message{Kind: "request", ID: "1", Method: "auth", Params: []byte(`{"pin":"","mode":"` + mode + `"}`)})
			select {
			case <-a.approvals.Events():
			case <-time.After(time.Second):
				t.Fatal("no consent")
			}
			p := a.approvals.Pending()
			if p == nil || p.Mode != mode {
				t.Fatal(p)
			}
			response := make(chan protocol.Message, 1)
			go func() { m, _ := c.ReadMessage(); response <- m }()
			select {
			case <-response:
				t.Fatal("granted before local consent")
			case <-time.After(25 * time.Millisecond):
			}
			if err := a.approvals.Resolve(p.ID, true); err != nil {
				t.Fatal(err)
			}
			m := <-response
			if !m.OK || m.Meta["control"] != (mode == "control") {
				t.Fatalf("%+v", m)
			}
			// The cancellation watcher must preserve the first post-auth request.
			c.WriteMessage(protocol.Message{Kind: "request", ID: "2", Method: "info"})
			m, err := c.ReadMessage()
			if err != nil || !m.OK || m.ID != "2" {
				t.Fatal(m, err)
			}
		})
	}
}
func TestConsentRejectDisconnectAndWrongPIN(t *testing.T) {
	for _, kind := range []string{"reject", "disconnect", "wrong-pin"} {
		t.Run(kind, func(t *testing.T) {
			a, c, done := consentAgent(t)
			pin := ""
			if kind == "wrong-pin" {
				pin = "999999"
			}
			c.WriteMessage(protocol.Message{Kind: "request", ID: "1", Method: "auth", Params: []byte(`{"pin":"` + pin + `","mode":"control"}`)})
			if kind == "wrong-pin" {
				m, err := c.ReadMessage()
				if err != nil || m.OK || a.approvals.Pending() != nil {
					t.Fatal(m, err)
				}
				return
			}
			select {
			case <-a.approvals.Events():
			case <-time.After(time.Second):
				t.Fatal("no prompt")
			}
			p := a.approvals.Pending()
			if p == nil {
				t.Fatal("no pending")
			}
			if kind == "disconnect" {
				c.Close()
			} else {
				a.approvals.Resolve(p.ID, false)
				m, err := c.ReadMessage()
				if err != nil || m.OK {
					t.Fatal(m, err)
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancelled auth remains alive")
			}
			if a.approvals.Pending() != nil || a.approvals.Resolve(p.ID, true) == nil {
				t.Fatal("cancelled request still approvable")
			}
		})
	}
}

func TestMeetingJoinsWithoutConsentAndIsViewOnly(t *testing.T) {
	a, c, _ := consentAgent(t)
	a.statusMu.Lock()
	a.meetingPIN = "654321987"
	a.meetingUntil = time.Now().Add(time.Minute)
	a.statusMu.Unlock()
	if err := c.WriteMessage(protocol.Message{Kind: "request", ID: "1", Method: "auth", Params: []byte(`{"pin":"654321987","mode":"view","meeting":true}`)}); err != nil {
		t.Fatal(err)
	}
	m, err := c.ReadMessage()
	if err != nil || !m.OK || m.Meta["control"] != false || a.approvals.Pending() != nil {
		t.Fatalf("meeting was not view-only: %+v", m)
	}
}

func TestMeetingRejectsControlAndExpiredInvitation(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		expires    time.Time
	}{
		{name: "control", mode: "control", expires: time.Now().Add(time.Minute)},
		{name: "expired", mode: "view", expires: time.Now().Add(-time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, c, _ := consentAgent(t)
			a.statusMu.Lock()
			a.meetingPIN = "654321987"
			a.meetingUntil = tc.expires
			a.statusMu.Unlock()
			if err := c.WriteMessage(protocol.Message{Kind: "request", ID: "1", Method: "auth", Params: []byte(`{"pin":"654321987","mode":"` + tc.mode + `","meeting":true}`)}); err != nil {
				t.Fatal(err)
			}
			m, err := c.ReadMessage()
			if err != nil || m.OK || a.approvals.Pending() != nil {
				t.Fatalf("unsafe meeting request accepted: message=%+v err=%v", m, err)
			}
		})
	}
}
