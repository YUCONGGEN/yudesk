package filetransfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	m := New()
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return m
}

func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func call(t *testing.T, m *Manager, root, method string, params map[string]any, data []byte) ([]byte, map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if params == nil {
		raw = json.RawMessage("{}")
	}
	out, meta, err := m.Handle(root, method, raw, data)
	if err != nil {
		if out != nil || meta != nil {
			t.Fatal("errors must return nil data and metadata")
		}
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code == "" || apiErr.Message == "" {
			t.Fatalf("error is not a structured API error: %v", err)
		}
		encoded, marshalErr := json.Marshal(apiErr)
		if marshalErr != nil || !bytes.Contains(encoded, []byte(`"code"`)) || !bytes.Contains(encoded, []byte(`"message"`)) {
			t.Fatalf("error is not JSON compatible: %s, %v", encoded, marshalErr)
		}
	} else {
		if meta == nil {
			t.Fatal("successful calls must return metadata")
		}
		if _, err := json.Marshal(meta); err != nil {
			t.Fatalf("metadata is not JSON compatible: %v", err)
		}
		if method != "file_download_chunk" && out != nil {
			t.Fatalf("unexpected binary output for %s", method)
		}
	}
	return out, meta, err
}

func success(t *testing.T, m *Manager, root, method string, params map[string]any, data []byte) ([]byte, map[string]any) {
	t.Helper()
	out, meta, err := call(t, m, root, method, params, data)
	if err != nil {
		t.Fatalf("%s(%v): %v", method, params, err)
	}
	return out, meta
}

func wantError(t *testing.T, m *Manager, root, method string, params map[string]any, data []byte, code string) {
	t.Helper()
	_, _, err := call(t, m, root, method, params, data)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != code {
		t.Fatalf("%s: error = %v, want code %s", method, err, code)
	}
}

func upload(t *testing.T, m *Manager, root, name string, size int64) string {
	t.Helper()
	_, meta := success(t, m, root, "file_upload_begin", map[string]any{"path": name, "size": size}, nil)
	if meta["path"] != name || meta["size"] != size || meta["offset"] != int64(0) || meta["chunkSize"] != ChunkSize {
		t.Fatalf("wrong begin metadata: %#v", meta)
	}
	id, ok := meta["id"].(string)
	if !ok || len(id) != 32 {
		t.Fatalf("invalid opaque id: %v", meta["id"])
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatal(err)
	}
	return id
}

func send(t *testing.T, m *Manager, root, id string, offset int64, data []byte) {
	t.Helper()
	_, meta := success(t, m, root, "file_upload_chunk", map[string]any{"id": id, "offset": offset, "sha256": checksum(data)}, data)
	if meta["id"] != id || meta["offset"] != offset+int64(len(data)) {
		t.Fatalf("wrong chunk acknowledgement: %#v", meta)
	}
}

func put(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, name string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("file %s: got %q, err %v; want %q", name, got, err, want)
	}
}

func absent(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s must not exist, got %v", name, err)
	}
}

