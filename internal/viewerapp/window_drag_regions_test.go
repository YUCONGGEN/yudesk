package viewerapp

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWindowDragRegionAPI(t *testing.T) {
	h, err := newViewerHost("127.0.0.1:0", "drag-api-test")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, tc := range []struct {
		name, method, token, body string
		want                      int
	}{
		{"unauthorized", "POST", "wrong", `{}`, 403},
		{"method", "GET", "drag-api-test", "", 405},
		{"invalid", "POST", "drag-api-test", `{}`, 400},
		{"oversize", "POST", "drag-api-test", strings.Repeat(" ", 9000), 400},
		{"window-not-started", "POST", "drag-api-test", `{"width":860,"height":600,"scale":1,"rects":[]}`, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://127.0.0.1/api/ui/drag-regions?access_token="+tc.token, strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			h.server.Handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want %d", w.Code, tc.want)
			}
		})
	}
}

func TestWindowDragRegionValidation(t *testing.T) {
	good := `{"width":860,"height":600,"scale":1.25,"rects":[{"x":152,"y":0,"width":540,"height":44}]}`
	value, err := readWindowDragRegions(strings.NewReader(good))
	if err != nil || len(value.Rects) != 1 || value.received.IsZero() {
		t.Fatalf("valid regions: %v / %v", value, err)
	}
	for name, body := range map[string]string{
		"trailing": good + `{}`, "garbage": good + `x`, "viewport": `{"width":0,"height":600,"scale":1}`,
		"invalid-json": `{`, "scale": strings.Replace(good, `1.25`, `20`, 1),
		"outside":    strings.Replace(good, `"width":540`, `"width":900`, 1),
		"not-header": strings.Replace(good, `"y":0`, `"y":300`, 1),
		"negative":   strings.Replace(good, `"x":152`, `"x":-1`, 1),
		"nonfinite":  strings.Replace(good, `1.25`, `1e999`, 1),
		"oversize":   good + strings.Repeat(" ", 8193),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readWindowDragRegions(strings.NewReader(body)); err == nil {
				t.Fatal("accepted invalid regions")
			}
		})
	}
	value.Rects = make([]windowDragRect, 17)
	data, _ := json.Marshal(value)
	if _, err = readWindowDragRegions(strings.NewReader(string(data))); err == nil {
		t.Fatal("accepted too many regions")
	}
	if _, err = readWindowDragRegions(strings.NewReader(`{"width":860,"height":600,"scale":1,"rects":[]}`)); err != nil {
		t.Fatal(err)
	}
}
