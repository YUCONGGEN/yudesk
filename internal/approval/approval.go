// Package approval implements single-use, locally approved remote access.
package approval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

var ErrDenied = errors.New("对方拒绝了连接请求")
var ErrTimeout = errors.New("等待对方确认超时（60 秒）")
var ErrBusy = errors.New("对方正在处理连接请求，请稍后再试")

type Pending struct {
	ID       string    `json:"id"`
	Mode     string    `json:"mode"`
	Deadline time.Time `json:"deadline"`
}
type entry struct {
	Pending
	result chan bool
}
type Broker struct {
	mu      sync.Mutex
	pending *entry
	history []time.Time
	events  chan struct{}
	timeout time.Duration
}

func New() *Broker                        { return &Broker{events: make(chan struct{}, 1), timeout: 60 * time.Second} }
func (b *Broker) Events() <-chan struct{} { return b.events }
func (b *Broker) changed() {
	select {
	case b.events <- struct{}{}:
	default:
	}
}
func (b *Broker) Pending() *Pending {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil || !b.pending.Deadline.After(time.Now()) {
		return nil
	}
	value := b.pending.Pending
	return &value
}
func (b *Broker) Request(ctx context.Context, mode string) error {
	if mode != "control" && mode != "view" {
		return errors.New("无效连接模式")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	b.mu.Lock()
	now := time.Now()
	if b.pending != nil {
		b.mu.Unlock()
		return ErrBusy
	}
	kept := b.history[:0]
	for _, at := range b.history {
		if now.Sub(at) < time.Minute {
			kept = append(kept, at)
		}
	}
	b.history = kept
	if len(kept) >= 3 {
		b.mu.Unlock()
		return errors.New("连接请求过于频繁，请一分钟后再试")
	}
	b.history = append(b.history, now)
	p := &entry{Pending: Pending{ID: hex.EncodeToString(nonce[:]), Mode: mode, Deadline: now.Add(b.timeout)}, result: make(chan bool, 1)}
	b.pending = p
	b.mu.Unlock()
	b.changed()
	defer func() {
		b.mu.Lock()
		if b.pending == p {
			b.pending = nil
		}
		b.mu.Unlock()
		b.changed()
	}()
	timer := time.NewTimer(time.Until(p.Deadline))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ErrTimeout
	case allowed := <-p.result:
		if err := ctx.Err(); err != nil {
			return err
		}
		if !p.Deadline.After(time.Now()) {
			return ErrTimeout
		}
		if !allowed {
			return ErrDenied
		}
		return nil
	}
}
func (b *Broker) Resolve(id string, accept bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil || b.pending.ID != id || !b.pending.Deadline.After(time.Now()) {
		return errors.New("请求已结束或过期")
	}
	select {
	case b.pending.result <- accept:
		return nil
	default:
		return errors.New("请求已处理")
	}
}
func (b *Broker) Cancel() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending != nil {
		select {
		case b.pending.result <- false:
		default:
		}
	}
}
