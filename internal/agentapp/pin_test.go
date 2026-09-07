package agentapp

import (
	"sync"
	"testing"

	"github.com/yudesk/yudesk/internal/identity"
)

func TestRotateLivePINWithoutClosingDeviceAndConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	id, err := identity.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{pin: id.PIN, quit: make(chan struct{})}
	d := &Device{a: a, identityDir: dir}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = a.currentPIN()
				_ = a.matchesPIN(id.PIN)
				_ = d.Status()
			}
		}()
	}
	latest, err := d.RotatePIN()
	wg.Wait()
	if err != nil || a.matchesPIN(id.PIN) || !a.matchesPIN(latest) || a.quitting() {
		t.Fatal("rotation did not replace live PIN", err)
	}
	if d.Status().PINSynced {
		t.Fatal("reported synced before server acknowledgment")
	}
	a.statusMu.Lock()
	a.managementOnline = true
	a.reportedPIN = latest
	a.statusMu.Unlock()
	if !d.Status().PINSynced {
		t.Fatal("server acknowledgment ignored")
	}
}
