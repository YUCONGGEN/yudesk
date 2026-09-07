# File-transfer backend contract

Requires **Go 1.25+**, because publication uses `os.Root.Link`. No third-party
dependencies. Create one manager per authenticated connection; authorization,
binary transport framing, and viewer-side download verification belong to the
caller.

```go
const ChunkSize = 32 << 10  // 32,768 bytes
const MaxFileSize = 1 << 30 // 1,073,741,824 bytes

func New() *Manager
func (*Manager) Handle(root, method string, raw json.RawMessage, data []byte) ([]byte, map[string]any, error)
func (*Manager) Reset() error
func (*Manager) Close() error
func (*Manager) Sweep() error
```

`Reset` invalidates every ID and cleans owned staging files and all handles; the
manager remains reusable, including with the exact same root. Call it when local
permission is revoked or the permission generation changes. The caller must gate
requests on current permission/generation, including queued requests. An empty
root in **any** `Handle` call also invalidates all transfers and returns `disabled`.
A changed root invalidates old transfers before validating the request or opening
the new root. `Close` is permanent and idempotent; `Reset` cannot reopen it.

All calls are serialized. At most two uploads/downloads combined are active.
Transfers expire after two minutes without a successful chunk request. Each
request checks expiry, even malformed requests. `Sweep` permits proactive cleanup
while no requests arrive, with no internal goroutine or timer. Use `Close` on
disconnect. Invalid requests, offsets, checksums, and publication failures do not
advance a transfer or invalidate other IDs. Cancellation, reset, expiry, and
close invalidate IDs even if filesystem cleanup reports an error.

## Parameters and metadata

All metadata contains ordinary JSON-compatible maps, arrays, strings, numbers,
and booleans. Sizes/offsets in Go metadata are `int64`; `chunkSize` and
`maxFileSize` are `int`. IDs are opaque random 128-bit identifiers represented by
32 lowercase hexadecimal characters, scoped to the manager. Never derive a
filesystem pathname from an ID.

| Method | JSON parameters | Success metadata |
| --- | --- | --- |
| `file_list` | `{"path":"."}`; path may be omitted or empty | `entries`, `path`, `maxFileSize`, `chunkSize`, `truncated` |
| `file_upload_begin` | `{"path":"nested/file.bin","size":123}` | `id`, `path`, `size`, `offset:0`, `chunkSize` |
| `file_upload_chunk` | `{"id":"...","offset":0,"sha256":"..."}` plus binary data | `id`, `offset` (**next** acknowledged byte offset) |
| `file_upload_commit` | `{"id":"...","sha256":"..."}` (whole-file hash) | `id`, `path`, `size`, `sha256` |
| `file_download_begin` | `{"path":"nested/file.bin"}` | `id`, `path`, `size`, `offset:0`, `chunkSize` |
| `file_download_chunk` | `{"id":"...","offset":0}` | `id`, `offset` (**starting** byte offset), `nextOffset`, `size` (this chunk), `sha256` (this chunk), `eof`; **`fileSHA256`** on final chunk only |
| `file_cancel` | `{"id":"..."}` | `id`, `cancelled:true` |

`file_download_chunk` returns its bytes in the first result. All other methods
return nil binary output. Only `file_upload_chunk` accepts nonempty binary input.
Upload chunk lengths must be 1..32768 bytes. Upload/download offsets are strictly
ordered and start at zero. Requests are not replayable after success: retry only
rejected requests whose offset did not advance. The enclosing transport should
close/reset the session if it loses an acknowledgement and cannot determine
whether a request succeeded.

Hashes are exactly 64 hexadecimal characters. Input accepts either case; output
is lowercase. Commit re-reads and hashes the actual staged file using a 32 KiB
buffer, verifies declared size, and flushes before publication. An empty upload
commits directly without chunks, using SHA-256 of empty input. An empty download
still requires one chunk request at offset zero; it returns no bytes, `size:0`,
`nextOffset:0`, `eof:true`, and both empty-input hashes.

Commit and final download chunk automatically release their IDs and capacity.
The final download `fileSHA256` hashes the concatenation of the bytes returned
for that transfer. The viewer must verify each chunk and the final whole-file
hash before publishing its local download. Downloads detect source size or
modification-time changes and return `file_changed` without advancing; cancel
and begin again in that case. They are not filesystem snapshots.

`entries` is always an array, including for an empty directory. Each entry is
`{"name":"file.bin","directory":false,"size":123}`; directory size is zero.
Root paths `""` and `"."` are returned canonically as `"."`. At most 1000 public
entries are returned, sorted by name. Directory enumeration is bounded to 10,000
examined entries; `truncated:true` indicates either bound omitted entries. There
is no pagination or directory-creation API. Large public files may be listed but
cannot be downloaded above `MaxFileSize`.

## Paths and publication

Paths are relative and use `/` separators. Existing nested directories work.
Absolute paths, empty/dot/traversal components, all symlinks (including confined
ones), directories as file targets, nonregular download files, Windows drive/UNC
and ADS syntax, device names (including superscript COM/LPT forms), control
characters, trailing spaces/dots, and Windows-invalid characters are rejected.
Tildes are also rejected to prevent NTFS short-name aliases from exposing internal
staging files. Paths are bounded to 4096 UTF-8 bytes and JSON parameters to 16 KiB.
Unsafe entries and symlinks are omitted from listings.

The case-insensitive `.yudesk-filetransfer-` prefix is reserved in every path
component. Stage files use random names with that prefix, exclusive creation,
and mode 0600 in the pinned destination directory. Root-relative APIs and pinned
directory handles prevent symlink escapes and parent-path replacement from
redirecting an existing operation. Renaming a destination directory preserves
the already-open directory as the operation's target.

Publication is an atomic hard-link creation which **never replaces any existing
destination**, including a symlink or directory. Both begin-time and commit-time
collisions fail clearly. A filesystem without hard-link support returns
`publication_failed`; there is no rename/copy fallback. Cleanup only removes the
owned staging name after checking file identity, and never removes the published
destination. A rare cleanup failure *after successful publication* returns
successful commit metadata with an additional `cleanupWarning` string, avoiding
an ambiguous retry of an already-published file.

As with `os.Root`, the configured tree is a trusted local share boundary, not a
sandbox against the local account: existing hard links and mounted filesystems
are not isolated. A local process with access to staging can modify or rename
files. Identity checks avoid deleting a replacement file; moved or inaccessible
staging names may require local cleanup. There is no scan for or deletion of
foreign/stale staging files belonging to other sessions.

## Errors

`Handle` returns `(nil, nil, *filetransfer.Error)` on failure. Its exported
`Code` and `Message` fields marshal as `{"code":"...","message":"..."}`;
`Error()` returns the message. The caller can use `errors.As` or serialize the
structured error. Error responses do not require disconnecting the session.

Codes: `closed`, `disabled`, `invalid_root`, `invalid_request`, `unknown_method`,
`invalid_path`, `not_found`, `not_file`, `exists`, `size_limit`, `limit`,
`unknown_transfer`, `wrong_transfer`, `offset_mismatch`, `size_mismatch`,
`hash_mismatch`, `file_changed`, `io_error`, `publication_failed`.
