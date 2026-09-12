package viewerapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A small, separate journal deliberately does NOT capture the standard logger:
// legacy log messages can contain UI URLs, pairing secrets or peer-supplied text.
// Only fixed event names and classified errors are persisted here.
type lifecycleJournal struct {
	mu          sync.Mutex
	file        *os.File
	path        string
	size, limit int64
}

func openLifecycleJournal(directory string) (*lifecycleJournal, error) {
	directory = filepath.Join(directory, "logs")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "lifecycle.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &lifecycleJournal{file: f, path: path, size: info.Size(), limit: 256 << 10}, nil
}

func lifecycleErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "truncated_stream"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "network_timeout"
		}
		return "network_error"
	}
	return "other_error"
}

func (j *lifecycleJournal) record(event string, err error) {
	if j == nil {
		return
	}
	switch event {
	case "app_started", "app_stopped", "window_closed", "exit_clicked", "tray_exit", "device_stopped", "service_restart",
		"session_disconnected", "session_ended", "session_connect_failed", "window_monitor_lost", "window_monitor_restored",
		"window_target_closed", "window_process_exited", "local_server_stopped", "browser_watch_closed",
		"window_closed_to_tray", "window_close_to_tray_failed":
	default:
		event = "unknown_event"
	}
	line := fmt.Sprintf("%s pid=%d event=%s error=%s\n", time.Now().Format(time.RFC3339Nano), os.Getpid(), event, lifecycleErrorClass(err))
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return
	}
	if j.size+int64(len(line)) > j.limit {
		// Two bounded files at most. Rotation never affects app availability.
		if err := os.Remove(j.path + ".1"); err != nil && !os.IsNotExist(err) {
			return
		}
		if err := j.file.Close(); err != nil {
			j.file = nil
			return
		}
		j.file = nil
		if err := os.Rename(j.path, j.path+".1"); err != nil {
			return
		}
		f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return
		}
		j.file, j.size = f, 0
	}
	n, _ := io.WriteString(j.file, line)
	j.size += int64(n)
	_ = j.file.Sync()
}

func (j *lifecycleJournal) Close() {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file != nil {
		_ = j.file.Close()
		j.file = nil
	}
}
