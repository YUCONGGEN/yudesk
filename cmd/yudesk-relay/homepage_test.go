package main

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func homepageTestDownloads(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, platform := range downloadLayout {
		for _, link := range platform.Links {
			path := filepath.Join(root, filepath.FromSlash(link.Path))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("homepage test download"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, content := range map[string]string{
		"release.json":            `{"version":"2.0.0","publishedAt":"2026-09-08T00:00:00Z"}`,
		"SHA256SUMS.txt":          "test checksum fixture\n",
		"THIRD_PARTY_NOTICES.txt": "third-party license fixture\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestHomepageContentAndDownloads(t *testing.T) {
	root := homepageTestDownloads(t)
	w := httptest.NewRecorder()
	serveDownloadHome(w, root)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("home status/cache: %d %v", w.Code, w.Header())
	}
	for _, want := range []string{
		"v2.0.0", "2026-09-08 08:00", "（北京时间）", "Android 预览版",
		`id="downloads"`, `id="about-title"`, `id="features"`, `id="how-it-works"`, `id="support"`,
		`aria-label="服务器实时状态"`, `id="live-online">0`, `id="live-connected">0`, "每 30 秒自动更新",
		`src="` + homepageAssetURL("homepage.js") + `"`,
		`href="/guide"`, `href="/admin"`, `href="/SHA256SUMS.txt"`, "安装包 SHA-256 校验和",
		`href="/THIRD_PARTY_NOTICES.txt"`, "第三方许可",
		"真实客户端界面，设备信息为演示数据", "非实测截图", "尚未实现系统声音采集",
		"Android 文件传输暂不支持，系统声音采集尚未实现", "真实锁屏场景尚未主动测试", "管理员可查看设备上报的 PIN", "不受 HTTPS 保护",
		"低延迟高响应", `class="icp-link"`, homepageAssetURL("beian.svg"),
		"遇到问题联系", "17739798184", "皖ICP备20003241号-3", `href="https://beian.miit.gov.cn/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("homepage missing %q", want)
		}
	}
	if strings.Index(body, `id="downloads"`) > strings.Index(body, `id="about-title"`) {
		t.Error("downloads must precede product content")
	}
	for _, platform := range downloadPlatforms(root) {
		for _, link := range platform.Links {
			want := `href="/download/` + link.Path + `?v=` + link.Version + `"`
			if !strings.Contains(body, want) {
				t.Errorf("download URL was changed: %s", want)
			}
		}
	}
	for _, unwanted := range []string{".smoke/", "unified-session.png", "<script>", "cdn.", "fonts.googleapis", "#ZgotmplZ"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("unexpected home content: %s", unwanted)
		}
	}
}

func TestHomepageAbsentReleaseAndDownloads(t *testing.T) {
	w := httptest.NewRecorder()
	serveDownloadHome(w, t.TempDir())
	body := w.Body.String()
	if !strings.Contains(body, "待发布") || !strings.Contains(body, "Android 预览版") {
		t.Fatal("missing release state or Android preview slot")
	}
	if strings.Contains(body, `href="/download/`) || strings.Contains(body, `href="/SHA256SUMS.txt"`) || strings.Contains(body, "v2.0.0") {
		t.Fatal("unavailable downloads or a made-up release were shown")
	}
	if strings.Count(body, `class="missing"`) != len(downloadLayout) {
		t.Fatal("missing download status not shown for every platform")
	}
}

func TestHomepageScreenshotFallback(t *testing.T) {
	page := template.Must(template.New("no-screenshots").Funcs(template.FuncMap{
		"siteURL":    homepageAssetURL,
		"screenshot": func(string) any { return nil },
	}).Parse(downloadPageTemplate))
	var out bytes.Buffer
	if err := page.Execute(&out, map[string]any{"PublishedAt": "待发布"}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), `class="screenshot-placeholder"`) != 3 || strings.Contains(out.String(), `class="screenshot-link"`) {
		t.Fatal("missing screenshots must render a DOM placeholder, never a broken image")
	}
}

func TestHomepageSiteRoutes(t *testing.T) {
	mux := http.NewServeMux()
	registerDownloadRoutes(mux, t.TempDir(), func(*http.Request) guideConfig { return guideConfig{} })
	handler := securityHeaders(mux)
	for _, name := range []string{"homepage.css", "homepage.js", "desktop-home.png", "desktop-devices.png", "desktop-approval.png", "beian.svg"} {
		t.Run(name, func(t *testing.T) {
			asset, ok := homepageAssets[name]
			if !ok {
				t.Fatalf("missing approved embedded asset %s", name)
			}
			url := homepageAssetURL(name)
			get := httptest.NewRecorder()
			handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, url, nil))
			if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), asset.data) || get.Header().Get("Content-Type") != asset.contentType {
				t.Fatalf("asset response mismatch: %d %v", get.Code, get.Header())
			}
			if !strings.Contains(get.Header().Get("Cache-Control"), "immutable") || get.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("versioned asset caching or nosniff missing")
			}
			if !strings.Contains(get.Header().Get("Content-Security-Policy"), "img-src 'self'") {
				t.Fatal("site route bypassed existing security headers")
			}
			if strings.HasSuffix(name, ".png") {
				if asset.width != 1720 || asset.height != 1124 {
					t.Fatalf("unexpected approved screenshot dimensions %dx%d", asset.width, asset.height)
				}
				w := httptest.NewRecorder()
				serveDownloadHome(w, t.TempDir())
				if !strings.Contains(w.Body.String(), `src="`+url+`"`) || !strings.Contains(w.Body.String(), `width="1720" height="1124"`) {
					t.Fatal("approved screenshot not embedded with intrinsic size")
				}
			}
			head := httptest.NewRecorder()
			handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, url, nil))
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
				t.Fatal("HEAD response should describe the asset with no body")
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			req.Header.Set("If-None-Match", get.Header().Get("ETag"))
			cached := httptest.NewRecorder()
			handler.ServeHTTP(cached, req)
			if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
				t.Fatal("conditional request should return 304 without a body")
			}
			for _, unversioned := range []string{"/site/" + name, "/site/" + name + "?v=old"} {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, unversioned, nil))
				if w.Code != http.StatusOK || strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
					t.Fatal("unversioned or stale URL must revalidate")
				}
			}
		})
	}
	for _, path := range []string{"/site/", "/site/homepage.html", "/site/homepage-browser.cjs", "/site/no-such.png", "/site/sub/desktop-home.png", "/site/%2e%2e/main.go", "/site/desktop-home.png/extra", "/api/login"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("non-public path %s returned %d", path, w.Code)
		}
	}
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/site/homepage.css", nil))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal("static site accepts writes")
	}
}

