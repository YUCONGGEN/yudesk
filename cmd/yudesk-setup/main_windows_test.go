//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestInstallerOffersCompactCustomDirectory(t *testing.T) {
	for _, want := range []string{"自定义安装目录", `id="directory"`, "{{DIRECTORY}}", "beginInstall(target)", "安装不会删除已有设备码或连接记录"} {
		if !strings.Contains(setupHTML, want) {
			t.Errorf("installer UI is missing %q", want)
		}
	}
	if strings.Contains(setupHTML, "alert(") || strings.Contains(setupHTML, "confirm(") {
		t.Fatal("installer contains a browser-default dialog")
	}
}
