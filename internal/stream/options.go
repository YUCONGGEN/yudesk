package stream

type Options struct {
	ProfileVersion int    `json:"profileVersion,omitempty"`
	FPS            int    `json:"fps"`
	Quality        int    `json:"quality"`
	Mode           string `json:"mode"`
	MaxWidth       int    `json:"maxWidth"`
	MaxMbps        int    `json:"maxMbps"`
	SaveIdle       bool   `json:"saveIdle"`
	FrameAck       bool   `json:"frameAck"`
	TileDelta      bool   `json:"tileDelta,omitempty"` // negotiated capability, never trust a saved preference
}

func (o Options) Normalized() Options {
	o.FPS = min(60, max(1, o.FPS))
	o.Quality = min(90, max(30, o.Quality))
	if o.Mode != "fixed" {
		o.Mode = "adaptive"
	}
	if o.MaxWidth != 0 {
		o.MaxWidth = min(3840, max(640, o.MaxWidth))
	}
	o.MaxMbps = min(100, max(0, o.MaxMbps))
	return o
}
