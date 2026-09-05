package stream

type Options struct {
	FPS      int    `json:"fps"`
	Quality  int    `json:"quality"`
	Mode     string `json:"mode"`
	MaxWidth int    `json:"maxWidth"`
	MaxMbps  int    `json:"maxMbps"`
	SaveIdle bool   `json:"saveIdle"`
	FrameAck bool   `json:"frameAck"`
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
