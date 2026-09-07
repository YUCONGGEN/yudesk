package stream

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"testing"
)

func testScreen(w, h int) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(im, im.Bounds(), image.NewUniform(color.RGBA{24, 32, 48, 255}), image.Point{}, draw.Src)
	return im
}

func TestTilesIdleSmallChangeAndLosslessText(t *testing.T) {
	e := new(TileEncoder)
	im := testScreen(1920, 1080)
	first, data, err := e.Encode(im, 82, false)
	if err != nil || !first.Reset || len(first.Tiles) != 40 {
		t.Fatalf("keyframe: %+v %v", first, err)
	}
	if err = first.Validate(data); err != nil {
		t.Fatal(err)
	}
	frame, idle, err := e.Encode(im, 82, false)
	if err != nil || frame.Reset || len(frame.Tiles) != 0 || len(idle) != 0 {
		t.Fatal("unchanged screen encoded again")
	}
	changed := testScreen(1920, 1080)
	draw.Draw(changed, image.Rect(10, 10, 80, 25), image.White, image.Point{}, draw.Src)
	frame, patch, err := e.Encode(changed, 82, false)
	if err != nil || frame.Reset || len(frame.Tiles) != 1 || frame.Tiles[0].MIME != "image/png" {
		t.Fatalf("dirty text not lossless: %+v %v", frame, err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(patch))
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			if decoded.At(x, y) != changed.At(x, y) {
				// Color concrete types differ between PNG and RGBA; compare channels.
				a, b, c, d := decoded.At(x, y).RGBA()
				aa, bb, cc, dd := changed.At(x, y).RGBA()
				if a != aa || b != bb || c != cc || d != dd {
					t.Fatalf("pixel mismatch %d,%d", x, y)
				}
			}
		}
	}
	var baseline bytes.Buffer
	if err := jpeg.Encode(&baseline, changed, &jpeg.Options{Quality: 82}); err != nil {
		t.Fatal(err)
	}
	if len(patch) >= baseline.Len()/4 {
		t.Fatalf("dirty patch unexpectedly large: %d vs full JPEG %d", len(patch), baseline.Len())
	}
	t.Logf("1080p text edit: full JPEG=%d bytes; dirty PNG=%d bytes; reduction=%.1f%%; keyframe=%d bytes", baseline.Len(), len(patch), 100*(1-float64(len(patch))/float64(baseline.Len())), len(data))
	resized, _, err := e.Encode(testScreen(800, 600), 82, false)
	if err != nil || !resized.Reset {
		t.Fatal("resolution change did not reset")
	}
}

func TestTileValidationRejectsMalformedPayload(t *testing.T) {
	f := TileFrame{Width: 256, Height: 256, Tiles: []Tile{{Width: 256, Height: 256, Size: 2, MIME: "image/png"}}}
	if f.Validate([]byte{1}) == nil {
		t.Fatal("accepted truncated payload")
	}
	f.Tiles[0].Size = 1
	f.Tiles[0].X = -1
	if f.Validate([]byte{1}) == nil {
		t.Fatal("accepted negative rectangle")
	}
}

func BenchmarkUnchanged1080p(b *testing.B) {
	im := testScreen(1920, 1080)
	e := new(TileEncoder)
	_, _, _ = e.Encode(im, 82, false)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := e.Encode(im, 82, false)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFullJPEG1080p(b *testing.B) {
	im := testScreen(1920, 1080)
	var out bytes.Buffer
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		if err := jpeg.Encode(&out, im, &jpeg.Options{Quality: 82}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDirtyTile1080p(b *testing.B) {
	images := []*image.RGBA{testScreen(1920, 1080), testScreen(1920, 1080)}
	draw.Draw(images[1], image.Rect(10, 10, 80, 25), image.White, image.Point{}, draw.Src)
	e := new(TileEncoder)
	_, _, _ = e.Encode(images[1], 70, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := e.Encode(images[i%2], 70, false); err != nil {
			b.Fatal(err)
		}
	}
}

func TestTileWorkerCountReservesInteractiveCapacity(t *testing.T) {
	for _, tc := range []struct{ tasks, cpus, want int }{{0, 4, 0}, {9, 1, 1}, {9, 2, 1}, {9, 4, 3}, {9, 16, 4}, {1, 16, 1}} {
		if got := tileWorkerCount(tc.tasks, tc.cpus); got != tc.want {
			t.Fatalf("%+v: got %d", tc, got)
		}
	}
}
