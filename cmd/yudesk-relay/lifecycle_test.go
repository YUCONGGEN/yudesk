package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/account"
	"github.com/yudesk/yudesk/internal/identity"
	"github.com/yudesk/yudesk/internal/security"
	"github.com/yudesk/yudesk/internal/stream"
)

// Opt-in native executable tests. Only localhost, temporary identities, and a
// temporary database are used; no production credential or device is touched.
type lifecycleFixture struct {
	t                                              *testing.T
	b                                              *broker
	root, binaries, address, fingerprint, database string
	adminCSRF                                      string
	http                                           *httptest.Server
	offline                                        atomic.Bool
}

type lifecycleProcess struct {
	cmd     *exec.Cmd
	done    chan struct{}
	logPath string
}

func eventually(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out: " + description)
}

func newLifecycleFixture(t *testing.T, readDelay ...time.Duration) *lifecycleFixture {
	t.Helper()
	dir := os.Getenv("YUDESK_CLIENT_DIR")
	if dir == "" {
		t.Skip("set YUDESK_CLIENT_DIR to native Agent/Viewer build directory")
	}
	f := &lifecycleFixture{t: t, root: t.TempDir(), binaries: dir}
	f.database = filepath.Join(f.root, "test.db")
	store, err := account.Open(f.database)
	if err != nil {
		t.Fatal(err)
	}
	f.b = &broker{accounts: store, deviceLicenses: true, devices: map[string]waiting{}, active: map[string]activeSession{}}
	cfg, err := security.SelfSignedConfig("localhost lifecycle test")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(cfg.Certificates[0].Certificate[0])
	f.fingerprint = hex.EncodeToString(hash[:])
	listener, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.address = listener.Addr().String()
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if f.offline.Load() {
				conn.Close()
				continue
			}
			if len(readDelay) > 0 && readDelay[0] > 0 {
				conn = newDelayedReadConn(conn, readDelay[0])
			}
			workers.Add(1)
			go func() { defer workers.Done(); f.b.handle(conn) }()
		}
	}()
	mux := http.NewServeMux()
	registerStandaloneDeviceRoutes(mux, f.b, "local-test-only-password")
	f.http = httptest.NewServer(mux)
	adminRequest, _ := http.NewRequest(http.MethodGet, f.http.URL+"/admin", nil)
	adminRequest.SetBasicAuth("admin", "local-test-only-password")
	adminResponse, err := http.DefaultClient.Do(adminRequest)
	if err != nil {
		t.Fatal(err)
	}
	adminBody, _ := io.ReadAll(adminResponse.Body)
	adminResponse.Body.Close()
	csrfMarker := `name="_csrf" value="`
	csrfStart := strings.Index(string(adminBody), csrfMarker)
	if csrfStart < 0 {
		t.Fatal("lifecycle admin page did not contain a CSRF token")
	}
	csrfStart += len(csrfMarker)
	csrfEnd := strings.Index(string(adminBody)[csrfStart:], `"`)
	if csrfEnd < 0 {
		t.Fatal("lifecycle admin page contained a malformed CSRF token")
	}
	f.adminCSRF = string(adminBody)[csrfStart : csrfStart+csrfEnd]
	t.Cleanup(func() {
		listener.Close()
		f.http.Close()
		f.b.Lock()
		for _, c := range f.b.controls {
			c.conn.Close()
		}
		f.b.Unlock()
		workers.Wait()
		store.Close()
	})
	return f
}

func (f *lifecycleFixture) launch(component string, args ...string) *lifecycleProcess {
	f.t.Helper()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	logFile, err := os.CreateTemp(f.root, component+"-*.log")
	if err != nil {
		f.t.Fatal(err)
	}
	p := &lifecycleProcess{cmd: exec.Command(filepath.Join(f.binaries, component+suffix), args...), done: make(chan struct{}), logPath: logFile.Name()}
	// Keep any accidentally-created browser profile isolated from the user's
	// real YuDesk profile as a second guard behind -open=false.
	p.cmd.Env = append(os.Environ(), "LOCALAPPDATA="+filepath.Join(f.root, "cache"), "XDG_CACHE_HOME="+filepath.Join(f.root, "cache"))
	p.cmd.Stdout, p.cmd.Stderr = logFile, logFile
	if err := p.cmd.Start(); err != nil {
		logFile.Close()
		f.t.Fatal(err)
	}
	go func() { _ = p.cmd.Wait(); logFile.Close(); close(p.done) }()
	f.t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
		if f.t.Failed() {
			data, _ := os.ReadFile(p.logPath)
			f.t.Logf("%s: %s", component, data)
		}
	})
	return p
}

