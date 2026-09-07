// Package filetransfer implements a bounded file-transfer session rooted in one
// configured directory. It requires Go 1.25 or newer (os.Root.Link).
package filetransfer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ChunkSize   = 32 << 10
	MaxFileSize = 1 << 30

	maxTransfers = 2
	maxEntries   = 1000
	maxScanned   = 10000
	maxParams    = 16 << 10
	idleTimeout  = 2 * time.Minute
	tempPrefix   = ".yudesk-filetransfer-"
)

// Error is safe to marshal as JSON. Code is stable; Message describes the error.
// Handle returns nil data and metadata on errors. Callers may use errors.As to
// retrieve this type without treating a rejected request as a session failure.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func failure(code, message string) error { return &Error{Code: code, Message: message} }

type transfer struct {
	id       string
	path     string
	size     int64
	offset   int64
	lastUsed time.Time
	file     *os.File
	info     os.FileInfo
	digest   hash.Hash // Downloads hash the bytes actually returned, in order.
	parent   *os.Root  // Upload destination directory, pinned across renames.
	temp     string    // Nonempty only for uploads; never the destination name.
}

// Manager belongs to exactly one connection/session. Calls are serialized and
// safe from multiple goroutines. Construct it with New and Close on disconnect.
// It has no background goroutines: expiry runs on Handle or optional Sweep calls.
type Manager struct {
	mu        sync.Mutex
	rootName  string
	root      *os.Root
	transfers map[string]*transfer
	now       func() time.Time
	closed    bool
}

func New() *Manager {
	return &Manager{transfers: make(map[string]*transfer), now: time.Now}
}

// Close permanently closes the session, all its handles, and owned staging files.
// It is idempotent and never removes an upload's published destination.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return m.reset()
}

// Reset invalidates all IDs and closes the root, transfer handles, and owned
// staging files. The manager remains reusable. Call it whenever permission is
// revoked or its generation changes, even if Handle never observes an empty
// root. Reset does not reopen a Manager that has been permanently Closed.
func (m *Manager) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rootName = ""
	return m.reset()
}

// Sweep releases transfers idle for at least two minutes. It is optional; every
// Handle call also expires transfers, including malformed/unknown requests.
func (m *Manager) Sweep() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.expire(m.now())
}

