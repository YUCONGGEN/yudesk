package stream

import (
	"testing"
	"time"
)

func TestLatencyControllerBacksOffBeforeQualityAndRecoversSlowly(t *testing.T) {
	c := NewLatencyController(Options{Quality: 70, MaxWidth: 1280})
	now := time.Now()
	c.Ack(20 * time.Millisecond)
	for i := 0; i < 20; i++ {
		c.Ack(220 * time.Millisecond)
		c.Update(now.Add(time.Duration(i)*time.Second), 60*time.Millisecond, 33*time.Millisecond, 1920)
	}
	if c.Width >= 1280 || c.Width < 640 || c.Quality >= 70 || c.Quality < 45 {
		t.Fatalf("backoff: %+v", c)
	}
	w := c.Width
	for i := 0; i < 20; i++ {
		c.Ack(20 * time.Millisecond)
		c.Update(now.Add(time.Duration(i+20)*time.Second), 10*time.Millisecond, 33*time.Millisecond, 1920)
	}
	if c.Width > w {
		t.Fatal("quality recovered before stable capacity")
	}
}

func TestLatencyControllerFlightWindowTracksPropagationNotQueue(t *testing.T) {
	c := NewLatencyController(Options{Quality: 70, MaxWidth: 1280})
	frames, bytes := c.FlightWindow(time.Second/30, 8)
	if frames != 2 || bytes != 50_000 {
		t.Fatalf("initial window: %d frames, %d bytes", frames, bytes)
	}
	c.Ack(20 * time.Millisecond)
	frames, bytes = c.FlightWindow(time.Second/30, 8)
	if frames != 2 || bytes != 20_000 {
		t.Fatalf("fast path retained excess queue: %d frames, %d bytes", frames, bytes)
	}
	for range 8 {
		c.Ack(120 * time.Millisecond)
	}
	frames, bytes = c.FlightWindow(time.Second/30, 8)
	if frames != 2 || bytes != 20_000 {
		t.Fatalf("queue growth enlarged propagation window: %d frames, %d bytes", frames, bytes)
	}
	high := NewLatencyController(Options{Quality: 70, MaxWidth: 1280})
	high.Ack(100 * time.Millisecond)
	frames, bytes = high.FlightWindow(time.Second/30, 8)
	if frames != 3 || bytes != 100_000 {
		t.Fatalf("100ms path lost BDP: %d frames, %d bytes", frames, bytes)
	}
}