func (p *lifecycleProcess) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *lifecycleProcess) exited(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		data, _ := os.ReadFile(p.logPath)
		t.Fatalf("process %d did not exit within 5s: %s", p.cmd.Process.Pid, data)
	}
}

func (f *lifecycleFixture) agent(directory string) *lifecycleProcess {
	return f.launch("yudesk-agent", "-config-dir", directory, "-relay", f.address, "-relay-fingerprint", f.fingerprint, "-server", f.http.URL, "-ui", "127.0.0.1:0", "-open=false")
}

func (f *lifecycleFixture) viewer(directory string) *lifecycleProcess {
	return f.launch("yudesk-viewer", "-state-dir", directory, "-relay", f.address, "-relay-fingerprint", f.fingerprint, "-server", f.http.URL, "-web", "127.0.0.1:0", "-open=false")
}

func (f *lifecycleFixture) prepareAgent() (string, identity.Identity) {
	f.t.Helper()
	dir := filepath.Join(f.root, "agent")
	id, err := identity.Load(dir, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return dir, id
}

func (f *lifecycleFixture) waitOnline(id string) {
	eventually(f.t, "authenticated management connection", func() bool { f.b.Lock(); defer f.b.Unlock(); return f.b.controls[id] != nil })
}

func (f *lifecycleFixture) waitOffline(id string) {
	eventually(f.t, "all device connections removed", func() bool {
		f.b.Lock()
		defer f.b.Unlock()
		_, wait := f.b.devices[id]
		_, active := f.b.active[id]
		return f.b.controls[id] == nil && !wait && !active
	})
}

func (f *lifecycleFixture) waitPairing(id string) {
	eventually(f.t, "agent waiting for viewer", func() bool { f.b.Lock(); defer f.b.Unlock(); w, ok := f.b.devices[id]; return ok && w.role == "agent" })
}

func (f *lifecycleFixture) page(path string) string {
	f.t.Helper()
	var target string
	eventually(f.t, "local UI: "+filepath.Base(path), func() bool {
		data, err := os.ReadFile(path)
		target = strings.TrimSpace(string(data))
		return err == nil && target != ""
	})
	return target
}

func (f *lifecycleFixture) post(target, path string, values url.Values) string {
	f.t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		f.t.Fatal(err)
	}
	u.Path = path
	isAdmin := strings.HasPrefix(path, "/admin/")
	if isAdmin {
		values.Set("_csrf", f.adminCSRF)
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(values.Encode()))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if isAdmin {
		req.SetBasicAuth("admin", "local-test-only-password")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
	}
	return string(body)
}

func (f *lifecycleFixture) admin(action, id string) {
	f.post(f.http.URL, "/admin/"+action, url.Values{"device_id": {id}, "hours": {"1"}})
}

func (f *lifecycleFixture) assertState(text string) {
	f.t.Helper()
	page := httptest.NewRecorder()
	serveAdminPage(page, nil, f.b, nil, "", f.adminCSRF)
	if !strings.Contains(page.Body.String(), text) {
		f.t.Fatalf("admin page missing state %q", text)
	}
}

