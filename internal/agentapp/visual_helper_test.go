package agentapp

import (
	"context"
	"crypto/ed25519"
	"image"
	"image/color"
	"image/draw"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yudesk/yudesk/internal/desktop"
	"github.com/yudesk/yudesk/internal/identity"
	"github.com/yudesk/yudesk/internal/peerpath"
	"github.com/yudesk/yudesk/internal/relay"
)

// Compiled only into the test executable. Remote input changes a synthetic
// pixel marker; it NEVER calls native input or opens a real desktop window.
func TestVisualLatencyAgentHelper(t *testing.T) {
	if os.Getenv("YUDESK_VISUAL_HELPER") != "1" {
		t.Skip("helper process only")
	}
	id, err := identity.Load(os.Getenv("YUDESK_VISUAL_IDENTITY"), "")
	if err != nil {
		t.Fatal(err)
	}
	var marker atomic.Uint32
	a := &agent{id: id.ID, pin: id.PIN, name: "visual-latency-fixture", privateKey: id.PrivateKey, allowControl: true, quit: make(chan struct{})}
	// This benchmark injects delay in the relay. Local ICE shortcuts would
	// bypass it and incorrectly label LAN timings as a 100 ms WAN result.
	a.peerOptions = &peerpath.Options{STUNURLs: []string{}, Timeout: time.Nanosecond}
	a.inputFactory = func() *inputSession {
		s := newInputSession()
		s.apply = func(events []desktop.InputEvent) error {
			for _, event := range events {
				if event.Type == "move" {
					marker.Store(uint32(event.X%240 + 1))
				}
			}
			return nil
		}
		return s
	}
	a.captureDesktop = func(options desktop.CaptureOptions) (desktop.Screenshot, error) {
		width, height := 1280, 720
		if options.MaxWidth > 0 && options.MaxWidth < width {
			width = options.MaxWidth
			height = 720 * width / 1280
		}
		img := options.Buffer
		if img == nil || img.Rect != image.Rect(0, 0, width, height) {
			img = image.NewRGBA(image.Rect(0, 0, width, height))
		}
		draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{22, 32, 48, 255}), image.Point{}, draw.Src)
		draw.Draw(img, image.Rect(0, 0, 64, 64), image.NewUniform(color.RGBA{uint8(marker.Load()), 77, 123, 255}), image.Point{}, draw.Src)
		return desktop.Screenshot{Pixels: img, Width: width, Height: height, SourceWidth: 1280, SourceHeight: 720}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	conn, err := relay.DialWithContext(ctx, os.Getenv("YUDESK_VISUAL_RELAY"), relay.DialOptions{TLS: true, Fingerprint: os.Getenv("YUDESK_VISUAL_FINGERPRINT")}, relay.Hello{Role: "agent", ID: id.ID, Name: a.name, PublicKey: id.PrivateKey.Public().(ed25519.PublicKey)})
	if err != nil {
		t.Fatal(err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	a.handle(conn)
}
