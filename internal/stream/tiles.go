package stream

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"math/bits"
	"runtime"
	"sync"
)

const TileSize = 256
const MaxPixels = 3840 * 2160 * 4

// Tiles are independently decodable absolute rectangles, not a chain of video
// deltas. Receivers may merge newer versions of the same tile without losing
// changes elsewhere, even when a browser is slow or reconnects.
type Tile struct {
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"w"`
	Height int    `json:"h"`
	Size   int    `json:"size"`
	MIME   string `json:"mime"`
}

type TileFrame struct {
	Revision     uint64 `json:"revision,omitempty"` // local viewer/browser cursor; not a media timestamp
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	SourceWidth  int    `json:"sourceWidth"`
	SourceHeight int    `json:"sourceHeight"`
	Reset        bool   `json:"reset"`
	Tiles        []Tile `json:"tiles"`
}

func (f TileFrame) Validate(data []byte) error {
	if f.Width < 1 || f.Height < 1 || f.Width > 16384 || f.Height > 16384 || f.Width*f.Height > MaxPixels || len(f.Tiles) > 1024 {
		return errors.New("invalid tile frame dimensions")
	}
	size := 0
	seen := make(map[[2]int]bool, len(f.Tiles))
	for _, t := range f.Tiles {
		position := [2]int{t.X, t.Y}
		if seen[position] {
			return errors.New("duplicate tile")
		}
		seen[position] = true
		if t.X < 0 || t.Y < 0 || t.Width < 1 || t.Height < 1 || t.X+t.Width > f.Width || t.Y+t.Height > f.Height || t.Size < 1 || t.Size > len(data)-size || (t.MIME != "image/png" && t.MIME != "image/jpeg") {
			return errors.New("invalid tile frame payload")
		}
		size += t.Size
	}
	if size != len(data) {
		return errors.New("tile payload length mismatch")
	}
	return nil
}

type TileEncoder struct {
	previous   *image.RGBA
	pngBuffers tilePNGBufferPool
}

// A PNG compressor owns roughly a megabyte of scratch storage even for a
// tiny desktop change. Reuse it across workers/frames instead of continually
// allocating it and causing GC jitter during pointer movement and typing.
type tilePNGBufferPool struct{ sync.Pool }

func (p *tilePNGBufferPool) Get() *png.EncoderBuffer {
	buffer, _ := p.Pool.Get().(*png.EncoderBuffer)
	return buffer
}

func (p *tilePNGBufferPool) Put(buffer *png.EncoderBuffer) { p.Pool.Put(buffer) }

// Encode compares raw pixels first. Unchanged tiles do not enter an encoder.
// Flat UI/text uses lossless PNG; photographic tiles use the requested JPEG
// quality when PNG would be expensive. This is a desktop codec, not H.264.
func (e *TileEncoder) Encode(img *image.RGBA, quality int, force bool) (TileFrame, []byte, error) {
	if img == nil || img.Rect.Min != (image.Point{}) || img.Rect.Dx() < 1 || img.Rect.Dy() < 1 || img.Rect.Dx()*img.Rect.Dy() > MaxPixels {
		return TileFrame{}, nil, errors.New("invalid captured pixels")
	}
	f := TileFrame{Width: img.Rect.Dx(), Height: img.Rect.Dy()}
	f.Reset = force || e.previous == nil || e.previous.Rect != img.Rect
	var rectangles []image.Rectangle
	for y := 0; y < f.Height; y += TileSize {
		for x := 0; x < f.Width; x += TileSize {
			r := image.Rect(x, y, min(x+TileSize, f.Width), min(y+TileSize, f.Height))
			changed := f.Reset
			for row := r.Min.Y; !changed && row < r.Max.Y; row++ {
				a, b := img.PixOffset(x, row), e.previous.PixOffset(x, row)
				changed = !bytes.Equal(img.Pix[a:a+r.Dx()*4], e.previous.Pix[b:b+r.Dx()*4])
			}
			if changed {
				rectangles = append(rectangles, r)
			}
		}
	}
	f.Tiles = make([]Tile, len(rectangles))
	encoded := make([][]byte, len(rectangles))
	errs := make([]error, len(rectangles))
	encode := func(i int) {
		encoder := png.Encoder{CompressionLevel: png.BestSpeed, BufferPool: &e.pngBuffers}
		r := rectangles[i]
		var out bytes.Buffer
		mime := "image/png"
		photo := photographic(img, r)
		if !photo {
			errs[i] = encoder.Encode(&out, img.SubImage(r))
		}
		if (photo || out.Len() > r.Dx()*r.Dy()/3) && errs[i] == nil {
			out.Reset()
			errs[i] = jpeg.Encode(&out, img.SubImage(r), &jpeg.Options{Quality: quality})
			mime = "image/jpeg"
		}
		encoded[i] = out.Bytes()
		f.Tiles[i] = Tile{X: r.Min.X, Y: r.Min.Y, Width: r.Dx(), Height: r.Dy(), Size: out.Len(), MIME: mime}
	}
	workers := tileWorkerCount(len(rectangles), runtime.GOMAXPROCS(0))
	if workers <= 1 {
		// The common pointer/text update needs no goroutine/channel handoff.
		for i := range rectangles {
			encode(i)
		}
	} else {
		jobs := make(chan int)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					encode(i)
				}
			}()
		}
		for i := range rectangles {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
	}
	size := 0
	for _, b := range encoded {
		size += len(b)
	}
	data := make([]byte, 0, size)
	for i, b := range encoded {
		if errs[i] != nil {
			return TileFrame{}, nil, errs[i]
		}
		data = append(data, b...)
	}
	// Capture returns an owned immutable image. Retain it until the next frame.
	e.previous = img
	return f, data, nil
}

func tileWorkerCount(tasks, cpus int) int {
	// Leave scheduler capacity for input, capture and network handling on
	// multicore hosts. More workers than CPU capacity just add contention.
	return min(tasks, max(1, min(4, cpus-1)))
}

// Sampling a coarse color histogram is much cheaper than trying PNG on every
// photographic tile. Gray text/antialiased UI retains the lossless path.
func photographic(img *image.RGBA, r image.Rectangle) bool {
	var colors [64]uint64
	for y := r.Min.Y; y < r.Max.Y; y += 8 {
		for x := r.Min.X; x < r.Max.X; x += 8 {
			i := img.PixOffset(x, y)
			c := int(img.Pix[i]>>4)<<8 | int(img.Pix[i+1]>>4)<<4 | int(img.Pix[i+2]>>4)
			colors[c/64] |= 1 << uint(c%64)
		}
	}
	n := 0
	for _, word := range colors {
		n += bits.OnesCount64(word)
	}
	return n > 96
}