func (f *lifecycleFixture) connectViewer(directory string, id identity.Identity) *lifecycleProcess {
	p := f.viewer(directory)
	launcher := f.page(filepath.Join(directory, "viewer-launcher.url"))
	client := &http.Client{Timeout: 5 * time.Second}
	statusURL, _ := url.Parse(launcher)
	statusURL.Path = "/api/device/status"
	statusQuery := statusURL.Query()
	statusQuery.Set("ids", id.ID)
	statusURL.RawQuery = statusQuery.Encode()
	statusResponse, err := client.Get(statusURL.String())
	if err != nil {
		f.t.Fatal(err)
	}
	statusBody, _ := io.ReadAll(statusResponse.Body)
	statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusOK || !strings.Contains(string(statusBody), `"online":true`) || !strings.Contains(string(statusBody), `"active":true`) {
		f.t.Fatalf("launcher device presence: %d %s", statusResponse.StatusCode, statusBody)
	}
	f.post(launcher, "/connect", url.Values{"device_id": {id.ID}, "pin": {id.PIN}})
	sessionURL := f.page(filepath.Join(directory, "viewer-session.url"))
	u, _ := url.Parse(sessionURL)
	u.Path = "/api/info"
	resp, err := client.Get(u.String())
	if err != nil {
		f.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), id.ID) {
		f.t.Fatalf("remote info: %s", body)
	}
	if runtime.GOOS == "windows" || os.Getenv("YUDESK_TEST_DESKTOP") == "1" {
		u.Path = "/api/frames"
		response, err := client.Get(u.String())
		if err != nil {
			f.t.Fatal(err)
		}
		defer response.Body.Close()
		var sizes [8]byte
		if _, err := io.ReadFull(response.Body, sizes[:]); err != nil {
			f.t.Fatal(err)
		}
		hsize, dsize := binary.BigEndian.Uint32(sizes[:4]), binary.BigEndian.Uint32(sizes[4:])
		if hsize > 1<<20 || dsize > 32<<20 {
			f.t.Fatal("invalid frame size")
		}
		header, data := make([]byte, hsize), make([]byte, dsize)
		if _, err := io.ReadFull(response.Body, header); err != nil {
			f.t.Fatal(err)
		}
		if _, err := io.ReadFull(response.Body, data); err != nil {
			f.t.Fatal(err)
		}
		var frame stream.TileFrame
		if err := json.Unmarshal(header, &frame); err != nil {
			f.t.Fatal(err)
		}
		if err := frame.Validate(data); err != nil {
			f.t.Fatal(err)
		}
		if !frame.Reset || frame.Width < 100 || frame.Height < 100 {
			f.t.Fatal("missing full desktop keyframe")
		}
		offset := 0
		for _, tile := range frame.Tiles {
			decoded, _, err := image.DecodeConfig(bytes.NewReader(data[offset : offset+tile.Size]))
			offset += tile.Size
			if err != nil || decoded.Width != tile.Width || decoded.Height != tile.Height {
				f.t.Fatalf("invalid encoded tile: %v", err)
			}
		}
		f.t.Logf("decoded remote desktop %dx%d (%d tiles, %d bytes) in original Viewer PID %d", frame.Width, frame.Height, len(frame.Tiles), len(data), p.cmd.Process.Pid)
	}
	if !p.running() {
		f.t.Fatal("launcher process was replaced instead of reused")
	}
	f.assertState(`class="badge connected">连接中`)
	return p
}

