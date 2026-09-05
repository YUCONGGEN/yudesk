package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryPersistsDeduplicatesAndOmitsSecrets(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 35; i++ {
		if err := updateHistory(dir, connectionRecord{DeviceID: fmt.Sprintf("%024X", i), Name: fmt.Sprintf("设备%d", i)}, false); err != nil {
			t.Fatal(err)
		}
	}
	id := fmt.Sprintf("%024X", 12)
	if err := updateHistory(dir, connectionRecord{DeviceID: id, Name: "常用电脑"}, false); err != nil {
		t.Fatal(err)
	}
	items, err := loadHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 30 || items[0].DeviceID != id || items[0].Name != "常用电脑" {
		t.Fatalf("history ordering/count: %+v", items)
	}
	count := 0
	for _, item := range items {
		if item.DeviceID == id {
			count++
		}
	}
	if count != 1 {
		t.Fatal("history contains duplicate device")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "connection-history.json"))
	for _, key := range []string{"pin", "password", "token", "privateKey"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("secret field persisted: %s", key)
		}
	}
	if err := updateHistory(dir, connectionRecord{DeviceID: id}, true); err != nil {
		t.Fatal(err)
	}
	items, _ = loadHistory(dir)
	if len(items) != 29 {
		t.Fatal("delete not persisted")
	}
}

func TestViewerHistoryPageEscapesNames(t *testing.T) {
	var out strings.Builder
	if err := viewerLauncherPage.Execute(&out, map[string]any{"Token": "test-token", "History": []connectionRecord{{DeviceID: strings.Repeat("A", 24), Name: "<script>alert(1)</script>"}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<script>alert") || !strings.Contains(out.String(), "再次连接") {
		t.Fatal("unsafe or missing history UI")
	}
}
