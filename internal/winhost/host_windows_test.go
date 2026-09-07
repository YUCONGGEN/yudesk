//go:build windows

package winhost

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestUntrustedClientCannotUseService(t *testing.T) {
	if _, err := Call("capture", nil); err == nil || !strings.Contains(err.Error(), "安装版") {
		t.Fatalf("untrusted executable not rejected: %v", err)
	}
	for _, method := range []string{"exec", "file", "token", "clipboard", "unknown"} {
		if _, err := Call(method, nil); err == nil {
			t.Fatalf("unapproved method accepted: %s", method)
		}
	}
}

func TestInstalledCaptureWithLimitedToken(t *testing.T) {
	if os.Getenv("YUDESK_TEST_INSTALLED_SERVICE") != "1" {
		t.Skip("requires explicitly authorized local service installation")
	}
	// Only the protected installed executable is launched, in diagnostic capture
	// mode. No GUI, clipboard, locking, passwords or input injection is involved.
	linked, err := windows.GetCurrentProcessToken().GetLinkedToken()
	restricted := err != nil
	if err != nil {
		// Built-in Administrator may have no UAC linked token. Produce a
		// medium-integrity restricted copy: admin SID deny-only, privileges off.
		var source windows.Token
		if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &source); err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
		if err != nil {
			t.Fatal(err)
		}
		disable := windows.SIDAndAttributes{Sid: admin}
		ok, _, callErr := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken").Call(uintptr(source), 1, 1, uintptr(unsafe.Pointer(&disable)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&linked)))
		if ok == 0 {
			t.Fatal(callErr)
		}
		medium, err := windows.StringToSid("S-1-16-8192")
		if err != nil {
			t.Fatal(err)
		}
		label := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: medium, Attributes: windows.SE_GROUP_INTEGRITY}}
		if err := windows.SetTokenInformation(linked, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&label)), label.Size()); err != nil {
			linked.Close()
			t.Fatal(err)
		}
		t.Log("using restricted medium-integrity token; no UAC linked token on this account")
	}
	defer linked.Close()
	if !restricted && linked.IsElevated() {
		t.Fatal("linked token is unexpectedly elevated")
	}
	groups, err := linked.GetTokenGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) && group.Attributes&windows.SE_GROUP_ENABLED != 0 {
			t.Fatal("admin group still enabled")
		}
	}
	path, err := installedPath()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-desktop-service-check")
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(linked), HideWindow: true}
	cmd.Env = append(os.Environ(), "YUDESK_SERVICE_DIAGNOSTIC=1")
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ordinary-user capture failed: %v %s", err, data)
	}
	var result struct {
		Bytes int
		State State
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Bytes <= 0 || result.Bytes > 32<<20 {
		t.Fatalf("invalid capture size %d", result.Bytes)
	}
	if !result.State.TrustedClient || !result.State.Running {
		t.Fatalf("invalid service status: %+v", result.State)
	}
	t.Logf("non-elevated installed client captured %d bytes", result.Bytes)
}
