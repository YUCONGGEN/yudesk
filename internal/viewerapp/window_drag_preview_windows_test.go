//go:build windows

package viewerapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"regexp"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// An explicitly enabled, local-only fixture for user-authorized real dragging.
// Uses real page headers, CSS, window-ui.js and the host, but no device identity,
// remote connection, service installation, permissions or remote input handlers.
func TestNativeWindowDragPreview(t *testing.T) {
	if os.Getenv("YUDESK_DRAG_PREVIEW") != "1" {
		t.Skip("opt-in local-only real drag verification")
	}
	h, err := newViewerHost("127.0.0.1:0", "drag-fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	h.window.profile = t.TempDir()
	h.window.candidates = []string{os.Getenv("YUDESK_TEST_BROWSER")}
	l, err := listenViewerPage("", h)
	if err != nil {
		t.Fatal(err)
	}
	scripts := regexp.MustCompile(`(?s)<script\b[^>]*>.*?</script>`)
	attachViewerPage(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/assets/dashboard.css", "/assets/session.css":
			w.Header().Set("Content-Type", "text/css")
			if r.URL.Path == "/assets/dashboard.css" {
				fmt.Fprint(w, dashboardCSS)
			} else {
				fmt.Fprint(w, sessionCSS)
			}
			return
		case "/fixture/state":
			h.window.mu.Lock()
			defer h.window.mu.Unlock()
			var rect nativeRect
			hwnd := h.window.nativeHandle()
			nativeGetRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
			if n := h.window.native; n != nil {
				if regions := n.dragRegions.Load(); regions != nil && len(regions.Rects) > 0 {
					area := regions.Rects[0]
					x, y := int((area.X+area.Width/2)*regions.Scale), int((area.Y+area.Height/2)*regions.Scale)
					title := syscall.StringToUTF16Ptr(fmt.Sprintf("YuDesk 拖动测试 | 位置 %d,%d | 起点 %d,%d 终点 %d,%d | %s", rect.Left, rect.Top, x, y, x+90, y+60, r.URL.Query().Get("nav")))
					windowUser32.NewProc("SetWindowTextW").Call(hwnd, uintptr(unsafe.Pointer(title)))
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"x": rect.Left, "y": rect.Top, "w": rect.Right - rect.Left, "h": rect.Bottom - rect.Top})
			return
		case "/api/local/approval":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"pending":null}`)
			return
		case "/api/exit", "/fixture/done":
			w.WriteHeader(http.StatusNoContent)
			h.cancel()
			return
		}
		var html string
		switch r.URL.Path {
		case "/fixture/session":
			html = indexHTML
		case "/fixture/transition":
			html = `<!doctype html><meta charset="utf-8"><body data-token="{{.Token}}"><p id="notice">YuDesk 连接过渡页拖动测试</p></body>`
		default:
			html = dashboardHTML
		}
		html = scripts.ReplaceAllString(html, "")
		var body bytes.Buffer
		if err := template.Must(template.New("drag-fixture").Parse(html+footerHTML)).Execute(&body, map[string]any{"Token": "drag-fixture", "Version": "测试窗口", "Control": false}); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body.String())
		fmt.Fprint(w, `<div style="position:fixed;left:20px;bottom:30px;z-index:10;background:#fff;border:1px solid #1478f7;padding:8px;font:13px system-ui"><b>仅测试本机窗口拖动</b> <a href="/?access_token=drag-fixture">主界面</a> · <a href="/fixture/session?access_token=drag-fixture">远程界面</a> · <a href="/fixture/transition?access_token=drag-fixture">过渡页</a><span id="geometry"></span></div><script src="/assets/window-ui.js?access_token=drag-fixture"></script><script>setInterval(async()=>{const nav=Array.from(document.querySelectorAll('a[href*=drag-fixture]')).map(a=>{const b=a.getBoundingClientRect();return a.textContent+'='+Math.round((b.x+b.width/2)*devicePixelRatio)+','+Math.round((b.y+b.height/2)*devicePixelRatio)}).join(' ');const r=await fetch('/fixture/state?access_token=drag-fixture&nav='+encodeURIComponent(nav));const s=await r.json();const bar=document.querySelector('[data-window-drag]')?.getBoundingClientRect();let point='';if(bar){const x=Math.round((bar.x+70)*devicePixelRatio),y=Math.round((bar.y+bar.height/2)*devicePixelRatio);point='；本窗口拖动起点 '+x+','+y+' 终点 '+(x+110)+','+(y+90);}document.getElementById('geometry').textContent=' 位置 '+s.x+','+s.y+' · '+s.w+'×'+s.h+point;},200);</script>`)
	}))
	if err := h.window.Show(); err != nil {
		t.Fatal(err)
	}
	t.Logf("Local drag fixture ready: %s", h.window.url)
	select {
	case <-h.ctx.Done():
	case <-time.After(10 * time.Minute):
	}
}
