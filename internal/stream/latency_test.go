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
