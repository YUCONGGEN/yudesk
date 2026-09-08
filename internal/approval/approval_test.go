package approval

import (
	"context"
	"errors"
	"testing"
	"time"
)

func waitPending(t *testing.T, b *Broker) *Pending {
	t.Helper()
	select {
	case <-b.Events():
	case <-time.After(time.Second):
		t.Fatal("no request")
	}
	p := b.Pending()
	if p == nil {
		t.Fatal("missing pending")
	}
	return p
}
func TestApproveDenyAndSingleUse(t *testing.T) {
	for _, accept := range []bool{true, false} {
		b := New()
		done := make(chan error, 1)
		go func() { done <- b.Request(context.Background(), "control") }()
		p := waitPending(t, b)
		if time.Until(p.Deadline) > 60*time.Second || len(p.ID) != 32 {
			t.Fatal("invalid deadline/id")
		}
		if err := b.Resolve("stale", true); err == nil {
			t.Fatal("accepted stale id")
		}
		if err := b.Request(context.Background(), "view"); !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
		if err := b.Resolve(p.ID, accept); err != nil {
			t.Fatal(err)
		}
		err := <-done
		if accept && err != nil || !accept && !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
		if b.Pending() != nil {
			t.Fatal("request retained")
		}
		if b.Resolve(p.ID, true) == nil {
			t.Fatal("replayed approval")
		}
	}
}
func TestTimeoutAndCancelDeny(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		b := New()
		b.timeout = 50 * time.Millisecond
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		done := make(chan error, 1)
		go func() { done <- b.Request(ctx, "view") }()
		p := waitPending(t, b)
		if cancel {
			stop()
		}
		err := <-done
		if err == nil {
			t.Fatal("granted without approval")
		}
		if b.Resolve(p.ID, true) == nil {
			t.Fatal("late approval")
		}
	}
}
func TestRequestRateBound(t *testing.T) {
	b := New()
	for i := 0; i < 3; i++ {
		done := make(chan error, 1)
		go func() { done <- b.Request(context.Background(), "view") }()
		waitPending(t, b)
		b.Cancel()
		<-done
		select {
		case <-b.Events():
		default:
		}
	}
	if b.Request(context.Background(), "view") == nil {
		t.Fatal("rate limit missing")
	}
}
