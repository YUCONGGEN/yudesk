package agentapp

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/yudesk/yudesk/internal/desktop"
)

type inputSession struct {
	mu             sync.Mutex
	width, height  int
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

func (s *inputSession) geometry(w, h int) { s.mu.Lock(); s.width, s.height = w, h; s.mu.Unlock() }

func (s *inputSession) handle(raw json.RawMessage) error {
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
	if s.releasePending {
		if err := s.releaseLocked(); err != nil {
			return err
		}
	}
	for i := range p.Events {
		e := &p.Events[i]
		switch e.Type {
		case "move", "down", "up":
			if (e.Type == "down" || e.Type == "up") && (e.Button < 1 || e.Button > 3) {
				return errors.New("invalid mouse button")
			}
			if p.Width > 0 && p.Height > 0 && s.width > 0 && s.height > 0 {
				e.X = desktop.ScaleCoordinate(e.X, p.Width, s.width)
				e.Y = desktop.ScaleCoordinate(e.Y, p.Height, s.height)
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
