package main

import (
	"encoding/json"
	"net"
	"time"

	"github.com/yudesk/yudesk/internal/relay"
)

func (b *broker) handleResolve(c net.Conn, code string) {
	_ = c.SetWriteDeadline(time.Now().Add(3 * time.Second))
	result := relay.Resolution{Code: code, Error: "device unavailable"}
	if !b.permitLookup(c.RemoteAddr().String(), 1) {
		_ = json.NewEncoder(c).Encode(result)
		return
	}
	if b.accounts != nil && relay.IsDeviceCode(code) {
		if id, err := b.accounts.ResolveDeviceCode(code); err == nil {
			result.ID = id
			result.Error = ""
		}
	}
	_ = json.NewEncoder(c).Encode(result)
}

type lookupWindow struct {
	at    time.Time
	count int
}

func (b *broker) permitLookup(remote string, cost int) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	now := time.Now()
	b.Lock()
	defer b.Unlock()
	if b.lookups == nil {
		b.lookups = make(map[string]lookupWindow)
	}
	if len(b.lookups) >= 4096 {
		for key, value := range b.lookups {
			if now.Sub(value.at) >= time.Minute {
				delete(b.lookups, key)
			}
		}
		if _, ok := b.lookups[host]; !ok && len(b.lookups) >= 4096 {
			return false
		}
	}
	window := b.lookups[host]
	if now.Sub(window.at) >= time.Minute {
		window = lookupWindow{at: now}
	}
	if window.count+cost > 240 {
		return false
	}
	window.count += cost
	b.lookups[host] = window
	return true
}

func (b *broker) canonicalDeviceID(id string) string {
	if relay.IsDeviceCode(id) && b.accounts != nil {
		if full, err := b.accounts.ResolveDeviceCode(id); err == nil {
			return full
		}
	}
	return id
}
