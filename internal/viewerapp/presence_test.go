package viewerapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func onlineTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"devices":[{"id":%q,"online":true,"active":true,"ready":true}]}`, r.URL.Query().Get("ids"))
	}))
	t.Cleanup(s.Close)
	return s
}

func TestPresenceRefusesOfflineBusyUnlicensedAndPreparing(t *testing.T) {
	id := strings.Repeat("A", 24)
	for _, state := range []string{`"online":false`, `"online":true,"active":false`, `"online":true,"active":true,"connected":true`, `"online":true,"active":true,"ready":false`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"devices":[{"id":%q,%s}]}`, id, state) }))
		if err := checkViewerPresence(context.Background(), s.URL, id); err == nil {
			t.Errorf("accepted %s", state)
		}
		s.Close()
	}
	if err := checkViewerPresence(context.Background(), onlineTestServer(t).URL, id); err != nil {
		t.Fatal(err)
	}
}
