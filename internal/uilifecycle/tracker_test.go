package uilifecycle

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

type flushWriter struct {
	*httptest.ResponseRecorder
	started chan struct{}
	once    bool
}

func (w *flushWriter) Flush() {
	if !w.once {
		w.once = true
		close(w.started)
	}
}

func attach(t *testing.T, tracker *Tracker) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest("GET", "/api/ui/watch", nil).WithContext(ctx)
	started := make(chan struct{})
	writer := &flushWriter{ResponseRecorder: httptest.NewRecorder(), started: started}
	go tracker.ServeHTTP(writer, request)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("watcher did not attach")
	}
	return cancel
}

func TestTrackerClosesAfterLastPage(t *testing.T) {
	tracker := New(20 * time.Millisecond)
	cancel := attach(t, tracker)
	cancel()
	select {
	case <-tracker.Done():
	case <-time.After(time.Second):
		t.Fatal("tracker did not close")
	}
}

func TestTrackerAllowsReloadAndDoesNotArmHeadless(t *testing.T) {
	tracker := New(80 * time.Millisecond)
	select {
	case <-tracker.Done():
		t.Fatal("headless tracker armed itself")
	case <-time.After(20 * time.Millisecond):
	}
	first := attach(t, tracker)
	first()
	time.Sleep(20 * time.Millisecond)
	second := attach(t, tracker)
	time.Sleep(90 * time.Millisecond)
	select {
	case <-tracker.Done():
		t.Fatal("reload closed tracker")
	default:
	}
	second()
	select {
	case <-tracker.Done():
	case <-time.After(time.Second):
		t.Fatal("tracker did not close after reloaded page")
	}
}

func TestTrackerSuspendKeepsProcessHiddenUntilReopened(t *testing.T) {
	tracker := New(30 * time.Millisecond)
	first := attach(t, tracker)
	tracker.Suspend()
	first()
	time.Sleep(80 * time.Millisecond)
	select {
	case <-tracker.Done():
		t.Fatal("intentional hide closed tracker")
	default:
	}
	second := attach(t, tracker)
	second()
	select {
	case <-tracker.Done():
	case <-time.After(time.Second):
		t.Fatal("tracker did not re-arm after hidden page was reopened")
	}
}

func TestTrackerReusableWindowClose(t *testing.T) {
	events := make(chan struct{}, 2)
	tracker := NewWithCallback(20*time.Millisecond, func() { events <- struct{}{} })
	for i := 0; i < 2; i++ {
		cancel := attach(t, tracker)
		cancel()
		select {
		case <-events:
		case <-time.After(time.Second):
			t.Fatal("missing repeatable close")
		}
	}
	select {
	case <-tracker.Done():
		t.Fatal("reusable close killed tracker")
	default:
	}
}
