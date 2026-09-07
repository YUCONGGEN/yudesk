package stream

import (
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

// Opt-in native probe, not a CI timing assertion. Scheduling load affects
// samples; compare compiler/runtime versions on the same host.
func TestNativeTimerResolution(t *testing.T) {
	if os.Getenv("YUDESK_TIMER_TEST") != "1" {
		t.Skip("native runtime timer probe only")
	}
	for _, requested := range []time.Duration{time.Millisecond, 20 * time.Millisecond} {
		samples := make([]time.Duration, 60)
		for i := range samples {
			start := time.Now()
			<-time.NewTimer(requested).C
			samples[i] = time.Since(start)
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		t.Logf("runtime=%s requested=%v p50=%v p95=%v max=%v", runtime.Version(), requested, samples[29], samples[56], samples[59])
	}
}
