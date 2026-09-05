package desktop

import (
	"bytes"
	"image"
	"image/jpeg"
)

// encodeScreen preserves the source dimensions for input mapping. Resizing is
// optional and never enlarges a captured desktop.
func encodeScreen(img image.Image, options CaptureOptions) (Screenshot, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if options.MaxWidth > 0 && w > options.MaxWidth {
		nw := options.MaxWidth
		nh := max(1, h*nw/w)
		out := image.NewRGBA(image.Rect(0, 0, nw, nh))
		rgba, fast := img.(*image.RGBA)
		for y := 0; y < nh; y++ {
			for x := 0; x < nw; x++ {
				sx, sy := b.Min.X+x*w/nw, b.Min.Y+y*h/nh
				if fast {
					src, dst := rgba.PixOffset(sx, sy), out.PixOffset(x, y)
					copy(out.Pix[dst:dst+4], rgba.Pix[src:src+4])
				} else {
					out.Set(x, y, img.At(sx, sy))
				}
			}
		}
		img = out
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: options.Quality}); err != nil {
		return Screenshot{}, err
	}
	return Screenshot{JPEG: encoded.Bytes(), Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), SourceWidth: w, SourceHeight: h}, nil
}

func ScaleCoordinate(value, from, to int) int {
	if from <= 1 || to <= 1 {
		return 0
	}
	value = min(max(value, 0), from-1)
	return int((int64(value)*int64(to-1) + int64(from-1)/2) / int64(from-1))
}

// Windows absolute input addresses pixel centres, avoiding floor-rounding to
// the preceding pixel. Browser buttons follow left=1, middle=2, right=3.
func absolutePixel(value, extent int) int32 {
	if extent <= 0 {
		return 0
	}
	value = min(max(value, 0), extent-1)
	return int32(min(int64(65535), (2*int64(value)+1)*65536/(2*int64(extent))))
}

func mouseButtonFlags(button int, release bool) uint32 {
	flags := map[int]uint32{1: 0x0002, 2: 0x0020, 3: 0x0008}[button]
	if release {
		flags <<= 1
	}
	return flags
}
