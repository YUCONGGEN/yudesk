package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/relay"
	"github.com/yudesk/yudesk/internal/secureconn"
)

func portMapFixture(t *testing.T, devices int) (*broker, []string) {
	t.Helper()
	store, err := account.Open(t.TempDir() + "/accounts.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b := &broker{
		accounts: store, controls: make(map[string]*deviceControl),
		portMaps: make(map[string]*serverPortMap), portByNumber: make(map[int]string),
		portMapServer: "www.yucg.cn:8232",
	}
	ids := make([]string, 0, devices)
	for index := 0; index < devices; index++ {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		id := secureconn.DeviceID(public)
		if err := store.RegisterLicensedDeviceNamed(id, public, fmt.Sprintf("设备 %d", index+1)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GrantDeviceLicense(id, time.Hour); err != nil {
			t.Fatal(err)
		}
		b.controls[id] = &deviceControl{portMapSession: fmt.Sprintf("session-%d", index)}
		ids = append(ids, id)
	}
	return b, ids
}

func TestPortMapAllocationEnforcesRangeUniquenessAndLimit(t *testing.T) {
	b, ids := portMapFixture(t, 2)
	ports := make(map[int]string)
	for deviceIndex, id := range ids {
		for index := 0; index < portMapLimit; index++ {
			result := b.createPortMap(id, fmt.Sprintf("session-%d", deviceIndex), relay.PortMapCommand{
				RequestID: fmt.Sprintf("request-%d-%d", deviceIndex, index), LocalPort: 3000 + index,
			})
			if !result.OK {
				t.Fatalf("allocation failed: %+v", result)
			}
		}
	}
	for _, mapping := range b.portMaps {
		if mapping.RemotePort < portMapMin || mapping.RemotePort > portMapMax {
			t.Fatalf("port outside pool: %d", mapping.RemotePort)
		}
		if owner := ports[mapping.RemotePort]; owner != "" {
			t.Fatalf("duplicate public port %d for %s and %s", mapping.RemotePort, owner, mapping.DeviceID)
		}
		ports[mapping.RemotePort] = mapping.DeviceID
	}
	if len(ports) != 10 {
		t.Fatalf("got %d mappings, want 10", len(ports))
	}
	result := b.createPortMap(ids[0], "session-0", relay.PortMapCommand{RequestID: "sixth", LocalPort: 9090})
	if result.OK {
		t.Fatal("sixth mapping was accepted")
	}
}

func TestFRPPluginRejectsForgedAndClosedMappings(t *testing.T) {
	b, ids := portMapFixture(t, 1)
	id := ids[0]
	if result := b.createPortMap(id, "session-0", relay.PortMapCommand{RequestID: "request", LocalPort: 8080}); !result.OK {
		t.Fatal(result.Message)
	}
	var mapping *serverPortMap
	for _, candidate := range b.portMaps {
		mapping = candidate
	}
	request := func(op string, content any) (bool, string) {
		raw, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		return b.authorizeFRPRequest(frpPluginRequest{Version: "0.1.0", Op: op, Content: raw})
	}
	metas := map[string]string{"yudesk_session": "session-0"}
	if ok, reason := request("Login", frpPluginLogin{User: id, Metas: metas}); !ok {
		t.Fatalf("valid login rejected: %s", reason)
	}
	proxy := frpPluginProxy{
		User: frpPluginUser{User: id, Metas: metas}, ProxyName: expectedFRPProxyName(id, mapping.ID),
		ProxyType: "tcp", RemotePort: mapping.RemotePort,
		Metas: map[string]string{"yudesk_map_id": mapping.ID, "yudesk_map_secret": mapping.Secret},
	}
	if ok, reason := request("NewProxy", proxy); !ok {
		t.Fatalf("valid proxy rejected: %s", reason)
	}
	proxy.RemotePort++
	if ok, _ := request("NewProxy", proxy); ok {
		t.Fatal("forged remote port was accepted")
	}
	b.closeDevicePortMaps(id)
	if ok, _ := request("NewUserConn", frpPluginUserConnection{User: frpPluginUser{User: id, Metas: metas}, ProxyName: expectedFRPProxyName(id, mapping.ID), ProxyType: "tcp"}); ok {
		t.Fatal("closed mapping still accepted a public connection")
	}
}

func TestPortMapRequestIsIdempotent(t *testing.T) {
	b, ids := portMapFixture(t, 1)
	command := relay.PortMapCommand{RequestID: "same-request", LocalPort: 8080}
	if first := b.createPortMap(ids[0], "session-0", command); !first.OK {
		t.Fatal(first.Message)
	}
	if second := b.createPortMap(ids[0], "session-0", command); !second.OK {
		t.Fatal(second.Message)
	}
	if len(b.portMaps) != 1 {
		t.Fatalf("retry created %d mappings", len(b.portMaps))
	}
}

func TestPortMapHookIsTransactional(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	b, ids := portMapFixture(t, 1)
	directory := t.TempDir()
	hook := filepath.Join(directory, "hook.sh")
	logFile := filepath.Join(directory, "hook.log")
	t.Setenv("YUDESK_TEST_HOOK_LOG", logFile)
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"${2:-}\" >> \"$YUDESK_TEST_HOOK_LOG\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	b.portMapHook = hook
	command := relay.PortMapCommand{RequestID: "hook-open", LocalPort: 8080}
	if result := b.createPortMap(ids[0], "session-0", command); !result.OK {
		t.Fatal(result.Message)
	}
	var mapping *serverPortMap
	for _, mapping = range b.portMaps {
	}
	if mapping == nil || !mapping.HookReady {
		t.Fatal("mapping was published before its port hook committed")
	}
	if result := b.deletePortMap(ids[0], relay.PortMapCommand{RequestID: "hook-close", MapID: mapping.ID}); !result.OK {
		t.Fatal(result.Message)
	}
	if err := b.cleanupPortMapPorts(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	log := string(contents)
	if !strings.Contains(log, fmt.Sprintf("open %d", mapping.RemotePort)) || !strings.Contains(log, fmt.Sprintf("close %d", mapping.RemotePort)) || !strings.Contains(log, "cleanup ") {
		t.Fatalf("unexpected hook calls: %q", log)
	}

	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 9\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	failed := b.createPortMap(ids[0], "session-0", relay.PortMapCommand{RequestID: "hook-failure", LocalPort: 8081})
	if failed.OK || len(b.portMaps) != 0 || len(b.portByNumber) != 0 {
		t.Fatalf("failed hook leaked a mapping: result=%+v maps=%d ports=%d", failed, len(b.portMaps), len(b.portByNumber))
	}
}