func noStaging(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), tempPrefix) {
			t.Errorf("staging file leaked: %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUploadAndDownload(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested", "existing"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := testManager(t)
	content := bytes.Repeat([]byte{0, 255, 19, 4, 10}, ChunkSize/2)
	name := "nested/existing/data.bin"
	id := upload(t, m, root, name, int64(len(content)))
	absent(t, filepath.Join(root, filepath.FromSlash(name)))
	_, list := success(t, m, root, "file_list", map[string]any{"path": "nested/existing"}, nil)
	if len(list["entries"].([]map[string]any)) != 0 {
		t.Fatal("staging file is visible")
	}
	for offset := 0; offset < len(content); {
		end := min(offset+ChunkSize, len(content))
		send(t, m, root, id, int64(offset), content[offset:end])
		offset = end
	}
	absent(t, filepath.Join(root, filepath.FromSlash(name)))
	_, committed := success(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": strings.ToUpper(checksum(content))}, nil)
	want := map[string]any{"id": id, "path": name, "size": int64(len(content)), "sha256": checksum(content)}
	if !reflect.DeepEqual(committed, want) {
		t.Fatalf("commit = %#v; want %#v", committed, want)
	}
	assertFile(t, filepath.Join(root, filepath.FromSlash(name)), content)
	noStaging(t, root)
	wantError(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum(content)}, nil, "unknown_transfer")
	_, meta := success(t, m, root, "file_download_begin", map[string]any{"path": name}, nil)
	id = meta["id"].(string)
	if meta["size"] != int64(len(content)) || meta["chunkSize"] != ChunkSize || meta["offset"] != int64(0) {
		t.Fatalf("download begin = %#v", meta)
	}
	var received []byte
	for {
		offset := int64(len(received))
		chunk, meta := success(t, m, root, "file_download_chunk", map[string]any{"id": id, "offset": offset}, nil)
		if len(chunk) > ChunkSize || meta["sha256"] != checksum(chunk) || meta["offset"] != offset || meta["nextOffset"] != offset+int64(len(chunk)) || meta["size"] != int64(len(chunk)) {
			t.Fatalf("download chunk = %#v", meta)
		}
		received = append(received, chunk...)
		if meta["eof"] == true {
			if meta["fileSHA256"] != checksum(received) {
				t.Fatalf("final SHA-256 = %v", meta["fileSHA256"])
			}
			break
		}
		if _, exists := meta["fileSHA256"]; exists {
			t.Fatal("whole hash must only appear on the final chunk")
		}
	}
	if !bytes.Equal(received, content) || len(m.transfers) != 0 {
		t.Fatal("incorrect download or unreleased transfer")
	}
	wantError(t, m, root, "file_download_chunk", map[string]any{"id": id, "offset": len(content)}, nil, "unknown_transfer")
}

func TestUploadValidationPreservesTransfer(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	id := upload(t, m, root, "retry.bin", 3)
	wantError(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum([]byte("abc"))}, nil, "size_mismatch")
	for _, offset := range []int64{-1, 1, 9223372036854775807} {
		wantError(t, m, root, "file_upload_chunk", map[string]any{"id": id, "offset": offset, "sha256": checksum([]byte("a"))}, []byte("a"), "offset_mismatch")
	}
	wantError(t, m, root, "file_upload_chunk", map[string]any{"id": id, "sha256": checksum([]byte("a"))}, []byte("a"), "invalid_request")
	for _, hash := range []string{"bad", strings.Repeat("z", 64)} {
		wantError(t, m, root, "file_upload_chunk", map[string]any{"id": id, "offset": 0, "sha256": hash}, []byte("a"), "invalid_request")
	}
	wantError(t, m, root, "file_upload_chunk", map[string]any{"id": id, "offset": 0, "sha256": checksum([]byte("b"))}, []byte("a"), "hash_mismatch")
	wantError(t, m, root, "file_upload_chunk", map[string]any{"id": id, "offset": 0, "sha256": checksum(nil)}, nil, "invalid_request")
	wantError(t, m, root, "file_upload_chunk", map[string]any{"id": id, "offset": 0, "sha256": checksum([]byte("abcd"))}, []byte("abcd"), "size_mismatch")
	wantError(t, m, root, "file_upload_chunk", map[string]any{"id": id, "offset": 0, "sha256": checksum(nil)}, make([]byte, ChunkSize+1), "invalid_request")
	if m.transfers[id].offset != 0 {
		t.Fatal("a rejected chunk advanced the upload")
	}
	send(t, m, root, id, 0, []byte("a"))
	wantError(t, m, root, "file_upload_chunk", map[string]any{"id": id, "offset": 0, "sha256": checksum([]byte("a"))}, []byte("a"), "offset_mismatch")
	send(t, m, root, id, 1, []byte("bc"))
	wantError(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": ""}, nil, "invalid_request")
	wantError(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum([]byte("wrong"))}, nil, "hash_mismatch")
	absent(t, filepath.Join(root, "retry.bin"))
	success(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum([]byte("abc"))}, nil)
	assertFile(t, filepath.Join(root, "retry.bin"), []byte("abc"))
	noStaging(t, root)
}

