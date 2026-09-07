package viewerapp

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/stream"
)

type cachedTile struct {
	tile     stream.Tile
	data     []byte
	revision uint64
}
type tileHub struct {
	mu                sync.Mutex
	frame             stream.TileFrame
	tiles             map[[2]int]cachedTile
	revision, resetAt uint64
	bytes             int
	changed           chan struct{}
}

func newTileHub() *tileHub {
	return &tileHub{tiles: map[[2]int]cachedTile{}, changed: make(chan struct{})}
}
func (h *tileHub) publish(f stream.TileFrame, data []byte, legacy bool) error {
	if err := f.Validate(data); err != nil {
		return err
	}
	if !legacy {
		for _, t := range f.Tiles {
			if t.X%stream.TileSize != 0 || t.Y%stream.TileSize != 0 || t.Width != min(stream.TileSize, f.Width-t.X) || t.Height != min(stream.TileSize, f.Height-t.Y) {
				return errors.New("invalid tile grid")
			}
		}
		if f.Reset && len(f.Tiles) != ((f.Width+stream.TileSize-1)/stream.TileSize)*((f.Height+stream.TileSize-1)/stream.TileSize) {
			return errors.New("incomplete keyframe")
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !f.Reset && (h.revision == 0 || f.Width != h.frame.Width || f.Height != h.frame.Height) {
		return errors.New("missing tile keyframe")
	}
	retained := h.bytes
	if f.Reset {
		retained = 0
	}
	for _, t := range f.Tiles {
		if !f.Reset {
			retained -= len(h.tiles[[2]int{t.X, t.Y}].data)
		}
		retained += t.Size
	}
	if retained > protocol.MaxDataSize {
		return errors.New("tile cache exceeds frame payload limit")
	}
	h.bytes = retained
	h.revision++
	if f.Reset {
		clear(h.tiles)
		h.resetAt = h.revision
	}
	h.frame = f
	h.frame.Tiles = nil
	offset := 0
	for _, t := range f.Tiles {
		h.tiles[[2]int{t.X, t.Y}] = cachedTile{tile: t, data: append([]byte(nil), data[offset:offset+t.Size]...), revision: h.revision}
		offset += t.Size
	}
	close(h.changed)
	h.changed = make(chan struct{})
	return nil
}

// Return the union of dirty tiles, not just the most recent frame. Slow browser
// consumers skip obsolete versions without dropping updates in other regions.
func (h *tileHub) snapshot(after uint64) (stream.TileFrame, []byte, uint64, <-chan struct{}) {
	h.mu.Lock()
	f := h.frame
	f.Tiles = []stream.Tile{} // empty fixed-cadence updates must encode as [], not null
	f.Reset = after < h.resetAt
	var selected []cachedTile
	for _, t := range h.tiles {
		if t.revision > after || f.Reset {
			selected = append(selected, t)
		}
	}
	revision, changed := h.revision, h.changed
	h.mu.Unlock() // encoded tile data is immutable; copying must not stall the receiver
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].tile.Y == selected[j].tile.Y {
			return selected[i].tile.X < selected[j].tile.X
		}
		return selected[i].tile.Y < selected[j].tile.Y
	})
	size := 0
	for _, t := range selected {
		size += len(t.data)
	}
	data := make([]byte, 0, size)
	for _, t := range selected {
		f.Tiles = append(f.Tiles, t.tile)
		data = append(data, t.data...)
	}
	return f, data, revision, changed
}

// Local binary framing: big-endian JSON length + payload length, JSON, encoded
// tiles. No base64 or decoding/re-encoding in the Go viewer.
func (c *client) serveTiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	var revision uint64
	if after := r.URL.Query().Get("after"); after != "" {
		var err error
		revision, err = strconv.ParseUint(after, 10, 64)
		if err != nil {
			http.Error(w, "invalid frame cursor", 400)
			return
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	for {
		f, data, next, changed := c.tiles.snapshot(revision)
		if next < revision {
			revision = 0
			continue
		}
		if next == revision {
			select {
			case <-r.Context().Done():
				return
			case <-c.closed:
				return
			case <-changed:
				continue
			}
		}
		f.Revision = next
		header, err := json.Marshal(f)
		if err != nil {
			return
		}
		var sizes [8]byte
		binary.BigEndian.PutUint32(sizes[:4], uint32(len(header)))
		binary.BigEndian.PutUint32(sizes[4:], uint32(len(data)))
		// Known length lets browsers finish/reuse the HTTP connection instead
		// of cancelling an unfinished chunked body after every rendered frame.
		w.Header().Set("Content-Length", strconv.Itoa(8+len(header)+len(data)))
		_ = controller.SetWriteDeadline(time.Now().Add(3 * time.Second))
		for _, part := range [][]byte{sizes[:], header, data} {
			if _, err := w.Write(part); err != nil {
				return
			}
		}
		if controller.Flush() != nil {
			return
		}
		_ = controller.SetWriteDeadline(time.Time{})
		// One response per browser render. A slow decoder cannot accumulate
		// queued obsolete frames in fetch/TCP buffers; the next request merges
		// all changes since this revision instead.
		return
	}
}
