package viewerapp

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/stream"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestTileHubSlowConsumerMergesAllDirtyRegions(t *testing.T) {
	h := newTileHub()
	f := stream.TileFrame{Width: 512, Height: 256, Reset: true, Tiles: []stream.Tile{{Width: 256, Height: 256, Size: 1, MIME: "image/png"}, {X: 256, Width: 256, Height: 256, Size: 1, MIME: "image/png"}}}
	if err := h.publish(f, []byte{1, 2}, false); err != nil {
		t.Fatal(err)
	}
	_, _, first, _ := h.snapshot(0)
	f.Reset = false
	f.Tiles = f.Tiles[:1]
	if err := h.publish(f, []byte{3}, false); err != nil {
		t.Fatal(err)
	}
	f.Tiles[0].X = 256
	if err := h.publish(f, []byte{4}, false); err != nil {
		t.Fatal(err)
	}
	frame, data, revision, _ := h.snapshot(first)
	if frame.Reset || len(frame.Tiles) != 2 || string(data) != string([]byte{3, 4}) {
		t.Fatalf("lost a dirty region: %+v %v", frame, data)
	}
	all, full, _, _ := h.snapshot(0)
	if !all.Reset || len(all.Tiles) != 2 || string(full) != string(data) {
		t.Fatal("reopened browser lost its keyframe")
	}
	_, idle, seq, _ := h.snapshot(revision)
	if len(idle) != 0 || seq != revision {
		t.Fatal("idle hub resent data")
	}
	empty, _, _, _ := h.snapshot(revision)
	header, err := json.Marshal(empty)
	if err != nil || !bytes.Contains(header, []byte(`"tiles":[]`)) {
		t.Fatalf("empty update is not browser-decodable: %s %v", header, err)
	}
}

func TestTileCacheRejectsOversizeWithoutLosingCurrentState(t *testing.T) {
	h := newTileHub()
	f := stream.TileFrame{Width: 512, Height: 256, Reset: true, Tiles: []stream.Tile{{Width: 256, Height: 256, Size: protocol.MaxDataSize - 2, MIME: "image/png"}, {X: 256, Width: 256, Height: 256, Size: 1, MIME: "image/png"}}}
	if err := h.publish(f, make([]byte, protocol.MaxDataSize-1), false); err != nil {
		t.Fatal(err)
	}
	f.Reset = false
	f.Tiles = f.Tiles[1:]
	f.Tiles[0].Size = 4
	if h.publish(f, make([]byte, 4), false) == nil {
		t.Fatal("unbounded aggregate cache accepted")
	}
	if h.revision != 1 || h.bytes != protocol.MaxDataSize-1 || len(h.tiles[[2]int{256, 0}].data) != 1 {
		t.Fatal("failed publication corrupted cached frame")
	}
	f.Tiles[0].X = 0
	f.Tiles[0].Size = 1
	if err := h.publish(f, []byte{7}, false); err != nil {
		t.Fatal(err)
	}
	if h.bytes != 2 {
		t.Fatalf("replaced tile still counted: %d", h.bytes)
	}
}

func TestTileSnapshotsStayConsistentDuringPublication(t *testing.T) {
	h := newTileHub()
	f := stream.TileFrame{Width: 512, Height: 256, Reset: true, Tiles: []stream.Tile{{Width: 256, Height: 256, Size: 1, MIME: "image/png"}, {X: 256, Width: 256, Height: 256, Size: 1, MIME: "image/png"}}}
	if err := h.publish(f, []byte{0, 0}, false); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if err := h.publish(f, []byte{byte(i), byte(i)}, false); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 100; i++ {
		frame, data, _, _ := h.snapshot(0)
		if len(frame.Tiles) != 2 || len(data) != 2 || data[0] != data[1] {
			t.Error("snapshot mixed revisions")
			break
		}
	}
	wg.Wait()
}

func TestFrameHTTPResponsesReuseConnection(t *testing.T) {
	c := &client{tiles: newTileHub(), closed: make(chan struct{})}
	f := stream.TileFrame{Width: 256, Height: 256, Reset: true, Tiles: []stream.Tile{{Width: 256, Height: 256, Size: 1, MIME: "image/png"}}}
	if err := c.tiles.publish(f, []byte{1}, false); err != nil {
		t.Fatal(err)
	}
	addresses := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { addresses <- r.RemoteAddr; c.serveTiles(w, r) }))
	defer server.Close()
	client := server.Client()
	for i := 0; i < 2; i++ {
		response, err := client.Get(server.URL + "/api/frames?after=0")
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.ContentLength != int64(len(data)) || len(response.TransferEncoding) != 0 {
			t.Fatal("frame is still an unfinished chunked response")
		}
	}
	if first, second := <-addresses, <-addresses; first != second {
		t.Fatal("frame response forced a new connection")
	}
}

func TestTileHubRejectsMissingKeyframe(t *testing.T) {
	h := newTileHub()
	f := stream.TileFrame{Width: 256, Height: 256, Tiles: []stream.Tile{{Width: 256, Height: 256, Size: 1, MIME: "image/png"}}}
	if h.publish(f, []byte{1}, false) == nil {
		t.Fatal("accepted delta before keyframe")
	}
}

func TestBrowserFrameResponseHasBoundedLifetimeAndRevision(t *testing.T) {
	c := &client{tiles: newTileHub(), closed: make(chan struct{})}
	f := stream.TileFrame{Width: 256, Height: 256, Reset: true, Tiles: []stream.Tile{{Width: 256, Height: 256, Size: 1, MIME: "image/png"}}}
	if err := c.tiles.publish(f, []byte{1}, false); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	c.serveTiles(recorder, httptest.NewRequest("GET", "/api/frames?after=0", nil))
	var sizes [8]byte
	if _, err := io.ReadFull(recorder.Body, sizes[:]); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, binary.BigEndian.Uint32(sizes[:4]))
	if _, err := io.ReadFull(recorder.Body, header); err != nil {
		t.Fatal(err)
	}
	var result stream.TileFrame
	if err := json.Unmarshal(header, &result); err != nil {
		t.Fatal(err)
	}
	if result.Revision != 1 || !result.Reset {
		t.Fatalf("missing browser cursor: %+v", result)
	}
	if recorder.Body.Len() != 1 {
		t.Fatal("more than one update was queued for a browser render")
	}
	bad := httptest.NewRecorder()
	c.serveTiles(bad, httptest.NewRequest("GET", "/api/frames?after=-1", nil))
	if bad.Code != 400 {
		t.Fatal("invalid cursor accepted")
	}
}
