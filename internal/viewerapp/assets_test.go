package viewerapp

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFooterPresentAndOfflineAssets(t *testing.T) {
	for _, page := range []*template.Template{dashboardPage, page} {
		var out bytes.Buffer
		if err := page.Execute(&out, map[string]any{"Token": "test-token"}); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"设计者：郁从根", "17739798184", "低延迟高响应", "皖ICP备20003241号-3", `href="https://beian.miit.gov.cn/"`, `rel="noopener noreferrer"`} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing %s", want)
			}
		}
	}
	for _, path := range []string{"/assets/footer.css", "/assets/beian.svg"} {
		w := httptest.NewRecorder()
		if !serveBrandAsset(w, httptest.NewRequest(http.MethodGet, path, nil)) || w.Body.Len() == 0 {
			t.Fatalf("missing local asset %s", path)
		}
	}
}

func TestDashboardContainsMultipartyMeetingFlow(t *testing.T) {
	var output bytes.Buffer
	if err := dashboardPage.Execute(&output, map[string]any{"Token": "test-token", "Version": "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	page := output.String() + dashboardJS + dashboardCSS + conferenceCSS
	for _, expected := range []string{"data-tab=\"meeting\"", "快速会议", "9 位会议号", "请输入姓名", "自己立即进入", "/api/local/meeting/start", "/api/local/meeting/end", "/api/local/meeting/resolve", "/api/local/conference", "RTCPeerConnection", "getUserMedia", "getDisplayMedia", "MediaRecorder", "转让主持人", "移出会议", "conferenceMemberSearch", "conference-grid-focused"} {
		if !strings.Contains(page, expected) {
			t.Errorf("meeting dashboard does not contain %q", expected)
		}
	}
}
