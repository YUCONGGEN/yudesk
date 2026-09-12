package viewerapp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestShellURLBoundary(t *testing.T) {
	for _, url := range []string{"https://127.0.0.1:22/?access_token=t", "http://example.org:12/?access_token=t", "http://127.0.0.1:2/", "http://user@127.0.0.1:3/?access_token=t", "http://127.0.0.1:0/?access_token=t", "http://127.0.0.1:3/?access_token=t#secret"} {
		if validShellURL(url) {
			t.Errorf("accepted %q", url)
		}
	}
	if !validShellURL("http://127.0.0.1:4567/?access_token=private") {
		t.Fatal("valid loopback rejected")
	}
}

func TestShellFixture(t *testing.T) {
	mode := os.Getenv("YUDESK_SHELL_FIXTURE")
	if mode == "" {
		return
	}
	for _, arg := range os.Args {
		if strings.Contains(arg, "access_token") {
			os.Exit(12)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var command struct{ Action, URL string }
		if json.Unmarshal(scanner.Bytes(), &command) != nil {
			os.Exit(13)
		}
		switch command.Action {
		case "open":
			if !validShellURL(command.URL) {
				os.Exit(14)
			}
			if mode == "malformed" {
				fmt.Println(strings.Repeat("x", 20<<10))
				continue
			}
			fmt.Println(`{"event":"ready"}`)
		case "show":
			if mode == "crash" {
				os.Exit(3)
			}
			if mode == "userclose" {
				fmt.Println(`{"event":"closed"}`)
			}
		}
	}
	os.Exit(0)
}

func fixtureShell(t *testing.T, mode string, closed func()) *windowShell {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestShellFixture$")
	cmd.Env = append(os.Environ(), "YUDESK_SHELL_FIXTURE="+mode)
	s, err := startWindowShell(cmd, "http://127.0.0.1:4567/?access_token=private", closed, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return s
}

func TestShellHideMinimizePreserveProcess(t *testing.T) {
	var closed atomic.Int32
	s := fixtureShell(t, "normal", func() { closed.Add(1) })
	for range 12 {
		for _, action := range []string{"hide", "show", "minimize", "show", "enter-fullscreen", "exit-fullscreen"} {
			if err := s.send(action, ""); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case <-s.done:
			t.Fatal("window action terminated helper")
		default:
		}
	}
	s.close()
	if closed.Load() != 0 {
		t.Fatal("parent shutdown called user close")
	}
}

func TestShellCrashIsNotUserExit(t *testing.T) {
	var closed atomic.Int32
	s := fixtureShell(t, "crash", func() { closed.Add(1) })
	_ = s.send("show", "")
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		t.Fatal("crashed helper not reaped")
	}
	if closed.Load() != 0 {
		t.Fatal("crash exited core")
	}
	if s.send("show", "") == nil {
		t.Fatal("dead child accepted command")
	}
}

func TestShellUserClose(t *testing.T) {
	closed := make(chan struct{}, 1)
	s := fixtureShell(t, "userclose", func() { closed <- struct{}{} })
	if err := s.send("show", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("missing user close")
	}
}

func TestShellOversizeEventStopsHelper(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestShellFixture$")
	cmd.Env = append(os.Environ(), "YUDESK_SHELL_FIXTURE=malformed")
	if s, err := startWindowShell(cmd, "http://127.0.0.1:4567/?access_token=t", nil, nil); err == nil {
		s.close()
		t.Fatal("oversized event accepted")
	}
}