// Handle executes one file_* method; data is binary input to file_upload_chunk
// only, and binary output is produced by file_download_chunk only. An empty root
// disables transfers and invalidates all IDs; a changed root also invalidates
// all IDs, even if the new request is invalid or the new root cannot be opened.
// See README.md for the precise parameters, response metadata, and error codes.
func (m *Manager) Handle(root, method string, raw json.RawMessage, data []byte) ([]byte, map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, failure("closed", "file-transfer session is closed")
	}
	now := m.now()
	cleanupErr := m.expire(now)
	if root != m.rootName || root == "" {
		cleanupErr = errors.Join(cleanupErr, m.reset())
		m.rootName = root
	}
	if root == "" {
		return nil, nil, failure("disabled", "file transfers are disabled")
	}
	if cleanupErr != nil {
		return nil, nil, failure("io_error", "could not fully clean expired or invalidated transfers")
	}
	if m.root == nil {
		var err error
		m.root, err = os.OpenRoot(root)
		if err != nil {
			return nil, nil, failure("invalid_root", "cannot open the configured file-transfer directory")
		}
	}
	if len(data) > ChunkSize || (method != "file_upload_chunk" && len(data) != 0) {
		return nil, nil, failure("invalid_request", "binary input is permitted only for upload chunks of at most 32768 bytes")
	}
	var meta map[string]any
	var output []byte
	var err error
	switch method {
	case "file_list":
		var p struct {
			Path string `json:"path"`
		}
		if err = decode(raw, &p); err == nil {
			meta, err = m.list(p.Path)
		}
	case "file_upload_begin":
		var p struct {
			Path string `json:"path"`
			Size *int64 `json:"size"`
		}
		if err = decode(raw, &p); err == nil {
			if p.Size == nil {
				err = failure("invalid_request", "size is required")
			} else {
				meta, err = m.uploadBegin(p.Path, *p.Size, now)
			}
		}
	case "file_upload_chunk":
		var p struct {
			ID     string `json:"id"`
			Offset *int64 `json:"offset"`
			SHA256 string `json:"sha256"`
		}
		if err = decode(raw, &p); err == nil {
			meta, err = m.uploadChunk(p.ID, p.Offset, p.SHA256, data, now)
		}
	case "file_upload_commit":
		var p struct {
			ID     string `json:"id"`
			SHA256 string `json:"sha256"`
		}
		if err = decode(raw, &p); err == nil {
			meta, err = m.uploadCommit(p.ID, p.SHA256)
		}
	case "file_download_begin":
		var p struct {
			Path string `json:"path"`
		}
		if err = decode(raw, &p); err == nil {
			meta, err = m.downloadBegin(p.Path, now)
		}
	case "file_download_chunk":
		var p struct {
			ID     string `json:"id"`
			Offset *int64 `json:"offset"`
		}
		if err = decode(raw, &p); err == nil {
			output, meta, err = m.downloadChunk(p.ID, p.Offset, now)
		}
	case "file_cancel":
		var p struct {
			ID string `json:"id"`
		}
		if err = decode(raw, &p); err == nil {
			if t, lookupErr := m.lookup(p.ID, ""); lookupErr != nil {
				err = lookupErr
			} else if cleanup(t) != nil {
				delete(m.transfers, p.ID)
				err = failure("io_error", "transfer cancelled but staging cleanup failed")
			} else {
				delete(m.transfers, p.ID)
				meta = map[string]any{"id": p.ID, "cancelled": true}
			}
		}
	default:
		err = failure("unknown_method", "unknown file-transfer method")
	}
	if err != nil {
		return nil, nil, err
	}
	return output, meta, nil
}

func decode(raw json.RawMessage, dst any) error {
	if len(raw) > maxParams {
		return failure("invalid_request", "parameters exceed 16384 bytes")
	}
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return failure("invalid_request", "parameters must be a JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return failure("invalid_request", "invalid method parameters")
	}
	if d.Decode(new(any)) != io.EOF {
		return failure("invalid_request", "parameters must contain exactly one JSON object")
	}
	return nil
}

// Paths use a deliberately portable subset, including on non-Windows hosts.
// Reject before cleaning: cleaning away '..' would conceal traversal attempts.
func publicPath(name string, directory bool) (string, error) {
	if directory && (name == "" || name == ".") {
		return ".", nil
	}
	if len(name) == 0 || len(name) > 4096 || !utf8.ValidString(name) {
		return "", failure("invalid_path", "invalid relative path")
	}
	for _, component := range strings.Split(name, "/") {
		if !publicName(component) {
			return "", failure("invalid_path", "path contains an unsafe or reserved component")
		}
	}
	return name, nil
}

func publicName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") ||
		strings.ContainsAny(name, "\\/:*?\"<>|~") || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	upper := strings.ToUpper(name)
	if strings.HasPrefix(strings.ToLower(name), tempPrefix) {
		return false
	}
	base, _, _ := strings.Cut(upper, ".")
	base = strings.TrimRight(base, " ")
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
		return false
	}
	if strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT") {
		suffix := base[3:]
		if strings.Contains("123456789¹²³", suffix) && utf8.RuneCountInString(suffix) == 1 {
			return false
		}
	}
	return true
}

// Walk with directory handles, checking identity after each open. Rejecting
// symlinks also prevents aliases into the reserved staging namespace. Root adds
// confinement even if a component is replaced during an operation.
func (m *Manager) openDir(name string) (*os.Root, error) {
	dir, err := m.root.OpenRoot(".")
	if err != nil {
		return nil, failure("io_error", "cannot open directory")
	}
	if name == "." {
		return dir, nil
	}
	for _, part := range strings.Split(name, "/") {
		info, err := dir.Lstat(part)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			dir.Close()
			return nil, failure("invalid_path", "directory is missing, not a directory, or a symbolic link")
		}
		next, err := dir.OpenRoot(part)
		dir.Close()
		if err != nil {
			return nil, failure("invalid_path", "cannot open confined directory")
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, failure("invalid_path", "directory changed while opening")
		}
		dir = next
	}
	return dir, nil
}