func TestZeroLength(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	id := upload(t, m, root, "empty", 0)
	success(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum(nil)}, nil)
	assertFile(t, filepath.Join(root, "empty"), nil)
	_, begin := success(t, m, root, "file_download_begin", map[string]any{"path": "empty"}, nil)
	id = begin["id"].(string)
	data, end := success(t, m, root, "file_download_chunk", map[string]any{"id": id, "offset": 0}, nil)
	if len(data) != 0 || end["eof"] != true || end["sha256"] != checksum(nil) || end["fileSHA256"] != checksum(nil) || end["size"] != int64(0) || end["nextOffset"] != int64(0) {
		t.Fatalf("zero-length download = %#v, %v", end, data)
	}
	if len(m.transfers) != 0 {
		t.Fatal("empty transfer not released")
	}
	noStaging(t, root)
}

func TestCollisionsNeverOverwrite(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	put(t, filepath.Join(root, "existing"), []byte("original"))
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"existing", "directory"} {
		wantError(t, m, root, "file_upload_begin", map[string]any{"path": name, "size": 0}, nil, "exists")
	}
	a := upload(t, m, root, "race", 1)
	b := upload(t, m, root, "race", 1)
	send(t, m, root, a, 0, []byte("a"))
	send(t, m, root, b, 0, []byte("b"))
	success(t, m, root, "file_upload_commit", map[string]any{"id": a, "sha256": checksum([]byte("a"))}, nil)
	wantError(t, m, root, "file_upload_commit", map[string]any{"id": b, "sha256": checksum([]byte("b"))}, nil, "exists")
	if m.transfers[b] == nil {
		t.Fatal("collision must retain the transfer for cancel/retry")
	}
	success(t, m, root, "file_cancel", map[string]any{"id": b}, nil)
	c := upload(t, m, root, "created-later", 0)
	put(t, filepath.Join(root, "created-later"), []byte("local user file"))
	wantError(t, m, root, "file_upload_commit", map[string]any{"id": c, "sha256": checksum(nil)}, nil, "exists")
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(root, "race"), []byte("a"))
	assertFile(t, filepath.Join(root, "existing"), []byte("original"))
	assertFile(t, filepath.Join(root, "created-later"), []byte("local user file"))
	noStaging(t, root)
}

func TestLimitsCancelAndSessionIsolation(t *testing.T) {
	root := t.TempDir()
	m, other := testManager(t), testManager(t)
	put(t, filepath.Join(root, "download"), []byte("x"))
	for _, size := range []int64{-1, MaxFileSize + 1, 9223372036854775807} {
		wantError(t, m, root, "file_upload_begin", map[string]any{"path": "large", "size": size}, nil, "size_limit")
	}
	wantError(t, m, root, "file_upload_begin", map[string]any{"path": "missing-size"}, nil, "invalid_request")
	a := upload(t, m, root, "a", MaxFileSize)
	if info, err := m.transfers[a].file.Stat(); err != nil || info.Size() != 0 {
		t.Fatalf("begin preallocated file data: %v %v", info, err)
	}
	_, begin := success(t, m, root, "file_download_begin", map[string]any{"path": "download"}, nil)
	b := begin["id"].(string)
	wantError(t, m, root, "file_upload_begin", map[string]any{"path": "c", "size": 0}, nil, "limit")
	wantError(t, m, root, "file_download_begin", map[string]any{"path": "download"}, nil, "limit")
	wantError(t, other, root, "file_cancel", map[string]any{"id": a}, nil, "unknown_transfer")
	c := upload(t, other, root, "c", 0)
	if a == b || a == c || b == c {
		t.Fatal("identifiers reused")
	}
	wantError(t, m, root, "file_download_chunk", map[string]any{"id": a, "offset": 0}, nil, "wrong_transfer")
	wantError(t, m, root, "file_upload_commit", map[string]any{"id": b, "sha256": checksum(nil)}, nil, "wrong_transfer")
	_, cancelled := success(t, m, root, "file_cancel", map[string]any{"id": a}, nil)
	if cancelled["cancelled"] != true || cancelled["id"] != a {
		t.Fatalf("cancel = %#v", cancelled)
	}
	wantError(t, m, root, "file_cancel", map[string]any{"id": a}, nil, "unknown_transfer")
	d := upload(t, m, root, "d", 0)
	success(t, m, root, "file_cancel", map[string]any{"id": d}, nil)
	success(t, m, root, "file_cancel", map[string]any{"id": b}, nil)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := other.transfers[c].file.Stat(); err != nil {
		t.Fatal("another manager's staging handle was closed", err)
	}
	success(t, other, root, "file_upload_commit", map[string]any{"id": c, "sha256": checksum(nil)}, nil)
	assertFile(t, filepath.Join(root, "download"), []byte("x"))
	noStaging(t, root)
}