func TestNativeClientLifecycle(t *testing.T) {
	t.Run("unlicensed-admin-disconnect", func(t *testing.T) {
		f := newLifecycleFixture(t)
		dir, id := f.prepareAgent()
		p := f.agent(dir)
		f.waitOnline(id.ID)
		f.assertState("在线 · 未被连接")
		f.admin("disconnect", id.ID)
		p.exited(t)
		f.waitOffline(id.ID)
	})
	t.Run("waiting-ui-exit-and-single-instance", func(t *testing.T) {
		f := newLifecycleFixture(t)
		dir, id := f.prepareAgent()
		p := f.agent(dir)
		f.waitOnline(id.ID)
		f.admin("grant", id.ID)
		f.waitPairing(id.ID)
		for i := 0; i < 5; i++ {
			f.agent(dir).exited(t)
		}
		if !p.running() {
			t.Fatal("original Agent no longer running")
		}
		f.post(f.page(filepath.Join(dir, "agent-ui.url")), "/exit", nil)
		p.exited(t)
		f.waitOffline(id.ID)
		p = f.agent(dir)
		f.waitPairing(id.ID)
		f.admin("disconnect", id.ID)
		p.exited(t)
		f.waitOffline(id.ID)
	})
	t.Run("disabled-restart-rejected", func(t *testing.T) {
		f := newLifecycleFixture(t)
		dir, id := f.prepareAgent()
		p := f.agent(dir)
		f.waitOnline(id.ID)
		f.admin("grant", id.ID)
		f.waitPairing(id.ID)
		f.admin("revoke", id.ID)
		p.exited(t)
		f.waitOffline(id.ID)
		f.agent(dir).exited(t)
		f.waitOffline(id.ID)
	})
	t.Run("delete-does-not-reregister", func(t *testing.T) {
		f := newLifecycleFixture(t)
		dir, id := f.prepareAgent()
		p := f.agent(dir)
		f.waitOnline(id.ID)
		f.admin("grant", id.ID)
		f.waitPairing(id.ID)
		f.admin("delete", id.ID)
		p.exited(t)
		f.waitOffline(id.ID)
		devices, err := f.b.accounts.ListLicensedDevices(10)
		if err != nil || len(devices) != 0 {
			t.Fatalf("deleted device came back: %v %v", devices, err)
		}
	})
	for _, action := range []string{"disconnect", "revoke", "delete"} {
		t.Run("active-"+action+"-exits-both", func(t *testing.T) {
			f := newLifecycleFixture(t)
			dir, id := f.prepareAgent()
			a := f.agent(dir)
			f.waitOnline(id.ID)
			f.admin("grant", id.ID)
			f.waitPairing(id.ID)
			viewerDir := filepath.Join(f.root, "viewer")
			v := f.connectViewer(viewerDir, id)
			for i := 0; i < 5; i++ {
				f.viewer(viewerDir).exited(t)
			}
			if !v.running() {
				t.Fatal("original Viewer no longer running")
			}
			f.admin(action, id.ID)
			a.exited(t)
			v.exited(t)
			f.waitOffline(id.ID)
		})
	}
	t.Run("viewer-exit-and-network-recovery", func(t *testing.T) {
		f := newLifecycleFixture(t)
		dir, id := f.prepareAgent()
		a := f.agent(dir)
		f.waitOnline(id.ID)
		f.admin("grant", id.ID)
		f.waitPairing(id.ID)
		viewerDir := filepath.Join(f.root, "viewer")
		v := f.connectViewer(viewerDir, id)
		f.post(f.page(filepath.Join(viewerDir, "viewer-session.url")), "/api/exit", nil)
		v.exited(t)
		f.waitPairing(id.ID)
		f.b.Lock()
		old := f.b.controls[id.ID]
		old.conn.Close()
		f.b.Unlock()
		eventually(t, "management reconnect", func() bool { f.b.Lock(); defer f.b.Unlock(); c := f.b.controls[id.ID]; return c != nil && c != old })
		if !a.running() {
			t.Fatal("ordinary network loss killed Agent")
		}
		f.admin("disconnect", id.ID)
		a.exited(t)
		f.waitOffline(id.ID)
		v = f.viewer(viewerDir)
		f.post(f.page(filepath.Join(viewerDir, "viewer-launcher.url")), "/exit", nil)
		v.exited(t)
	})
	for _, offline := range []bool{false, true} {
		name := "expiry-online"
		if offline {
			name = "expiry-even-offline"
		}
		t.Run(name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			dir, id := f.prepareAgent()
			a := f.agent(dir)
			f.waitOnline(id.ID)
			// Shorten only this isolated database to avoid an hour-long test.
			db, err := sql.Open("sqlite", f.database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("UPDATE licensed_devices SET active_until=? WHERE device_id=?", time.Now().Add(7*time.Second).Unix(), id.ID); err != nil {
				t.Fatal(err)
			}
			f.waitPairing(id.ID)
			v := f.connectViewer(filepath.Join(f.root, "viewer"), id)
			if offline {
				f.offline.Store(true)
				f.b.Lock()
				f.b.controls[id.ID].conn.Close()
				f.b.Unlock()
			}
			eventually(t, "Agent expiry while server unreachable", func() bool { return !a.running() })
			v.exited(t)
			f.waitOffline(id.ID)
			f.offline.Store(false)
			f.agent(dir).exited(t)
			f.waitOffline(id.ID)
		})
	}
}
