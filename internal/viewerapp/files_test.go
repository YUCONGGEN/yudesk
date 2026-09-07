package viewerapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yudesk/yudesk/internal/filetransfer"
	"github.com/yudesk/yudesk/internal/protocol"
)

func testFileBridge(t *testing.T, allowed bool, mutate func(string, *protocol.Message)) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	manager := filetransfer.New()
	t.Cleanup(func() { manager.Close() })
	request := func(ctx context.Context, method string, p any, data []byte) (protocol.Message, error) {
		if err := ctx.Err(); err != nil {
			return protocol.Message{}, err
		}
		raw, _ := json.Marshal(p)
		out, meta, err := manager.Handle(root, method, raw, data)
		// Exercise the exact JSON numeric representations seen on the wire.
		encoded, _ := json.Marshal(meta)
		var wireMeta map[string]any
		_ = json.Unmarshal(encoded, &wireMeta)
		m := protocol.Response("", out, wireMeta)
		if mutate != nil {
			mutate(method, &m)
		}
		return m, err
	}
	mux := http.NewServeMux()
	registerFiles(mux, request, allowed)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, root
}
func fileHTTP(t *testing.T, server *httptest.Server, method, endpoint string, body []byte, wantStatus int) []byte {
	t.Helper()
	req, _ := http.NewRequest(method, server.URL+endpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s status %d want %d: %s", endpoint, resp.StatusCode, wantStatus, data)
	}
	return data
}
func TestFileBridgeUploadDownloadIntegrityAndNoOverwrite(t *testing.T) {
	server, root := testFileBridge(t, true, nil)
	for _, size := range []int{0, 1, filetransfer.ChunkSize*3 + 27} {
		name := fmt.Sprintf("中文-%d.bin", size)
		want := bytes.Repeat([]byte{0, 1, 2, 255}, (size+3)/4)[:size]
		params, _ := json.Marshal(map[string]any{"path": name, "size": size})
		var begin struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(fileHTTP(t, server, "POST", "/api/files/upload/begin", params, 200), &begin)
		for offset := 0; offset < size; {
			end := min(offset+filetransfer.ChunkSize, size)
			fileHTTP(t, server, "POST", fmt.Sprintf("/api/files/upload/chunk?id=%s&offset=%d", begin.ID, offset), want[offset:end], 200)
			offset = end
		}
		id, _ := json.Marshal(map[string]string{"id": begin.ID})
		fileHTTP(t, server, "POST", "/api/files/upload/commit", id, 200)
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !bytes.Equal(want, got) {
			t.Fatalf("uploaded contents differ: %v", err)
		}
		download := fileHTTP(t, server, "GET", "/api/download?path="+url.QueryEscape(name), nil, 200)
		if !bytes.Equal(want, download) {
			t.Fatal("download differs")
		}
		fileHTTP(t, server, "POST", "/api/files/upload/begin", params, 502)
	}
	listed := fileHTTP(t, server, "GET", "/api/files", nil, 200)
	if !bytes.Contains(listed, []byte("中文-")) {
		t.Fatal("file listing missing received file")
	}
}
func TestFileBridgeCancellationAndValidation(t *testing.T) {
	server, root := testFileBridge(t, true, nil)
	var begin struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(fileHTTP(t, server, "POST", "/api/files/upload/begin", []byte(`{"path":"cancel.bin","size":3}`), 200), &begin)
	fileHTTP(t, server, "POST", "/api/files/upload/chunk?id="+begin.ID+"&offset=2", []byte("a"), 409)
	fileHTTP(t, server, "POST", "/api/files/upload/chunk?id="+begin.ID+"&offset=0", make([]byte, filetransfer.ChunkSize+1), 400)
	id, _ := json.Marshal(map[string]string{"id": begin.ID})
	fileHTTP(t, server, "POST", "/api/files/upload/commit", id, 409)
	fileHTTP(t, server, "POST", "/api/files/cancel", id, 200)
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("cancel left staged file")
	}
	fileHTTP(t, server, "POST", "/api/files/upload/begin", []byte(`{"path":"../escape.bin","size":1}`), 502)
	fileHTTP(t, server, "POST", "/api/files/upload/begin", []byte(`{"path":"big.bin","size":1073741825}`), 400)
	denied, _ := testFileBridge(t, false, nil)
	for _, endpoint := range []string{"/api/files", "/api/download?path=a"} {
		fileHTTP(t, denied, "GET", endpoint, nil, 403)
	}
	fileHTTP(t, denied, "POST", "/api/files/upload/begin", []byte(`{}`), 403)
}
func TestDownloadRejectsCorruptionInsteadOfSavingBadBytes(t *testing.T) {
	server, root := testFileBridge(t, true, func(method string, m *protocol.Message) {
		if method == "file_download_chunk" && len(m.Data) > 0 {
			m.Data[0] ^= 0xff
		}
	})
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte("valid"), 0600); err != nil {
		t.Fatal(err)
	}
	result := fileHTTP(t, server, "GET", "/api/download?path=data.bin", nil, 502)
	if !strings.Contains(string(result), "校验失败") {
		t.Fatal("missing integrity error")
	}
}
func TestMidDownloadErrorTruncatesHTTPInsteadOfAppendingErrorText(t *testing.T) {
	server, root := testFileBridge(t, true, func(method string, m *protocol.Message) {
		if method == "file_download_chunk" && m.Meta["offset"] == float64(filetransfer.ChunkSize) {
			m.Meta["sha256"] = "corrupt"
		}
	})
	if err := os.WriteFile(filepath.Join(root, "large.bin"), make([]byte, filetransfer.ChunkSize*2), 0600); err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Get(server.URL + "/api/download?path=large.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err == nil || len(data) != filetransfer.ChunkSize {
		t.Fatalf("corruption reported as complete: bytes=%d err=%v", len(data), err)
	}
}
