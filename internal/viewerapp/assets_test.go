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
		for _, want := range []string{"遇到问题联系", "17739798184", "低延迟高响应", "皖ICP备20003241号-3", `href="https://beian.miit.gov.cn/"`, `rel="noopener noreferrer"`} {
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
	for _, forbidden := range []string{"yudesk.meetingName", "rememberMeetingName", "meetingHostName').value=$('meetingJoinName"} {
		if strings.Contains(dashboardJS, forbidden) {
			t.Errorf("meeting name must not be restored or copied automatically: found %q", forbidden)
		}
	}
	if strings.Count(page, `autocomplete="off"`) < 3 || !strings.Contains(dashboardJS, "['meetingHostName','meetingJoinName'])$(id).value=''") {
		t.Error("meeting name fields must start empty with browser autofill disabled")
	}
}

func TestDashboardContainsManagedPortMapping(t *testing.T) {
	var output bytes.Buffer
	if err := dashboardPage.Execute(&output, map[string]any{"Token": "test-token", "Version": "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	page := output.String() + dashboardJS + portMapCSS
	for _, expected := range []string{"dataset.tab='ports'", "端口映射", "9000–9500", "最多同时开启 5 个", "禁止非法行为", "/api/local/port-map/create", "/api/local/port-map/delete"} {
		if !strings.Contains(page, expected) {
			t.Errorf("port mapping dashboard does not contain %q", expected)
		}
	}
}

func TestDashboardUsesNativeCloseToTrayOnWindows(t *testing.T) {
	var windowsPage bytes.Buffer
	if err := dashboardPage.Execute(&windowsPage, map[string]any{"Token": "test-token", "Windows": true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(windowsPage.String(), `id="closeToTray"`) || strings.Contains(windowsPage.String(), `action="/exit?`) {
		t.Fatal("Windows dashboard must close to tray without rendering an exit form")
	}
	if !strings.Contains(dashboardJS, "/api/ui/close-to-tray") {
		t.Fatal("Windows close control is not wired to the local lifecycle endpoint")
	}

	var otherPage bytes.Buffer
	if err := dashboardPage.Execute(&otherPage, map[string]any{"Token": "test-token", "Windows": false}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(otherPage.String(), `action="/exit?`) {
		t.Fatal("non-Windows dashboard lost its explicit close action")
	}
}