func TestRootChangesDisableResetAndClose(t *testing.T) {
	for _, scenario := range []string{"change", "disable", "invalid-root", "reset", "close"} {
		t.Run(scenario, func(t *testing.T) {
			root, next := t.TempDir(), t.TempDir()
			m := testManager(t)
			put(t, filepath.Join(root, "source"), []byte("keep"))
			id := upload(t, m, root, "partial", 5)
			send(t, m, root, id, 0, []byte("p"))
			stage := m.transfers[id].file
			_, begin := success(t, m, root, "file_download_begin", map[string]any{"path": "source"}, nil)
			downloadID := begin["id"].(string)
			downloadFile := m.transfers[downloadID].file
			switch scenario {
			case "change":
				if _, _, err := m.Handle(next, "file_list", json.RawMessage("invalid"), nil); err == nil {
					t.Fatal("invalid request accepted")
				}
			case "disable":
				wantError(t, m, "", "file_upload_chunk", map[string]any{"id": id}, []byte("ignored"), "disabled")
			case "invalid-root":
				wantError(t, m, filepath.Join(next, "missing"), "file_list", nil, nil, "invalid_root")
			case "reset":
				for i := 0; i < 2; i++ {
					if err := m.Reset(); err != nil {
						t.Fatal(err)
					}
				}
			case "close":
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
				if err := m.Reset(); err != nil {
					t.Fatal(err)
				}
				wantError(t, m, root, "file_list", nil, nil, "closed")
			}
			for _, f := range []*os.File{stage, downloadFile} {
				var b [1]byte
				if _, err := f.ReadAt(b[:], 0); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("handle is still open: %v", err)
				}
			}
			if len(m.transfers) != 0 {
				t.Fatal("old transfers survived invalidation")
			}
			if scenario != "close" {
				success(t, m, root, "file_list", nil, nil)
				wantError(t, m, root, "file_cancel", map[string]any{"id": id}, nil, "unknown_transfer")
				wantError(t, m, root, "file_cancel", map[string]any{"id": downloadID}, nil, "unknown_transfer")
				newID := upload(t, m, root, "new", 0)
				success(t, m, root, "file_cancel", map[string]any{"id": newID}, nil)
			}
			assertFile(t, filepath.Join(root, "source"), []byte("keep"))
			absent(t, filepath.Join(root, "partial"))
			noStaging(t, root)
		})
	}
}

