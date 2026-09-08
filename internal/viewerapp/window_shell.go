package viewerapp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// The installed Unix helper owns only our WebView. Anonymous pipes keep the
// local UI credential out of argv, the environment and a public IPC listener.
// No helper failure means user intent to quit: only an explicit closed event
// does. A subsequent singleton Show can replace a failed helper.
type windowShell struct {
	cmd        *exec.Cmd
	input      io.WriteCloser
	writeMu    sync.Mutex
	done       chan struct{}
	ready      chan error
	closing    atomic.Bool
	closedOnce sync.Once
}

func validShellURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 8192 || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Fragment != "" {
		return false
	}
	p, err := strconv.Atoi(u.Port())
	return err == nil && p > 0 && p <= 65535 && u.Query().Get("access_token") != ""
}

func startWindowShell(cmd *exec.Cmd, address string, onClosed func(), journal *lifecycleJournal) (*windowShell, error) {
	if !validShellURL(address) {
		return nil, errors.New("invalid local window address")
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	// Native renderer diagnostics may include URLs. Never copy stderr into the
	// application log; the helper protocol exposes only fixed error categories.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, errors.New("无法启动原生窗口，请重新安装 YuDesk")
	}
	s := &windowShell{cmd: cmd, input: input, done: make(chan struct{}), ready: make(chan error, 1)}
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 16<<10)
		for scanner.Scan() {
			var event struct {
				Event string `json:"event"`
			}
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				break
			}
			switch event.Event {
			case "ready":
				select {
				case s.ready <- nil:
				default:
				}
			case "closed":
				if !s.closing.Load() && onClosed != nil {
					s.closedOnce.Do(func() { go onClosed() })
				}
			case "error":
				journal.record("window_monitor_lost", errors.New("native renderer error"))
				select {
				case s.ready <- errors.New("原生窗口加载失败，请重新打开 YuDesk"):
				default:
				}
			}
		}
		// EOF or a malformed/oversized event cannot leave a hanging child.
		_ = input.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		close(s.done)
		if !s.closing.Load() {
			journal.record("window_monitor_lost", io.EOF)
		}
	}()
	if err = s.send("open", address); err != nil {
		s.close()
		return nil, err
	}
	select {
	case err = <-s.ready:
	case <-s.done:
		err = errors.New("原生窗口已停止，请重新安装或再次打开 YuDesk")
	case <-time.After(15 * time.Second):
		err = errors.New("原生窗口加载超时，请再次打开 YuDesk")
	}
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *windowShell) send(action, address string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	select {
	case <-s.done:
		return errors.New("窗口已停止，请再次打开 YuDesk")
	default:
	}
	if s.closing.Load() {
		return errors.New("窗口正在关闭")
	}
	data, _ := json.Marshal(struct {
		Action string `json:"action"`
		URL    string `json:"url,omitempty"`
	}{action, address})
	result := make(chan error, 1)
	go func() { _, err := s.input.Write(append(data, '\n')); result <- err }()
	select {
	case err := <-result:
		if err != nil {
			return errors.New("窗口通信已断开，请再次打开 YuDesk")
		}
		return nil
	case <-time.After(2 * time.Second):
		_ = s.cmd.Process.Kill()
		_ = s.input.Close()
		return errors.New("窗口未响应，请再次打开 YuDesk")
	}
}

func (s *windowShell) close() {
	if !s.closing.Swap(true) {
		_ = s.input.Close()
	}
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
}

// Caller holds b.mu. There is never more than one live native helper per app.
func (b *appWindow) showShell() error {
	if b.shell != nil {
		select {
		case <-b.shell.done:
		default:
			if err := b.shell.send("show", ""); err == nil {
				return nil
			}
		}
		b.shell.close()
		b.shell = nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	path := filepath.Join(filepath.Dir(exe), "yudesk-window")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("缺少原生窗口组件，请从官网下载并安装 YuDesk 安装包")
	}
	// Identity is checked asynchronously after Show releases the lock.
	var current *windowShell
	closed := func() {
		b.mu.Lock()
		valid := current != nil && b.shell == current && !current.closing.Load()
		callback := b.onClose
		b.mu.Unlock()
		if valid && callback != nil {
			callback()
		}
	}
	current, err = startWindowShell(exec.Command(path), b.url, closed, b.journal)
	if err != nil {
		return err
	}
	b.shell = current
	b.managed.Store(true)
	return nil
}
