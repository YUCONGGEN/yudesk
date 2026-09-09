package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"html/template"
	"image/png"
	"net/http"
	"strings"
	"time"
)

// Keep the website self-contained. Only the explicit public assets below are
// served; HTML sources and any other files in site are never downloadable.
//
//go:embed site/homepage.css site/homepage.js site/desktop-home.png site/desktop-devices.png site/desktop-approval.png site/beian.svg
var homepageFiles embed.FS

//go:embed site/homepage.html
var downloadPageTemplate string

type homepageAsset struct {
	data        []byte
	contentType string
	version     string
	width       int
	height      int
}

var homepageAssets = loadHomepageAssets()

func loadHomepageAssets() map[string]homepageAsset {
	assets := make(map[string]homepageAsset)
	for _, name := range []string{"homepage.css", "homepage.js", "desktop-home.png", "desktop-devices.png", "desktop-approval.png", "beian.svg"} {
		data, err := homepageFiles.ReadFile("site/" + name)
		if err != nil {
			continue
		}
		asset := homepageAsset{data: data, contentType: "text/css; charset=utf-8", version: fmt.Sprintf("%x", sha256.Sum256(data))[:16]}
		if name == "homepage.js" {
			asset.contentType = "text/javascript; charset=utf-8"
		} else if name == "beian.svg" {
			asset.contentType = "image/svg+xml"
		} else if strings.HasSuffix(name, ".png") {
			config, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				continue
			}
			asset.contentType, asset.width, asset.height = "image/png", config.Width, config.Height
		}
		assets[name] = asset
	}
	return assets
}

func homepageAssetURL(name string) string {
	if asset, ok := homepageAssets[name]; ok {
		return "/site/" + name + "?v=" + asset.version
	}
	return ""
}

// The template falls back to a DOM placeholder if an image fails validation.
func homepageScreenshot(name string) map[string]any {
	asset, ok := homepageAssets[name]
	if !ok || asset.contentType != "image/png" {
		return nil
	}
	return map[string]any{"URL": homepageAssetURL(name), "Width": asset.width, "Height": asset.height}
}

var homepageTemplateFuncs = template.FuncMap{
	"siteURL":    homepageAssetURL,
	"screenshot": homepageScreenshot,
}

func registerHomepageSite(mux *http.ServeMux) {
	mux.HandleFunc("/site/", serveHomepageSite)
}

func serveHomepageSite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Exact matching excludes directory listings, traversal and private files.
	asset, ok := homepageAssets[strings.TrimPrefix(r.URL.Path, "/site/")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", asset.contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", `"`+asset.version+`"`)
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	if r.URL.Query().Get("v") == asset.version {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, r.URL.Path, time.Time{}, bytes.NewReader(asset.data))
}