func TestIdleExpiryAndRefresh(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	put(t, filepath.Join(root, "source"), []byte("read"))
	a := upload(t, m, root, "a", 2)
	_, begin := success(t, m, root, "file_download_begin", map[string]any{"path": "source"}, nil)
	b := begin["id"].(string)
	closedDownload := m.transfers[b].file
	now = now.Add(idleTimeout - time.Second)
	send(t, m, root, a, 0, []byte("a"))
	now = now.Add(time.Second)
	wantError(t, m, root, "unknown", nil, nil, "unknown_method")
	if m.transfers[a] == nil || m.transfers[b] != nil {
		t.Fatal("expiry did not respect successful per-transfer activity")
	}
	var bRead [1]byte
	if _, err := closedDownload.ReadAt(bRead[:], 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("expired download handle is open: %v", err)
	}
	now = now.Add(idleTimeout - 2*time.Second)
	wantError(t, m, root, "file_upload_chunk", map[string]any{"id": a, "offset": 0}, []byte("a"), "offset_mismatch")
	now = now.Add(time.Second)
	if err := m.Sweep(); err != nil {
		t.Fatal(err)
	}
	if len(m.transfers) != 0 {
		t.Fatal("bad requests kept an idle upload alive")
	}
	noStaging(t, root)
	c := upload(t, m, root, "c", 0)
	now = now.Add(idleTimeout)
	if _, _, err := m.Handle(root, "file_list", json.RawMessage("not JSON"), nil); err == nil {
		t.Fatal("malformed request accepted")
	}
	if m.transfers[c] != nil {
		t.Fatal("malformed request failed to expire transfers")
	}
	noStaging(t, root)
}

