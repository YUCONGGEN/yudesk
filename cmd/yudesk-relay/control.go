package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
)

type deviceControl struct {
	conn     net.Conn
	stop     chan string
	stopping bool         // protected by broker.Mutex
	lastSeen atomic.Int64 // last authenticated incoming heartbeat, Unix seconds
	pin      atomic.Value // last PIN successfully committed to the server database

	portMapSession string
	portMapMu      sync.Mutex
	portMapResult  *relay.PortMapResult
}

func (c *deviceControl) setPortMapResult(result relay.PortMapResult) {
	if result.RequestID == "" {
		return
	}
	c.portMapMu.Lock()
	c.portMapResult = &result
	c.portMapMu.Unlock()
}

func (c *deviceControl) takePortMapResult() *relay.PortMapResult {
	c.portMapMu.Lock()
	defer c.portMapMu.Unlock()
	result := c.portMapResult
	c.portMapResult = nil
	return result
}

func (b *broker) handleControl(c net.Conn, reader *bufio.Reader, hello relay.Hello) {
	encoder := json.NewEncoder(c)
	send := func(m relay.ControlMessage) error {
		_ = c.SetWriteDeadline(time.Now().Add(3 * time.Second))
		return encoder.Encode(m)
	}
	stop := func(code, message string) { _ = send(relay.ControlMessage{Type: "stop", Code: code, Message: message}) }
	if !b.deviceLicenses {
		stop("DENIED", "device management is unavailable")
		return
	}
	if len(hello.PublicKey) != ed25519.PublicKeySize || secureconn.DeviceID(hello.PublicKey) != hello.ID {
		stop("DENIED", "invalid device identity")
		return
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return
	}
	if send(relay.ControlMessage{Type: "challenge", Nonce: nonce}) != nil {
		return
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 16<<10)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if !scanner.Scan() {
		return
	}
	var proof relay.ControlMessage
	if json.Unmarshal(scanner.Bytes(), &proof) != nil || proof.Type != "proof" || !ed25519.Verify(hello.PublicKey, relay.ControlProof(hello.ID, nonce), proof.Signature) {
		stop("DENIED", "device identity authentication failed")
		return
	}
	if err := b.accounts.RegisterLicensedDeviceNamed(hello.ID, hello.PublicKey, hello.Name); err != nil {
		stop("DISABLED", err.Error())
		return
	}
	deviceCode, err := b.accounts.EnsureDeviceCode(hello.ID)
	if err != nil {
		stop("DENIED", "cannot assign device code")
		return
	}
	if hello.PIN != "" {
		if err := b.accounts.UpdateLicensedDevicePIN(hello.ID, hello.PIN); err != nil {
			stop("DENIED", "invalid pairing PIN")
			return
		}
	}
	if err := b.ensureAutomaticDeviceLicense(hello.ID, c.RemoteAddr().String()); err != nil {
		stop("DENIED", "automatic device activation failed")
		return
	}
	portMapSession, err := randomURLToken(32)
	if err != nil {
		stop("DENIED", "cannot create device management session")
		return
	}
	control := &deviceControl{conn: c, stop: make(chan string, 1), portMapSession: portMapSession}
	control.lastSeen.Store(time.Now().Unix())
	control.pin.Store(hello.PIN)
	b.Lock()
	if b.controls == nil {
		b.controls = make(map[string]*deviceControl)
	}
	previous := b.controls[hello.ID]
	if previous != nil && previous.stopping {
		b.Unlock()
		stop("STOP", "device process is stopping")
		return
	}
	if previous != nil {
		// A network transition can establish the replacement management socket
		// before the old socket's heartbeat deadline expires. The authenticated
		// device owns the same Ed25519 key, so let it take over immediately and
		// retain its port-map session instead of terminating the whole process as
		// a duplicate. The old handler's deferred cleanup is generation-checked.
		control.portMapSession = previous.portMapSession
	}
	b.controls[hello.ID] = control
	b.Unlock()
	if previous != nil {
		_ = previous.conn.Close()
	}
	defer func() {
		var connections []net.Conn
		var removedPortMaps []*serverPortMap
		b.Lock()
		if b.controls[hello.ID] != control {
			b.Unlock()
			return
		}
		delete(b.controls, hello.ID)
		b.removeMeetingLocked(hello.ID)
		owner := "device:" + hello.ID
		if waiting, ok := b.devices[hello.ID]; ok && waiting.owner == owner {
			delete(b.devices, hello.ID)
			connections = append(connections, waiting.conn)
			waiting.ready <- nil
		}
		if active, ok := b.active[hello.ID]; ok && active.owner == owner {
			delete(b.active, hello.ID)
			connections = append(connections, active.agent, active.viewer)
		}
		for mapID, mapping := range b.portMaps {
			if mapping.DeviceID == hello.ID {
				if removed := b.removePortMapLocked(mapID); removed != nil {
					removedPortMaps = append(removedPortMaps, removed)
				}
			}
		}
		b.Unlock()
		b.closePortMapHooks(removedPortMaps)
		for _, connection := range connections {
			if connection != nil {
				_ = connection.Close()
			}
		}
	}()
	readDone := make(chan struct{})
	defer func() {
		// Join the reader before persisting its final confirmed observation.
		_ = c.Close()
		<-readDone
		_ = b.accounts.TouchLicensedDevice(hello.ID, time.Unix(control.lastSeen.Load(), 0))
	}()
	go func() {
		defer close(readDone)
		var persisted time.Time
		for {
			_ = c.SetReadDeadline(time.Now().Add(12 * time.Second))
			if !scanner.Scan() {
				return
			}
			var reply relay.ControlMessage
			if json.Unmarshal(scanner.Bytes(), &reply) != nil || reply.Type != "pong" {
				return
			}
			now := time.Now()
			control.lastSeen.Store(now.Unix())
			if reply.PIN != "" && reply.PIN != control.pin.Load().(string) {
				if b.accounts.UpdateLicensedDevicePIN(hello.ID, reply.PIN) != nil {
					return
				}
				control.pin.Store(reply.PIN)
			}
			if reply.PortMapCommand != nil {
				control.setPortMapResult(b.processPortMapCommand(hello.ID, control.portMapSession, reply.PortMapCommand))
			}
			// The admin view reads live memory. Bound database writes during
			// long sessions while limiting crash loss to about 15 seconds.
			if now.Sub(persisted) >= 15*time.Second {
				if b.accounts.TouchLicensedDevice(hello.ID, now) == nil {
					persisted = now
				}
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	wasActive := false
	for {
		expiry, err := b.accounts.DeviceLicenseExpiry(hello.ID)
		if err != nil {
			stop("DISABLED", "device was disabled or removed")
			return
		}
		active := expiry.After(time.Now())
		if !active && (wasActive || !expiry.IsZero()) {
			stop("EXPIRED", "device license expired")
			return
		}
		wasActive = wasActive || active
		// Renew the data connection's expiry without disconnecting an ongoing session.
		if active && !account.IsPermanentDeviceLicense(expiry) {
			b.Lock()
			if wait, ok := b.devices[hello.ID]; ok && wait.conn != nil {
				_ = wait.conn.SetDeadline(expiry)
			}
			if session, ok := b.active[hello.ID]; ok {
				if session.agent != nil {
					_ = session.agent.SetDeadline(expiry)
				}
				if session.viewer != nil {
					_ = session.viewer.SetDeadline(expiry)
				}
			}
			b.Unlock()
		}
		if send(relay.ControlMessage{
			Type: "status", Active: active, ActiveUntil: expiry, DeviceCode: deviceCode, PIN: control.pin.Load().(string),
			PortMaps: b.portMapStatus(hello.ID, control.portMapSession), PortMapResult: control.takePortMapResult(),
			PortMapServer: b.portMapServer, PortMapSession: control.portMapSession, PortMapCertificate: b.portMapCertificate,
		}) != nil {
			return
		}
		select {
		case reason := <-control.stop:
			stop("STOP", reason)
			return
		case <-readDone:
			return
		case <-ticker.C:
		}
	}
}
