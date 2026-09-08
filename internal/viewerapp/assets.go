package viewerapp

import (
	_ "embed"
	"net/http"
)

//go:embed ui/session.html
var indexHTML string

//go:embed ui/session.css
var sessionCSS string

//go:embed ui/launcher.html
var launcherHTML string

//go:embed ui/input.js
var inputJS string

//go:embed ui/session.js
var sessionJS string

//go:embed ui/frames.js
var framesJS string

//go:embed ui/files.js
var filesJS string

//go:embed ui/audio.js
var audioJS string

//go:embed ui/icon.svg
var appIcon []byte

//go:embed ui/footer.html
var footerHTML string

//go:embed ui/footer.css
var footerCSS []byte

//go:embed ui/beian.svg
var beianIcon []byte

//go:embed ui/window-ui.js
var windowUI []byte

//go:embed ui/window-ui.css
var windowStyle []byte

func serveBrandAsset(w http.ResponseWriter, r *http.Request) bool {
	var data []byte
	switch r.URL.Path {
	case "/assets/window-ui.css":
		data = windowStyle
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case "/assets/window-ui.js":
		data = windowUI
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case "/assets/footer.css":
		data = footerCSS
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case "/assets/beian.svg":
		data = beianIcon
		w.Header().Set("Content-Type", "image/svg+xml")
	default:
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
	return true
}

func serveAppIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(appIcon)
}

func registerViewerAssets(mux *http.ServeMux) {
	for _, path := range []string{"/assets/footer.css", "/assets/beian.svg", "/assets/window-ui.js", "/assets/window-ui.css"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { serveBrandAsset(w, r) })
	}
	mux.HandleFunc("/assets/icon.svg", serveAppIcon)
	mux.HandleFunc("/assets/session.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(sessionCSS))
	})
	for path, source := range map[string]string{"/assets/input.js": inputJS, "/assets/session.js": sessionJS, "/assets/frames.js": framesJS, "/assets/files.js": filesJS, "/assets/audio.js": audioJS} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(source))
		})
	}
}