func (m *Manager) list(name string) (map[string]any, error) {
	name, err := publicPath(name, true)
	if err != nil {
		return nil, err
	}
	dir, err := m.openDir(name)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := dir.Open(".")
	if err != nil {
		return nil, failure("io_error", "cannot read directory")
	}
	defer f.Close()
	entries := make([]map[string]any, 0)
	truncated := false
	scanned := 0
scan:
	for {
		batch, readErr := f.ReadDir(64)
		if readErr != nil && readErr != io.EOF {
			return nil, failure("io_error", "cannot read directory entries")
		}
		for _, entry := range batch {
			scanned++
			if scanned > maxScanned {
				truncated = true
				break scan
			}
			if !publicName(entry.Name()) {
				continue
			}
			info, err := dir.Lstat(entry.Name())
			if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
				continue
			}
			if len(entries) == maxEntries {
				truncated = true
				break scan
			}
			size := info.Size()
			if info.IsDir() {
				size = 0
			}
			entries = append(entries, map[string]any{"name": entry.Name(), "directory": info.IsDir(), "size": size})
		}
		if readErr == io.EOF {
			break
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i]["name"].(string) < entries[j]["name"].(string) })
	return map[string]any{"entries": entries, "path": name, "maxFileSize": MaxFileSize, "chunkSize": ChunkSize, "truncated": truncated}, nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", failure("io_error", "cannot generate a transfer identifier")
	}
	return hex.EncodeToString(b[:]), nil
}

func (m *Manager) newID() (string, error) {
	if len(m.transfers) >= maxTransfers {
		return "", failure("limit", "at most two transfers may be active per session")
	}
	for i := 0; i < 4; i++ {
		id, err := randomID()
		if err != nil {
			return "", err
		}
		if _, exists := m.transfers[id]; !exists {
			return id, nil
		}
	}
	return "", failure("io_error", "cannot allocate a unique transfer identifier")
}

func beginMetadata(t *transfer) map[string]any {
	return map[string]any{"id": t.id, "path": t.path, "size": t.size, "offset": int64(0), "chunkSize": ChunkSize}
}

func (m *Manager) uploadBegin(name string, size int64, now time.Time) (map[string]any, error) {
	name, err := publicPath(name, false)
	if err != nil {
		return nil, err
	}
	if size < 0 || size > MaxFileSize {
		return nil, failure("size_limit", "file size must be between zero and 1073741824 bytes")
	}
	id, err := m.newID()
	if err != nil {
		return nil, err
	}
	parent, err := m.openDir(path.Dir(name))
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			parent.Close()
		}
	}()
	if _, err := parent.Lstat(path.Base(name)); err == nil {
		return nil, failure("exists", "destination already exists; files are never overwritten")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, failure("io_error", "cannot check destination")
	}
	for i := 0; i < 4; i++ {
		suffix, err := randomID()
		if err != nil {
			return nil, err
		}
		temp := tempPrefix + suffix
		f, err := parent.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, failure("io_error", "cannot create upload staging file")
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			// Without identity information it is unsafe to remove a pathname.
			return nil, failure("io_error", "cannot inspect upload staging file")
		}
		t := &transfer{id: id, path: name, size: size, lastUsed: now, file: f, info: info, parent: parent, temp: temp}
		m.transfers[id] = t
		keep = true
		return beginMetadata(t), nil
	}
	return nil, failure("io_error", "cannot allocate upload staging file")
}

