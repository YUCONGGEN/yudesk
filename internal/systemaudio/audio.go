package systemaudio

import "context"

// Format describes the uncompressed little-endian PCM stream sent to the
// viewer. A fixed format keeps browser playback small and predictable.
type Format struct {
	SampleRate int
	Channels   int
	Bits       int
}

// Sink must consume or copy data before it returns.
type Sink func(format Format, data []byte) error

// Capture streams the operating system's playback mix until ctx is canceled.
func Capture(ctx context.Context, sink Sink) error { return capture(ctx, sink) }

// Available reports whether this build has a usable native/external backend.
func Available() (bool, string) { return available() }
