package main

import (
	_ "embed"
	"net/http"
)

//go:embed ui/session.html
var indexHTML string

//go:embed ui/launcher.html
var launcherHTML string

//go:embed ui/input.js
var inputJS string

//go:embed ui/session.js
var sessionJS string

func registerViewerAssets(mux *http.ServeMux) {
	for path, source := range map[string]string{"/assets/input.js": inputJS, "/assets/session.js": sessionJS} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(source))
		})
	}
}