func TestRejectUnsafePaths(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	paths := []string{
		"../escape", "a/../escape", "a/../../escape", "/absolute", "//server/share", "a//b", "a/./b", "a/..", "./file", "a/",
		`C:\file`, "C:/file", "C:file", `\\server\share\file`, `\\?\C:\file`, `a\b`, `..\file`,
		"file:stream", "file::$DATA", "dir:stream/file", "file.", "file ", "dir./file", "dir /file",
		"NUL", "nul.txt", "CON", "con.json", "AUX", "prn", "COM1", "com9.log", "LPT1.txt", "lpt9",
		"COM¹.txt", "LPT²", "COM³", "CONIN$", "CONOUT$", "CLOCK$", "CON .txt", "dir/NUL.txt",
		"bad?name", "bad*name", "bad|name", "bad<name", "bad>name", "bad\"name", "bad\x00name", "bad\nname", "bad\x7fname",
		"YUDESK~1", tempPrefix + "foreign", strings.ToUpper(tempPrefix) + "FOREIGN", "dir/" + tempPrefix + "secret", strings.Repeat("a", 4097),
	}
	for _, name := range paths {
		t.Run(fmt.Sprintf("%q", name[:min(len(name), 80)]), func(t *testing.T) {
			for _, method := range []string{"file_list", "file_upload_begin", "file_download_begin"} {
				params := map[string]any{"path": name}
				if method == "file_upload_begin" {
					params["size"] = 0
				}
				wantError(t, m, root, method, params, nil, "invalid_path")
			}
		})
	}
	for _, name := range []string{"", ".", ".."} {
		wantError(t, m, root, "file_upload_begin", map[string]any{"path": name, "size": 0}, nil, "invalid_path")
		wantError(t, m, root, "file_download_begin", map[string]any{"path": name}, nil, "invalid_path")
	}
	for _, name := range []string{"COM10", "lpt0", ".ordinary", "中文文件", "safe name.txt"} {
		id := upload(t, m, root, name, 0)
		success(t, m, root, "file_cancel", map[string]any{"id": id}, nil)
	}
	for _, name := range []string{"", "."} {
		_, meta := success(t, m, root, "file_list", map[string]any{"path": name}, nil)
		if meta["path"] != "." {
			t.Fatalf("noncanonical root path: %v", meta)
		}
	}
	wantError(t, m, root, "file_upload_begin", map[string]any{"path": "missing/file", "size": 0}, nil, "invalid_path")
	noStaging(t, root)
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestSymlinksAndInternalAliases(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	m := testManager(t)
	put(t, filepath.Join(outside, "secret"), []byte("private"))
	put(t, filepath.Join(root, "public"), []byte("public"))
	symlink(t, outside, filepath.Join(root, "outside"))
	symlink(t, filepath.Join(outside, "secret"), filepath.Join(root, "escape"))
	symlink(t, "public", filepath.Join(root, "alias"))
	symlink(t, filepath.Join(outside, "missing"), filepath.Join(root, "dangling"))
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	symlink(t, "../..", filepath.Join(root, "sub", "up"))
	for _, name := range []string{"escape", "alias", "dangling"} {
		wantError(t, m, root, "file_download_begin", map[string]any{"path": name}, nil, "not_file")
		wantError(t, m, root, "file_upload_begin", map[string]any{"path": name, "size": 0}, nil, "exists")
	}
	for _, name := range []string{"outside", "sub/up"} {
		wantError(t, m, root, "file_list", map[string]any{"path": name}, nil, "invalid_path")
		wantError(t, m, root, "file_download_begin", map[string]any{"path": name + "/secret"}, nil, "invalid_path")
		wantError(t, m, root, "file_upload_begin", map[string]any{"path": name + "/new", "size": 0}, nil, "invalid_path")
	}
	id := upload(t, m, root, "new", 0)
	stage := m.transfers[id].temp
	symlink(t, stage, filepath.Join(root, "stage-alias"))
	wantError(t, m, root, "file_download_begin", map[string]any{"path": stage}, nil, "invalid_path")
	wantError(t, m, root, "file_download_begin", map[string]any{"path": "stage-alias"}, nil, "not_file")
	_, list := success(t, m, root, "file_list", nil, nil)
	for _, entry := range list["entries"].([]map[string]any) {
		if entry["name"] != "public" && entry["name"] != "sub" {
			t.Fatalf("nonpublic entry listed: %v", entry)
		}
	}
	success(t, m, root, "file_cancel", map[string]any{"id": id}, nil)
	assertFile(t, filepath.Join(outside, "secret"), []byte("private"))
	absent(t, filepath.Join(outside, "new"))
	absent(t, filepath.Join(outside, "missing"))
}

func TestPinnedDirectorySurvivesReplacement(t *testing.T) {
	for _, finish := range []string{"commit", "cancel"} {
		t.Run(finish, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			m := testManager(t)
			if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
			id := upload(t, m, root, "nested/new", 0)
			destinationDir := "moved"
			if err := os.Rename(filepath.Join(root, "nested"), filepath.Join(root, "moved")); err != nil {
				if runtime.GOOS != "windows" || !errors.Is(err, os.ErrPermission) {
					t.Fatal(err)
				}
				// Windows can deny renaming a pinned directory outright. Verify
				// that the operation still targets the protected original path.
				destinationDir = "nested"
			} else {
				symlink(t, outside, filepath.Join(root, "nested"))
			}
			if finish == "commit" {
				success(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum(nil)}, nil)
				assertFile(t, filepath.Join(root, destinationDir, "new"), nil)
			} else {
				success(t, m, root, "file_cancel", map[string]any{"id": id}, nil)
				absent(t, filepath.Join(root, destinationDir, "new"))
			}
			absent(t, filepath.Join(outside, "new"))
			noStaging(t, root)
		})
	}
}

func TestCleanupNeverDeletesForeignFiles(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	foreign := filepath.Join(root, tempPrefix+"preexisting")
	put(t, foreign, []byte("unowned"))
	id := upload(t, m, root, "new", 0)
	stageName := filepath.Join(root, m.transfers[id].temp)
	if err := os.Rename(stageName, filepath.Join(root, "moved-by-owner")); err != nil {
		t.Fatal(err)
	}
	put(t, stageName, []byte("replacement"))
	wantError(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum(nil)}, nil, "io_error")
	wantError(t, m, root, "file_cancel", map[string]any{"id": id}, nil, "io_error")
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	assertFile(t, stageName, []byte("replacement"))
	assertFile(t, foreign, []byte("unowned"))
	assertFile(t, filepath.Join(root, "moved-by-owner"), nil)
	absent(t, filepath.Join(root, "new"))
}

