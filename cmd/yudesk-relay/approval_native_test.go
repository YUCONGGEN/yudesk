package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/approval"
	"github.com/yudesk/yudesk/internal/identity"
)

func TestNativeUnifiedConsent(t *testing.T) {
	if os.Getenv("YUDESK_UNIFIED_TEST") != "1" {
		t.Skip("opt-in local encrypted native fixture")
	}
	for _, action := range []string{"allow-control", "allow-view", "reject", "cancel", "timeout"} {
		t.Run(action, func(t *testing.T) {
			f := newLifecycleFixture(t)
			type side struct {
				p             *lifecycleProcess
				page, dir, id string
			}
			start := func(name string) side {
				dir := filepath.Join(f.root, name)
				identityDir := filepath.Join(f.root, name+"-identity")
				id, err := identity.Load(identityDir, "")
				if err != nil {
					t.Fatal(err)
				}
				p := f.launch("yudesk", "-state-dir", dir, "-device-dir", identityDir, "-relay", f.address, "-relay-fingerprint", f.fingerprint, "-server", f.http.URL, "-web", "127.0.0.1:0", "-open=false")
				page := f.page(filepath.Join(dir, "viewer-launcher.url"))
				f.waitOnline(id.ID)
				f.admin("grant", id.ID)
				f.waitPairing(id.ID)
				return side{p, page, dir, id.ID}
			}
			left, right := start("controller"), start("controlled")
			client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
			defer client.CloseIdleConnections()
			endpoint := func(page, path string) string { u, _ := url.Parse(page); u.Path = path; return u.String() }
			pending := func() *approval.Pending {
				r, err := client.Get(endpoint(right.page, "/api/local/approval"))
				if err != nil {
					return nil
				}
				defer r.Body.Close()
				var v struct {
					Pending *approval.Pending `json:"pending"`
				}
				json.NewDecoder(r.Body).Decode(&v)
				return v.Pending
			}
			mode := "control"
			if action == "allow-view" {
				mode = "view"
			}
			body := f.post(left.page, "/connect", url.Values{"device_id": {right.id}, "mode": {mode}})
			if !bytes.Contains([]byte(body), []byte("等待对方确认")) {
				t.Fatal("missing approval wait UI")
			}
			var p *approval.Pending
			eventually(t, "local consent request", func() bool { p = pending(); return p != nil })
			if p.Mode != mode || time.Until(p.Deadline) < 55*time.Second {
				t.Fatal("invalid approval", p)
			}
			if action == "cancel" {
				f.post(left.page, "/exit", nil)
				left.p.exited(t)
				eventually(t, "cancel clears consent", func() bool { return pending() == nil })
				return
			}
			if action == "timeout" {
				// Real 60-second wait catches old 15/30-second auth deadlines.
				timer := time.NewTimer(time.Until(p.Deadline) + 500*time.Millisecond)
				<-timer.C
				if pending() != nil {
					t.Fatal("expired request retained")
				}
			} else {
				payload, _ := json.Marshal(map[string]any{"requestID": p.ID, "accept": action != "reject"})
				r, err := client.Post(endpoint(right.page, "/api/local/approval"), "application/json", bytes.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				r.Body.Close()
				if r.StatusCode != 200 {
					t.Fatal(r.Status)
				}
			}
			if action == "allow-control" || action == "allow-view" {
				f.page(filepath.Join(left.dir, "viewer-session.url"))
				r, err := client.Get(endpoint(left.page, "/api/info"))
				if err != nil {
					t.Fatal(err)
				}
				defer r.Body.Close()
				var info struct {
					Meta struct {
						Control bool `json:"control"`
					} `json:"meta"`
				}
				json.NewDecoder(r.Body).Decode(&info)
				if info.Meta.Control != (mode == "control") {
					t.Fatal("wrong mode", info)
				}
				f.post(left.page, "/api/disconnect", nil)
			}
			eventually(t, "returns to usable launcher", func() bool {
				r, err := client.Get(endpoint(left.page, "/api/ui/mode"))
				if err != nil {
					return false
				}
				defer r.Body.Close()
				var data bytes.Buffer
				data.ReadFrom(r.Body)
				return data.String() == "launcher"
			})
			if !left.p.running() || !right.p.running() {
				t.Fatal("session result killed client")
			}
		})
	}
}