func (m *Manager) lookup(id, direction string) (*transfer, error) {
	t := m.transfers[id]
	if t == nil {
		return nil, failure("unknown_transfer", "unknown or expired transfer identifier")
	}
	if (direction == "upload" && t.temp == "") || (direction == "download" && t.temp != "") {
		return nil, failure("wrong_transfer", "transfer identifier has the wrong direction")
	}
	return t, nil
}

func checkOffset(t *transfer, offset *int64) error {
	if offset == nil {
		return failure("invalid_request", "offset is required")
	}
	if *offset != t.offset {
		return failure("offset_mismatch", fmt.Sprintf("expected offset %d", t.offset))
	}
	return nil
}

func parseHash(s string) ([]byte, error) {
	if len(s) != sha256.Size*2 {
		return nil, failure("invalid_request", "sha256 must contain exactly 64 hexadecimal characters")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, failure("invalid_request", "sha256 must contain exactly 64 hexadecimal characters")
	}
	return b, nil
}

func (m *Manager) uploadChunk(id string, offset *int64, digest string, data []byte, now time.Time) (map[string]any, error) {
	t, err := m.lookup(id, "upload")
	if err != nil {
		return nil, err
	}
	if err := checkOffset(t, offset); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, failure("invalid_request", "upload chunks must contain at least one byte; empty files commit directly")
	}
	if int64(len(data)) > t.size-t.offset {
		return nil, failure("size_mismatch", "chunk exceeds the declared file size")
	}
	want, err := parseHash(digest)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if !bytes.Equal(want, sum[:]) {
		return nil, failure("hash_mismatch", "chunk SHA-256 does not match")
	}
	n, err := t.file.WriteAt(data, t.offset)
	if err != nil || n != len(data) {
		// Retrying from the unchanged logical offset replaces any partial write.
		_ = t.file.Truncate(t.offset)
		return nil, failure("io_error", "cannot write upload chunk; offset has not advanced")
	}
	t.offset += int64(n)
	t.lastUsed = now
	return map[string]any{"id": id, "offset": t.offset}, nil
}

func (m *Manager) uploadCommit(id, digest string) (map[string]any, error) {
	t, err := m.lookup(id, "upload")
	if err != nil {
		return nil, err
	}
	want, err := parseHash(digest)
	if err != nil {
		return nil, err
	}
	info, err := t.file.Stat()
	if err != nil {
		return nil, failure("io_error", "cannot inspect staged upload")
	}
	if t.offset != t.size || info.Size() != t.size {
		return nil, failure("size_mismatch", "upload has not reached its declared size")
	}
	// Re-read the actual staged bytes, with bounded memory, before publishing.
	h := sha256.New()
	n, err := io.CopyBuffer(h, io.NewSectionReader(t.file, 0, t.size), make([]byte, ChunkSize))
	if err != nil || n != t.size {
		return nil, failure("io_error", "cannot verify staged upload")
	}
	if !bytes.Equal(want, h.Sum(nil)) {
		return nil, failure("hash_mismatch", "file SHA-256 does not match")
	}
	if err := t.file.Sync(); err != nil {
		return nil, failure("io_error", "cannot flush staged upload")
	}
	current, err := t.parent.Lstat(t.temp)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(t.info, current) || current.Size() != t.size {
		return nil, failure("io_error", "upload staging path changed")
	}
	// Link atomically fails if ANY destination already exists. Never fall back
	// to rename or copying, which could overwrite or expose partial user files.
	if err := t.parent.Link(t.temp, path.Base(t.path)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, failure("exists", "destination already exists; files are never overwritten")
		}
		return nil, failure("publication_failed", "cannot publish upload atomically; destination filesystem must support hard links")
	}
	delete(m.transfers, id)
	meta := map[string]any{"id": id, "path": t.path, "size": t.size, "sha256": hex.EncodeToString(h.Sum(nil))}
	// Publication is already successful; do not report a retryable commit error
	// if removing the private staging name fails. The destination is never removed.
	if err := cleanup(t); err != nil {
		meta["cleanupWarning"] = "file committed, but upload staging cleanup failed"
	}
	return meta, nil
}