func TestStagedBytesAreVerified(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	id := upload(t, m, root, "new", 3)
	send(t, m, root, id, 0, []byte("abc"))
	tx := m.transfers[id]
	if _, err := tx.file.WriteAt([]byte("xyz"), 0); err != nil {
		t.Fatal(err)
	}
	wantError(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum([]byte("abc"))}, nil, "hash_mismatch")
	absent(t, filepath.Join(root, "new"))
	if err := tx.file.Truncate(4); err != nil {
		t.Fatal(err)
	}
	wantError(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum([]byte("xyz"))}, nil, "size_mismatch")
	success(t, m, root, "file_cancel", map[string]any{"id": id}, nil)
	noStaging(t, root)
}

func TestWriteFailureCanBeRetried(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	id := upload(t, m, root, "retry", 1)
	tx := m.transfers[id]
	if err := tx.file.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	tx.file, err = tx.parent.Open(tx.temp)
	if err != nil {
		t.Fatal(err)
	}
	wantError(t, m, root, "file_upload_chunk", map[string]any{"id": id, "offset": 0, "sha256": checksum([]byte("x"))}, []byte("x"), "io_error")
	if tx.offset != 0 {
		t.Fatal("write failure advanced offset")
	}
	if err := tx.file.Close(); err != nil {
		t.Fatal(err)
	}
	tx.file, err = tx.parent.OpenFile(tx.temp, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	send(t, m, root, id, 0, []byte("x"))
	success(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum([]byte("x"))}, nil)
	assertFile(t, filepath.Join(root, "retry"), []byte("x"))
}

func TestDownloadOrderingChangesAndFolders(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	wantError(t, m, root, "file_download_begin", map[string]any{"path": "directory"}, nil, "not_file")
	wantError(t, m, root, "file_download_begin", map[string]any{"path": "missing"}, nil, "not_found")
	content := bytes.Repeat([]byte("x"), ChunkSize+1)
	put(t, filepath.Join(root, "source"), content)
	_, begin := success(t, m, root, "file_download_begin", map[string]any{"path": "source"}, nil)
	id := begin["id"].(string)
	wantError(t, m, root, "file_download_chunk", map[string]any{"id": id}, nil, "invalid_request")
	for _, offset := range []int64{-1, 1} {
		wantError(t, m, root, "file_download_chunk", map[string]any{"id": id, "offset": offset}, nil, "offset_mismatch")
	}
	data, _ := success(t, m, root, "file_download_chunk", map[string]any{"id": id, "offset": 0}, nil)
	if !bytes.Equal(data, content[:ChunkSize]) {
		t.Fatal("bad data after rejected offsets")
	}
	wantError(t, m, root, "file_download_chunk", map[string]any{"id": id, "offset": 0}, nil, "offset_mismatch")
	data, final := success(t, m, root, "file_download_chunk", map[string]any{"id": id, "offset": ChunkSize}, nil)
	if len(data) != 1 || final["fileSHA256"] != checksum(content) {
		t.Fatal("rejected offsets polluted whole-file hash")
	}
	for _, change := range []string{"grow", "shrink", "same-size"} {
		t.Run(change, func(t *testing.T) {
			put(t, filepath.Join(root, "source"), content)
			_, begin := success(t, m, root, "file_download_begin", map[string]any{"path": "source"}, nil)
			id := begin["id"].(string)
			success(t, m, root, "file_download_chunk", map[string]any{"id": id, "offset": 0}, nil)
			switch change {
			case "grow":
				put(t, filepath.Join(root, "source"), append(content, 'y'))
			case "shrink":
				put(t, filepath.Join(root, "source"), []byte("short"))
			case "same-size":
				put(t, filepath.Join(root, "source"), bytes.Repeat([]byte("z"), len(content)))
				modified := m.transfers[id].info.ModTime().Add(time.Hour)
				if err := os.Chtimes(filepath.Join(root, "source"), modified, modified); err != nil {
					t.Fatal(err)
				}
			}
			wantError(t, m, root, "file_download_chunk", map[string]any{"id": id, "offset": ChunkSize}, nil, "file_changed")
			if m.transfers[id].offset != ChunkSize {
				t.Fatal("failed read advanced offset")
			}
			success(t, m, root, "file_cancel", map[string]any{"id": id}, nil)
		})
	}
}

