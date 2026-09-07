package agentapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yudesk/yudesk/internal/filetransfer"
	"github.com/yudesk/yudesk/internal/protocol"
)

func TestFilePermissionReenableInvalidatesOldIDs(t *testing.T) {
	a := &agent{shareDir: t.TempDir(), fileConsent: true}
	m := filetransfer.New()
	defer m.Close()
	var epoch uint64
	call := func(method string, p any) (map[string]any, error) {
		raw, _ := json.Marshal(p)
		_, meta, err := a.handleFileRequest(m, &epoch, protocol.Message{Method: method, Params: raw})
		return meta, err
	}
	if _, err := call("file_list", map[string]any{"path": ""}); err == nil {
		t.Fatal("unconsented root exposed")
	}
	if err := a.setFilePermission(true); err != nil {
		t.Fatal(err)
	}
	meta, err := call("file_upload_begin", map[string]any{"path": "pending.txt", "size": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.setFilePermission(false); err != nil {
		t.Fatal(err)
	}
	if err := a.setFilePermission(true); err != nil {
		t.Fatal(err)
	}
	if _, err := call("file_upload_commit", map[string]any{"id": meta["id"], "sha256": "none"}); err == nil {
		t.Fatal("old transfer revived")
	}
	entries, _ := os.ReadDir(a.shareDir)
	if len(entries) != 0 {
		t.Fatal("revoked staging file remains")
	}
}

func TestFilePermissionPersistedWithinIdentity(t *testing.T) {
	identity := t.TempDir()
	root := filepath.Join(t.TempDir(), "receive")
	a := &agent{shareDir: root, fileConsent: true, filePermissionPath: filepath.Join(identity, "file-permission.json")}
	if err := a.setFilePermission(true); err != nil {
		t.Fatal(err)
	}
	if a.fileRoot() != root {
		t.Fatal("permission not enabled")
	}
	if err := a.setFilePermission(false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.filePermissionPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct{ Enabled bool }
	if json.Unmarshal(data, &saved) != nil || saved.Enabled || a.fileRoot() != "" {
		t.Fatal("permission not revoked and saved")
	}
}
