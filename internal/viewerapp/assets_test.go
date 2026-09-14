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
	for _, path := range []string{"/assets/footer.css", "/assets/beian.svg", "/assets/product-theme.css"} {
		w := httptest.NewRecorder()
		if !serveBrandAsset(w, httptest.NewRequest(http.MethodGet, path, nil)) || w.Body.Len() == 0 {
			t.Fatalf("missing local asset %s", path)
		}
	}
}

func TestApplicationDialogsAreCustomStyled(t *testing.T) {
	scripts := dashboardJS + string(windowUI) + sessionJS
	for _, forbidden := range []string{"alert(", "confirm(", "prompt("} {
		if strings.Contains(scripts, forbidden) {
			t.Fatalf("application UI contains browser-native dialog call %q", forbidden)
		}
	}
	styles := dashboardCSS + string(windowStyle) + sessionCSS + conferenceCSS + string(productTheme)
	for _, expected := range []string{".yu-dialog-danger", ".app-form-dialog", ".dialog-error", ".session-dialog", ".conference-member-info", ".conference-info-mark", "backdrop-filter", "body[data-install-prompt]", "body[data-control][data-audio]"} {
		if !strings.Contains(styles, expected) {
			t.Errorf("custom dialog styling missing %q", expected)
		}
	}
}

func TestDashboardContainsMultipartyMeetingFlow(t *testing.T) {
	var output bytes.Buffer
	if err := dashboardPage.Execute(&output, map[string]any{"Token": "test-token", "Version": "2.0.0"}); err != nil {
		t.Fatal(err)
	}
	page := output.String() + dashboardJS + dashboardCSS + conferenceCSS
	for _, expected := range []string{"data-tab=\"meeting\"", "快速会议", "9 位会议号", "请输入姓名", "自己立即进入", "会议主题", "meetingTopic", "meetingHostStatus", "填写主题和姓名即可创建会议", "validMeetingTopic", "/api/local/meeting/start", "/api/local/meeting/end", "/api/local/meeting/resolve", "/api/local/conference", "RTCPeerConnection", "getUserMedia", "getDisplayMedia", "MediaRecorder", "转让主持人", "移出会议", "conferenceMemberSearch", "conference-grid-focused", "conferenceToast", "conferenceShareFailure", "preferredConferenceSharer", "transient user activation", `id="conferenceMinimize"`, `aria-label="最小化会议"`} {
		if !strings.Contains(page, expected) {
			t.Errorf("meeting dashboard does not contain %q", expected)
		}
	}
	if strings.Count(dashboardJS, "Conference screen-sharing lifecycle") != 1 {
		t.Fatal("meeting-specific screen sharing script must be appended exactly once")
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

func TestDashboardCloseBehaviorMatchesEveryDesktop(t *testing.T) {
	if !strings.Contains(string(windowUI), "'/api/ui/close-to-tray'") {
		t.Fatal("desktop close control is not wired to the local lifecycle endpoint")
	}
	var output bytes.Buffer
	if err := dashboardPage.Execute(&output, map[string]any{"Token": "test-token", "Version": "2.0.0", "Build": "abcdef012345"}); err != nil {
		t.Fatal(err)
	}
	page := output.String()
	for _, expected := range []string{`id="closeToTray"`, `data-window-action="close-to-tray"`, `aria-label="关闭窗口"`, "关闭窗口并继续在线", "构建 abcdef012345"} {
		if !strings.Contains(page, expected) {
			t.Errorf("desktop dashboard missing %q", expected)
		}
	}
	if strings.Contains(page, `action="/exit?`) || strings.Contains(page, "if .Windows") {
		t.Fatal("desktop dashboard close behavior diverged by platform")
	}
}
