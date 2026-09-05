package main

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/yudesk/yudesk/internal/desktop"
)

type inputSession struct {
	mu            sync.Mutex
	width, height int
	x, y          int
	buttons       map[int]bool
	keys          map[string]bool
	apply         func([]desktop.InputEvent) error
	wake          chan struct{}
}

func newInputSession() *inputSession {
	return &inputSession{buttons: map[int]bool{}, keys: map[string]bool{}, apply: desktop.ApplyInput, wake: make(chan struct{}, 1)}
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
		// Track before injection so a partially failed batch is also released.
		switch e.Type {
		case "down":
			s.buttons[e.Button] = true
		case "key_down":
			s.keys[e.Key] = true
		}
		if err := s.apply([]desktop.InputEvent{*e}); err != nil {
			return err
		}
		switch e.Type {
		case "up":
			delete(s.buttons, e.Button)
		case "key_up":
			delete(s.keys, e.Key)
		}
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *inputSession) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for button := range s.buttons {
		_ = s.apply([]desktop.InputEvent{{Type: "up", Button: button, X: s.x, Y: s.y}})
	}
	for key := range s.keys {
		_ = s.apply([]desktop.InputEvent{{Type: "key_up", Key: key}})
	}
	s.buttons = map[int]bool{}
	s.keys = map[string]bool{}
}
