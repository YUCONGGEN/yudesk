package desktop

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
)

func TestCaptureResizePreservesInputSpace(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	shot, err := encodeScreen(img, CaptureOptions{Quality: 70, MaxWidth: 1280})
	if err != nil {
		t.Fatal(err)
	}
	if shot.Width != 1280 || shot.Height != 720 || shot.SourceWidth != 3840 || shot.SourceHeight != 2160 {
		t.Fatalf("wrong dimensions: %+v", shot)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(shot.JPEG))
	if err != nil || cfg.Width != 1280 || cfg.Height != 720 {
		t.Fatalf("encoded dimensions %v %v", cfg, err)
	}
	if ScaleCoordinate(1279, 1280, 3840) != 3839 || ScaleCoordinate(719, 720, 2160) != 2159 {
		t.Fatal("edge coordinates not preserved")
	}
	if ScaleCoordinate(-30, 1280, 3840) != 0 || ScaleCoordinate(9000, 1280, 3840) != 3839 {
		t.Fatal("out of bounds coordinates not clamped")
	}
}

func TestWindowsAbsoluteCoordinatesAndButtons(t *testing.T) {
	for _, extent := range []int{1280, 1920, 2560, 3840, 7680} {
		for x := 0; x < extent; x++ {
			got := int(absolutePixel(x, extent)) * extent / 65536
			if got != x {
				t.Fatalf("extent %d: point %d became %d", extent, x, got)
			}
		}
	}
	for _, test := range []struct {
		button   int
		down, up uint32
	}{{1, 2, 4}, {2, 32, 64}, {3, 8, 16}} {
		if mouseButtonFlags(test.button, false) != test.down || mouseButtonFlags(test.button, true) != test.up {
			t.Fatalf("wrong mapping for button %d", test.button)
		}
	}
}
