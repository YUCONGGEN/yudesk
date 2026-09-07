package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/identity"
)

func TestNativeUnifiedLifecycle(t *testing.T) {
	if os.Getenv("YUDESK_UNIFIED_TEST") != "1" {
		t.Skip("opt-in native unified app")
	}
	for _, action := range []string{"revoke", "delete", "expiry-offline", "window-close", "hide-reopen"} {
		t.Run(action, func(t *testing.T) {
			f := newLifecycleFixture(t)
			dir, id := f.prepareAgent()
			state := filepath.Join(f.root, "unified-state")
			args := []string{"-state-dir", state, "-device-dir", dir, "-relay", f.address, "-relay-fingerprint", f.fingerprint, "-server", f.http.URL, "-web", "127.0.0.1:0", "-open=false"}
			p := f.launch("yudesk", args...)
			page := f.page(filepath.Join(state, "viewer-launcher.url"))
			f.waitOnline(id.ID)
			watchURL, _ := url.Parse(page)
			watchURL.Path = "/api/ui/watch"
			watch, err := http.Get(watchURL.String())
			if err != nil {
				t.Fatal(err)
			}
			defer watch.Body.Close()
			switch action {
			case "window-close":
				watch.Body.Close()
				p.exited(t)
			case "hide-reopen":
				u, _ := url.Parse(page)
				u.Path = "/api/local/hide"
				r, err := http.Post(u.String(), "application/json", bytes.NewBufferString("{}"))
				if err != nil {
					t.Fatal(err)
				}
				r.Body.Close()
				if r.StatusCode != 200 {
					t.Fatal("hide failed")
				}
				watch.Body.Close()
				time.Sleep(3 * time.Second)
				if !p.running() {
					t.Fatal("hidden app exited")
				}
				f.launch("yudesk", args...).exited(t)
				watch, err = http.Get(watchURL.String())
				if err != nil {
					t.Fatal(err)
				}
				watch.Body.Close()
				p.exited(t)
			case "expiry-offline":
				db, err := sql.Open("sqlite", f.database)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err = db.Exec("UPDATE licensed_devices SET active_until=? WHERE device_id=?", time.Now().Add(6*time.Second).Unix(), id.ID); err != nil {
					t.Fatal(err)
				}
				f.waitPairing(id.ID)
				f.offline.Store(true)
				f.b.Lock()
				f.b.controls[id.ID].conn.Close()
				f.b.Unlock()
				eventually(t, "unified process expires while offline", func() bool { return !p.running() })
			default:
				f.admin(action, id.ID)
				p.exited(t)
				f.waitOffline(id.ID)
				if action == "revoke" {
					f.launch("yudesk", args...).exited(t)
					f.waitOffline(id.ID)
				}
			}
		})
	}
}

