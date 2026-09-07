package desktop

import (
	"bytes"
	"image"
	"image/draw"
	"testing"
)

func TestOwnedBGRACopiesAndReusesWithoutRetainingDIB(t *testing.T) {
	src := []byte{1, 2, 3, 0, 4, 5, 6, 0}
	before := bytes.Clone(src)
	buffer := image.NewRGBA(image.Rect(0, 0, 2, 1))
	got := ownedBGRA(src, 2, 1, buffer)
	if got != buffer || !bytes.Equal(got.Pix, []byte{3, 2, 1, 255, 6, 5, 4, 255}) || !bytes.Equal(src, before) {
		t.Fatal("conversion, ownership or reuse failed")
	}
	src[0] = 99
	if got.Pix[2] != 1 {
		t.Fatal("retained source")
	}
}

func TestOwnedBGRAOddPixelAndWrongSizedReuse(t *testing.T) {
	src := []byte{1, 2, 3, 9, 4, 5, 6, 9, 7, 8, 9, 9}
	got := ownedBGRA(src, 3, 1, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	if !bytes.Equal(got.Pix, []byte{3, 2, 1, 255, 6, 5, 4, 255, 9, 8, 7, 255}) {
		t.Fatalf("bad conversion: %v", got.Pix)
	}
}

func BenchmarkDIBOwnership1080p(b *testing.B) {
	const w, h = 1920, 1080
	src := make([]byte, w*h*4)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	img := &image.RGBA{Pix: src, Stride: w * 4, Rect: dst.Rect}
	b.Run("two-pass", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for i := 0; i < len(src); i += 4 {
				src[i], src[i+2] = src[i+2], src[i]
				src[i+3] = 255
			}
			draw.Draw(dst, dst.Rect, img, image.Point{}, draw.Src)
		}
	})
	b.Run("one-pass", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ownedBGRA(src, w, h, dst)
		}
	})
}