func TestListMetadataAndLimit(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	_, empty := success(t, m, root, "file_list", nil, nil)
	if empty["path"] != "." || empty["maxFileSize"] != MaxFileSize || empty["chunkSize"] != ChunkSize || empty["truncated"] != false {
		t.Fatalf("list metadata = %#v", empty)
	}
	encoded, _ := json.Marshal(empty)
	if !bytes.Contains(encoded, []byte(`"entries":[]`)) {
		t.Fatalf("empty entries must be an array: %s", encoded)
	}
	put(t, filepath.Join(root, ".ordinary"), []byte("123"))
	put(t, filepath.Join(root, tempPrefix+"foreign"), []byte("hidden"))
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, listed := success(t, m, root, "file_list", nil, nil)
	wantEntries := []map[string]any{
		{"name": ".ordinary", "directory": false, "size": int64(3)},
		{"name": "nested", "directory": true, "size": int64(0)},
	}
	if !reflect.DeepEqual(listed["entries"], wantEntries) {
		t.Fatalf("entries = %#v", listed["entries"])
	}
	for i := 0; i < maxEntries+1; i++ {
		put(t, filepath.Join(root, "nested", fmt.Sprintf("file-%04d", i)), nil)
	}
	_, limited := success(t, m, root, "file_list", map[string]any{"path": "nested"}, nil)
	entries := limited["entries"].([]map[string]any)
	if len(entries) != maxEntries || limited["truncated"] != true || limited["path"] != "nested" {
		t.Fatalf("listing not bounded: entries=%d, truncated=%v", len(entries), limited["truncated"])
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e["name"].(string)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatal("entries not sorted")
	}
	wantError(t, m, root, "file_list", map[string]any{"path": ".ordinary"}, nil, "invalid_path")
	assertFile(t, filepath.Join(root, tempPrefix+"foreign"), []byte("hidden"))
}

func TestMalformedRequestsPreserveSession(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	id := upload(t, m, root, "valid", 0)
	for _, raw := range []string{"null", "[]", "123", "\"string\"", "{", "{} {}", `{"size":1.5,"path":"x"}`, `{"size":null,"path":"x"}`, `{"size":9223372036854775808,"path":"x"}`, `{"size":"1","path":"x"}`, `{"size":0,"path":"x","unknown":true}`, strings.Repeat(" ", maxParams+1)} {
		_, _, err := m.Handle(root, "file_upload_begin", json.RawMessage(raw), nil)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != "invalid_request" {
			t.Fatalf("malformed %q accepted: %v", raw[:min(len(raw), 80)], err)
		}
	}
	wantError(t, m, root, "file_list", nil, []byte("unexpected"), "invalid_request")
	wantError(t, m, root, "file_download_chunk", map[string]any{"id": id, "offset": 0}, []byte("unexpected"), "invalid_request")
	wantError(t, m, root, "not-a-method", nil, nil, "unknown_method")
	wantError(t, m, root, "file_cancel", map[string]any{"id": "untrusted"}, nil, "unknown_transfer")
	success(t, m, root, "file_upload_commit", map[string]any{"id": id, "sha256": checksum(nil)}, nil)
	assertFile(t, filepath.Join(root, "valid"), nil)
}

func TestConcurrentBeginLimit(t *testing.T) {
	root := t.TempDir()
	m := testManager(t)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, _ := json.Marshal(map[string]any{"path": fmt.Sprintf("file-%d", i), "size": 0})
			_, _, err := m.Handle(root, "file_upload_begin", raw, nil)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != "limit" {
				t.Fatal(err)
			}
		}
	}
	if accepted != maxTransfers {
		t.Fatalf("accepted %d transfers concurrently", accepted)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	noStaging(t, root)
}
