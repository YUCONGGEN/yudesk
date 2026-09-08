package agentapp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/yudesk/yudesk/internal/desktop"
)

type inputSession struct {
	mu             sync.Mutex
	geometryValue  atomic.Uint64
	x, y           int
	buttons        map[int]bool
	keys           map[string]desktop.InputEvent
	releasePending bool
	apply          func([]desktop.InputEvent) error
	wake           chan struct{}
}

func newInputSession() *inputSession {
	return &inputSession{buttons: map[int]bool{}, keys: map[string]desktop.InputEvent{}, apply: desktop.ApplyInput, wake: make(chan struct{}, 1)}
}

// Capture must never wait for a slow input driver or privileged desktop IPC.
// Publish the pair atomically so resize cannot mix dimensions from two frames.
func (s *inputSession) geometry(w, h int) {
	if w > 0 && h > 0 {
		s.geometryValue.Store(uint64(uint32(w))<<32 | uint64(uint32(h)))
	}
}

func (s *inputSession) handle(raw json.RawMessage) error {
	return s.handleContext(context.Background(), raw)
}

func (s *inputSession) handleContext(ctx context.Context, raw json.RawMessage) error {
	var p struct {
		Events []desktop.InputEvent `json:"events"`
		Width  int                  `json:"width"`
		Height int                  `json:"height"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if len(p.Events) > 64 {
		return errors.New("too many input events")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	geometry := s.geometryValue.Load()
	width, height := int(geometry>>32), int(uint32(geometry))
	if s.releasePending {
		if err := s.releaseLocked(); err != nil {
			return err
		}
	}
	for i := range p.Events {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := &p.Events[i]
		switch e.Type {
		case "move", "down", "up":
			if (e.Type == "down" || e.Type == "up") && (e.Button < 1 || e.Button > 3) {
				return errors.New("invalid mouse button")
			}
			if p.Width > 0 && p.Height > 0 && width > 0 && height > 0 {
				e.X = desktop.ScaleCoordinate(e.X, p.Width, width)
				e.Y = desktop.ScaleCoordinate(e.Y, p.Height, height)
			}
			s.x, s.y = e.X, e.Y
		}
		keyID := e.Code
		if keyID == "" {
			keyID = e.Key
		}
		if err := s.apply([]desktop.InputEvent{*e}); err != nil {
			// Each physical down/up is injected separately. Only successfully
			// injected keys are owned; unsupported keys must never poison all
			// future mouse events by becoming an impossible release obligation.
			return err
		}
		switch e.Type {
		case "down":
			s.buttons[e.Button] = true
		case "key_down":
			s.keys[keyID] = *e
		case "up":
			delete(s.buttons, e.Button)
		case "key_up":
			delete(s.keys, keyID)
		}
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *inputSession) release() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseLocked()
}

func (s *inputSession) releaseLocked() error {
	var failures []error
	for button := range s.buttons {
		if err := s.apply([]desktop.InputEvent{{Type: "up", Button: button, X: s.x, Y: s.y}}); err != nil {
			failures = append(failures, err)
		} else {
			delete(s.buttons, button)
		}
	}
	for key, event := range s.keys {
		event.Type = "key_up"
		if err := s.apply([]desktop.InputEvent{event}); err != nil {
			failures = append(failures, err)
		} else {
			delete(s.keys, key)
		}
	}
	s.releasePending = len(failures) > 0
	return errors.Join(failures...)
}