func (m *Manager) downloadBegin(name string, now time.Time) (map[string]any, error) {
	name, err := publicPath(name, false)
	if err != nil {
		return nil, err
	}
	id, err := m.newID()
	if err != nil {
		return nil, err
	}
	parent, err := m.openDir(path.Dir(name))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	info, err := parent.Lstat(path.Base(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, failure("not_found", "file does not exist")
	}
	if err != nil {
		return nil, failure("io_error", "cannot inspect download file")
	}
	if !info.Mode().IsRegular() {
		return nil, failure("not_file", "downloads require a regular file, not a directory or symbolic link")
	}
	if info.Size() < 0 || info.Size() > MaxFileSize {
		return nil, failure("size_limit", "download exceeds the maximum file size")
	}
	f, err := parent.Open(path.Base(name))
	if err != nil {
		return nil, failure("io_error", "cannot open download file")
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != info.Size() || !opened.ModTime().Equal(info.ModTime()) {
		f.Close()
		return nil, failure("file_changed", "download file changed while opening")
	}
	t := &transfer{id: id, path: name, size: opened.Size(), lastUsed: now, file: f, info: opened, digest: sha256.New()}
	m.transfers[id] = t
	return beginMetadata(t), nil
}

func (m *Manager) downloadChunk(id string, offset *int64, now time.Time) ([]byte, map[string]any, error) {
	t, err := m.lookup(id, "download")
	if err != nil {
		return nil, nil, err
	}
	if err := checkOffset(t, offset); err != nil {
		return nil, nil, err
	}
	unchanged := func() bool {
		info, err := t.file.Stat()
		return err == nil && info.Mode().IsRegular() && info.Size() == t.size && info.ModTime().Equal(t.info.ModTime())
	}
	if !unchanged() {
		return nil, nil, failure("file_changed", "download file changed; cancel and start a new download")
	}
	length := min(int64(ChunkSize), t.size-t.offset)
	data := make([]byte, int(length))
	if length > 0 {
		n, err := t.file.ReadAt(data, t.offset)
		if err != nil || n != len(data) {
			return nil, nil, failure("io_error", "cannot read download chunk; offset has not advanced")
		}
	}
	if !unchanged() {
		return nil, nil, failure("file_changed", "download file changed while reading")
	}
	sum := sha256.Sum256(data)
	t.digest.Write(data)
	start := t.offset
	t.offset += length
	t.lastUsed = now
	eof := t.offset == t.size
	meta := map[string]any{"id": id, "offset": start, "nextOffset": t.offset, "size": length, "sha256": hex.EncodeToString(sum[:]), "eof": eof}
	if eof {
		meta["fileSHA256"] = hex.EncodeToString(t.digest.Sum(nil))
		delete(m.transfers, id)
		_ = cleanup(t)
	}
	return data, meta, nil
}

func (m *Manager) expire(now time.Time) error {
	var errs []error
	for id, t := range m.transfers {
		if now.Sub(t.lastUsed) >= idleTimeout {
			errs = append(errs, cleanup(t))
			delete(m.transfers, id)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) reset() error {
	var errs []error
	for id, t := range m.transfers {
		errs = append(errs, cleanup(t))
		delete(m.transfers, id)
	}
	if m.root != nil {
		errs = append(errs, m.root.Close())
		m.root = nil
	}
	return errors.Join(errs...)
}

// Remove only this transfer's staging name, after checking its original file
// identity. Never glob, recursively delete, or remove the destination pathname.
func cleanup(t *transfer) error {
	var errs []error
	if t.file != nil {
		errs = append(errs, t.file.Close())
		t.file = nil
	}
	if t.parent != nil {
		info, err := t.parent.Lstat(t.temp)
		if err == nil {
			if info.Mode().IsRegular() && os.SameFile(t.info, info) {
				errs = append(errs, t.parent.Remove(t.temp))
			} else {
				errs = append(errs, errors.New("staging path no longer belongs to this transfer"))
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		errs = append(errs, t.parent.Close())
		t.parent = nil
	}
	return errors.Join(errs...)
}
