//go:build windows

package viewerapp

import (
	"reflect"
	"testing"
)

func TestWindowDragCaptureAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capture    uintptr
		pressed    bool
		cancelWins bool
		releaseOK  bool
		want       []string
	}{
		{"no capture", 0, true, false, true, []string{"move"}},
		{"browser capture releases on cancel", 1, true, true, true, []string{"cancel", "move"}},
		{"shared capture needs release on GUI thread", 1, true, false, true, []string{"cancel", "release", "move"}},
		{"foreign capture untouched", 2, true, false, true, nil},
		{"stuck capture cannot begin move", 1, true, false, false, []string{"cancel", "release"}},
		{"late pointerdown after release is ignored", 1, false, false, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := tc.capture
			var actions []string
			moved := beginWindowDrag(windowDragOps{
				valid: func() bool { return true }, pressed: func() bool { return tc.pressed },
				cursor:  func() (nativePoint, bool) { return nativePoint{-500, 40}, true },
				capture: func() uintptr { return capture }, owned: func(value uintptr) bool { return value == 1 },
				cancel: func(value uintptr) {
					actions = append(actions, "cancel")
					if tc.cancelWins {
						capture = 0
					}
				},
				release: func() {
					actions = append(actions, "release")
					if tc.releaseOK {
						capture = 0
					}
				},
				start: func(point nativePoint) {
					if point.X != -500 || capture != 0 {
						t.Fatal("invalid move state")
					}
					actions = append(actions, "move")
				},
			})
			if !reflect.DeepEqual(actions, tc.want) {
				t.Fatalf("actions %v, want %v", actions, tc.want)
			}
			if moved != (len(tc.want) > 0 && tc.want[len(tc.want)-1] == "move") {
				t.Fatal("wrong move result")
			}
		})
	}
}

func TestWindowDragRechecksReleaseAndWindow(t *testing.T) {
	for _, closing := range []bool{false, true} {
		valid, pressed := true, true
		capture := uintptr(1)
		if beginWindowDrag(windowDragOps{
			valid: func() bool { return valid }, pressed: func() bool { return pressed },
			cursor:  func() (nativePoint, bool) { return nativePoint{}, true },
			capture: func() uintptr { return capture }, owned: func(uintptr) bool { return true },
			cancel: func(uintptr) {
				capture = 0
				if closing {
					valid = false
				} else {
					pressed = false
				}
			},
			release: func() { t.Fatal("already released") }, start: func(nativePoint) { t.Fatal("late move") },
		}) {
			t.Fatal("cancelled drag reported success")
		}
	}
}

func TestWindowDragCoordinates(t *testing.T) {
	for _, p := range []nativePoint{{-1200, -400}, {1920, 1080}, {0, 0}} {
		packed := packWindowPoint(p)
		if int32(int16(packed&0xffff)) != p.X || int32(int16((packed>>16)&0xffff)) != p.Y {
			t.Fatal("lost signed multi-monitor position", p)
		}
	}
}