func TestNativeUnifiedApplication(t *testing.T) {
	if os.Getenv("YUDESK_UNIFIED_TEST") != "1" {
		t.Skip("opt-in native unified app")
	}
	f := newLifecycleFixture(t)
	type running struct {
		process                    *lifecycleProcess
		directory, deviceDir, page string
		id                         identity.Identity
		code                       string
	}
	start := func(name string) running {
		dir := filepath.Join(f.root, name)
		deviceDir := filepath.Join(f.root, name+"-identity")
		id, err := identity.Load(deviceDir, "")
		if err != nil {
			t.Fatal(err)
		}
		p := f.launch("yudesk", "-state-dir", dir, "-device-dir", deviceDir, "-relay", f.address, "-relay-fingerprint", f.fingerprint, "-server", f.http.URL, "-web", "127.0.0.1:0", "-open=false")
		page := f.page(filepath.Join(dir, "viewer-launcher.url"))
		f.waitOnline(id.ID)
		f.admin("grant", id.ID)
		f.waitPairing(id.ID)
		code, err := f.b.accounts.EnsureDeviceCode(id.ID)
		if err != nil {
			t.Fatal(err)
		}
		return running{p, dir, deviceDir, page, id, code}
	}
	a, b := start("left"), start("right")
	t.Cleanup(func() {
		if t.Failed() {
			for _, p := range []running{a, b} {
				data, _ := os.ReadFile(p.process.logPath)
				for _, line := range strings.Split(string(data), "\n") {
					if strings.Contains(line, "handshake") || strings.Contains(line, "rejected") {
						t.Log(strings.ReplaceAll(line, p.id.PIN, "[PIN]"))
					}
				}
			}
		}
	})
	client := &http.Client{Timeout: 6 * time.Second}
	status := func(base string) map[string]any {
		u, _ := url.Parse(base)
		u.Path = "/api/local/status"
		r, err := client.Get(u.String())
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var s map[string]any
		if json.NewDecoder(r.Body).Decode(&s) != nil {
			t.Fatal("bad local status")
		}
		return s
	}
	s := status(a.page)
	if s["code"] != a.code || len(s["pin"].(string)) != 6 {
		t.Fatal("invalid local identity presentation")
	}
	duplicate := f.launch("yudesk", "-state-dir", a.directory, "-device-dir", a.deviceDir, "-web", "127.0.0.1:0", "-open=false")
	duplicate.exited(t)
	if !a.process.running() {
		t.Fatal("duplicate killed original")
	}
	// Keep test-owned watchers across browser shutdown and session transitions.
	var watchers []*http.Response
	for _, base := range []string{a.page, b.page} {
		u, _ := url.Parse(base)
		u.Path = "/api/ui/watch"
		r, err := http.Get(u.String())
		if err != nil {
			t.Fatal(err)
		}
		watchers = append(watchers, r)
		defer r.Body.Close()
	}
	if script := os.Getenv("YUDESK_UNIFIED_BROWSER_SCRIPT"); script != "" {
		result, err := exec.Command("node", script, a.page, b.page, b.code, b.id.PIN).CombinedOutput()
		if err != nil {
			t.Fatalf("unified browser: %v %s", err, result)
		}
		t.Logf("unified browser: %s", result)
		devices, err := f.b.accounts.ListLicensedDevices(20)
		if err != nil {
			t.Fatal(err)
		}
		for _, instance := range []running{a, b} {
			current := status(instance.page)
			persisted, err := identity.Load(instance.deviceDir, "")
			if err != nil || persisted.PIN != current["pin"] || current["pinSynced"] != true {
				t.Fatal("rotated PIN not durable and acknowledged", err)
			}
			found := false
			for _, device := range devices {
				if device.ID == instance.id.ID {
					found = device.PairingPIN == persisted.PIN
				}
			}
			if !found {
				t.Fatal("admin database PIN not synchronized")
			}
		}
	} else {
		f.post(a.page, "/connect", url.Values{"device_id": {b.code}, "pin": {b.id.PIN}})
		f.page(filepath.Join(a.directory, "viewer-session.url"))
		f.post(a.page, "/api/disconnect", nil)
	}
	base := f.page(filepath.Join(a.directory, "viewer-launcher.url"))
	resp, err := client.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(html), "允许连接本机") || !a.process.running() || !b.process.running() {
		t.Fatal("ending control lost unified dashboard or agent")
	}
	f.admin("disconnect", b.id.ID)
	b.process.exited(t)
	f.post(base, "/exit", nil)
	a.process.exited(t)
}

func TestNativeUnifiedSessionXReturnsHome(t *testing.T) {
	if os.Getenv("YUDESK_UNIFIED_TEST") != "1" {
		t.Skip("opt-in native unified app")
	}
	f := newLifecycleFixture(t)
	rightDir, rightID := f.prepareAgent()
	right := f.launch("yudesk-agent", "-config-dir", rightDir, "-relay", f.address, "-relay-fingerprint", f.fingerprint, "-server", f.http.URL, "-ui", "127.0.0.1:0", "-open=false")
	f.waitOnline(rightID.ID)
	f.admin("grant", rightID.ID)
	f.waitPairing(rightID.ID)
	leftDir := filepath.Join(f.root, "left-state")
	deviceDir := filepath.Join(f.root, "left-identity")
	leftID, err := identity.Load(deviceDir, "")
	if err != nil {
		t.Fatal(err)
	}
	left := f.launch("yudesk", "-state-dir", leftDir, "-device-dir", deviceDir, "-relay", f.address, "-relay-fingerprint", f.fingerprint, "-server", f.http.URL, "-web", "127.0.0.1:0", "-open=false")
	home := f.page(filepath.Join(leftDir, "viewer-launcher.url"))
	f.waitOnline(leftID.ID)
	f.admin("grant", leftID.ID)
	f.waitPairing(leftID.ID)
	u, _ := url.Parse(home)
	u.Path = "/api/ui/watch"
	watch, err := http.Get(u.String())
	if err != nil {
		t.Fatal(err)
	}
	f.post(home, "/connect", url.Values{"device_id": {rightID.ID}, "pin": {rightID.PIN}})
	f.page(filepath.Join(leftDir, "viewer-session.url"))
	watch.Body.Close() // same lifecycle event as native window X
	mode := *u
	mode.Path = "/api/ui/mode"
	client := &http.Client{Timeout: time.Second}
	eventually(t, "session X returns to home without killing the app", func() bool {
		r, err := client.Get(mode.String())
		if err != nil {
			return false
		}
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		return string(data) == "launcher"
	})
	if !left.running() || !right.running() {
		t.Fatal("session X terminated a client")
	}
	f.waitOnline(leftID.ID)
	f.waitPairing(rightID.ID)
	watch, err = http.Get(u.String())
	if err != nil {
		t.Fatal(err)
	}
	watch.Body.Close() // now home X must exit
	left.exited(t)
	if !right.running() {
		t.Fatal("home X terminated the other computer")
	}
}
