package viewerapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/yudesk/yudesk/internal/filetransfer"
	"github.com/yudesk/yudesk/internal/protocol"
)

type fileRequest func(context.Context, string, any, []byte) (protocol.Message, error)
type uploadState struct {
	size, offset int64
	digest       hash.Hash
	used         time.Time
}
type fileBridge struct {
	request fileRequest
	allowed bool
	mu      sync.Mutex // Serializes file uploads only; never held by input/frames.
	uploads map[string]*uploadState
}

func registerFiles(mux *http.ServeMux, request fileRequest, allowed bool) *fileBridge {
	b := &fileBridge{request: request, allowed: allowed, uploads: make(map[string]*uploadState)}
	mux.HandleFunc("/api/files", b.list)
	mux.HandleFunc("/api/files/upload/begin", b.begin)
	mux.HandleFunc("/api/files/upload/chunk", b.chunk)
	mux.HandleFunc("/api/files/upload/commit", b.commit)
	mux.HandleFunc("/api/files/cancel", b.cancel)
	mux.HandleFunc("/api/download", b.download)
	return b
}

func (b *fileBridge) permit(w http.ResponseWriter, r *http.Request, method string) bool {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != method {
		http.Error(w, "method not allowed", 405)
		return false
	}
	if !b.allowed {
		http.Error(w, "文件传输需要控制模式及新版被控端，并在被控端开启文件授权", 403)
		return false
	}
	return true
}
func (b *fileBridge) call(ctx context.Context, method string, params any, data []byte) (protocol.Message, error) {
	timeout := 20 * time.Second
	if method == "file_upload_commit" {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return b.request(ctx, method, params, data)
}
func fileJSON(w http.ResponseWriter, meta map[string]any, err error) {
	if err != nil {
		http.Error(w, "文件操作失败："+err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meta)
}
func metaInt(meta map[string]any, key string) (int64, bool) {
	v, ok := meta[key].(float64)
	if ok && v >= 0 && v <= filetransfer.MaxFileSize && v == float64(int64(v)) {
		return int64(v), true
	}
	return 0, false
}
func (b *fileBridge) list(w http.ResponseWriter, r *http.Request) {
	if !b.permit(w, r, http.MethodGet) {
		return
	}
	m, err := b.call(r.Context(), "file_list", map[string]any{"path": r.URL.Query().Get("path")}, nil)
	fileJSON(w, m.Meta, err)
}
func (b *fileBridge) begin(w http.ResponseWriter, r *http.Request) {
	if !b.permit(w, r, http.MethodPost) {
		return
	}
	var p struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if !decodeJSON(w, r, &p, 8192) {
		return
	}
	if p.Size < 0 || p.Size > filetransfer.MaxFileSize {
		http.Error(w, "单个文件不能超过 1 GiB", 400)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, state := range b.uploads {
		if time.Since(state.used) > 2*time.Minute {
			delete(b.uploads, id)
		}
	}
	if len(b.uploads) >= 2 {
		http.Error(w, "已有文件正在传输，请先完成或取消", 409)
		return
	}
	m, err := b.call(r.Context(), "file_upload_begin", p, nil)
	if err == nil {
		id, _ := m.Meta["id"].(string)
		if id == "" {
			err = errors.New("被控端未返回传输编号")
		} else {
			b.uploads[id] = &uploadState{size: p.Size, digest: sha256.New(), used: time.Now()}
		}
	}
	fileJSON(w, m.Meta, err)
}
func (b *fileBridge) chunk(w http.ResponseWriter, r *http.Request) {
	if !b.permit(w, r, http.MethodPost) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, filetransfer.ChunkSize))
	if err != nil || len(data) == 0 {
		http.Error(w, "文件块无效或过大", 400)
		return
	}
	id := r.URL.Query().Get("id")
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil {
		http.Error(w, "偏移无效", 400)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.uploads[id]
	if s == nil || offset != s.offset || int64(len(data)) > s.size-s.offset {
		http.Error(w, "传输已失效或顺序错误，请重新上传", 409)
		return
	}
	digest := sha256.Sum256(data)
	m, err := b.call(r.Context(), "file_upload_chunk", map[string]any{"id": id, "offset": offset, "sha256": hex.EncodeToString(digest[:])}, data)
	if err == nil {
		next, valid := metaInt(m.Meta, "offset")
		if !valid || next != offset+int64(len(data)) {
			err = errors.New("被控端确认的文件偏移不一致")
		} else {
			s.digest.Write(data)
			s.offset = next
			s.used = time.Now()
		}
	}
	// A lost acknowledgement has an ambiguous remote offset. Never retry it
	// automatically or label the file complete; the UI cancels and restarts.
	fileJSON(w, m.Meta, err)
}
func (b *fileBridge) commit(w http.ResponseWriter, r *http.Request) {
	if !b.permit(w, r, http.MethodPost) {
		return
	}
	var p struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &p, 4096) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.uploads[p.ID]
	if s == nil || s.offset != s.size {
		http.Error(w, "文件尚未完整传输", 409)
		return
	}
	digest := hex.EncodeToString(s.digest.Sum(nil))
	m, err := b.call(r.Context(), "file_upload_commit", map[string]any{"id": p.ID, "sha256": digest}, nil)
	if err == nil && m.Meta["sha256"] != digest {
		err = errors.New("完整文件校验失败")
	}
	if err == nil {
		delete(b.uploads, p.ID)
	}
	fileJSON(w, m.Meta, err)
}
func (b *fileBridge) cancel(w http.ResponseWriter, r *http.Request) {
	if !b.permit(w, r, http.MethodPost) {
		return
	}
	var p struct {
		ID string `json:"id"`
	}
	if !decodeJSON(w, r, &p, 4096) {
		return
	}
	b.mu.Lock()
	delete(b.uploads, p.ID)
	b.mu.Unlock()
	m, err := b.call(r.Context(), "file_cancel", p, nil)
	fileJSON(w, m.Meta, err)
}
func (b *fileBridge) download(w http.ResponseWriter, r *http.Request) {
	if !b.permit(w, r, http.MethodGet) {
		return
	}
	name := r.URL.Query().Get("path")
	m, err := b.call(r.Context(), "file_download_begin", map[string]any{"path": name}, nil)
	if err != nil {
		fileJSON(w, nil, err)
		return
	}
	id, _ := m.Meta["id"].(string)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = b.request(ctx, "file_cancel", map[string]any{"id": id}, nil)
	}()
	size, valid := metaInt(m.Meta, "size")
	if id == "" || !valid {
		http.Error(w, "文件大小无效", 502)
		return
	}
	digest := sha256.New()
	var offset int64
	started := false
	for {
		chunk, err := b.call(r.Context(), "file_download_chunk", map[string]any{"id": id, "offset": offset}, nil)
		start, validStart := metaInt(chunk.Meta, "offset")
		next, validNext := metaInt(chunk.Meta, "nextOffset")
		eof, _ := chunk.Meta["eof"].(bool)
		sum := sha256.Sum256(chunk.Data)
		if err == nil && (!validStart || !validNext || start != offset || next != offset+int64(len(chunk.Data)) || next > size || len(chunk.Data) > filetransfer.ChunkSize || eof != (next == size) || (!eof && len(chunk.Data) == 0) || chunk.Meta["sha256"] != hex.EncodeToString(sum[:])) {
			err = errors.New("下载数据块校验失败")
		}
		if err == nil {
			digest.Write(chunk.Data)
			if eof && chunk.Meta["fileSHA256"] != hex.EncodeToString(digest.Sum(nil)) {
				err = errors.New("下载完整文件校验失败")
			}
		}
		if err != nil {
			if !started {
				fileJSON(w, nil, err)
				return
			}
			// Truncate the HTTP response, never append an error to user bytes or
			// let an incomplete response masquerade as a successful download.
			panic(http.ErrAbortHandler)
		}
		if !started {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(name)}))
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			started = true
		}
		controller := http.NewResponseController(w)
		_ = controller.SetWriteDeadline(time.Now().Add(20 * time.Second))
		_, writeErr := w.Write(chunk.Data)
		if writeErr == nil {
			writeErr = controller.Flush()
		}
		_ = controller.SetWriteDeadline(time.Time{})
		if writeErr != nil {
			return
		}
		if eof {
			return
		}
		offset = next
	}
}