func TestHomepageEmbedExcludesQA(t *testing.T) {
	entries, err := homepageFiles.ReadDir("site")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Fatalf("unexpected production asset count: %d", len(entries))
	}
	for _, entry := range entries {
		if _, ok := homepageAssets[entry.Name()]; !ok || entry.IsDir() {
			t.Errorf("non-public file embedded: %s", entry.Name())
		}
	}
}

func TestHomepageLiveStats(t *testing.T) {
	b := &broker{
		controls: map[string]*deviceControl{
			"CONTROL": {},
			"STOPPED": {stopping: true},
		},
		devices: map[string]waiting{
			"CONTROL": {role: "agent"},
			"WAITING": {role: "agent"},
			"VIEWER":  {role: "viewer"},
		},
		active: map[string]activeSession{
			"WAITING": {},
			"ACTIVE":  {},
		},
	}
	stats := b.homepageStats()
	if stats.OnlineDevices != 3 || stats.ConnectedDevices != 4 || stats.ActiveSessions != 2 {
		t.Fatalf("unexpected public stats: %+v", stats)
	}

	home := httptest.NewRecorder()
	serveDownloadHome(home, homepageTestDownloads(t), stats)
	for _, want := range []string{`id="live-online">3`, `id="live-connected">4`, `id="live-sessions">2 个会话，控制端与被控端合计`} {
		if !strings.Contains(home.Body.String(), want) {
			t.Errorf("homepage missing live statistic %q", want)
		}
	}

	mux := http.NewServeMux()
	registerDownloadRoutes(mux, t.TempDir(), func(*http.Request) guideConfig { return guideConfig{} }, b.homepageStats)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public-stats", nil))
	var decoded homepageStats
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &decoded) != nil || decoded != stats {
		t.Fatalf("unexpected public stats response: status=%d decoded=%+v body=%s", response.Code, decoded, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("public stats response must not be cached")
	}
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/public-stats", nil))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != http.MethodGet {
		t.Fatal("public stats endpoint accepts writes")
	}
}

// Optional local preview fixture for browser QA. It uses only fake download
// files and release metadata, and closes automatically after the test timeout.
func TestHomepagePreview(t *testing.T) {
	addr := os.Getenv("YUDESK_HOMEPAGE_PREVIEW")
	if addr == "" {
		t.Skip("set YUDESK_HOMEPAGE_PREVIEW=127.0.0.1:port for local browser QA")
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatal("preview must bind to loopback")
	}
	mux := http.NewServeMux()
	registerDownloadRoutes(mux, homepageTestDownloads(t), func(*http.Request) guideConfig { return guideConfig{} })
	server := &http.Server{Addr: addr, Handler: securityHeaders(mux)}
	mux.HandleFunc("/site-preview-done", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		go server.Close()
	})
	t.Logf("homepage preview: http://%s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
