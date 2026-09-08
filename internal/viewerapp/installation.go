package viewerapp

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/yudesk/yudesk/internal/releaseinfo"
	"github.com/yudesk/yudesk/internal/winhost"
)

type desktopInstallState struct {
	winhost.State
	Platform       string `json:"platform"`
	PackageManaged bool   `json:"packageManaged"`
}

func installedDesktopPath(platform string) string {
	switch platform {
	case "darwin":
		return "/Applications/YuDesk.app/Contents/MacOS/yudesk"
	case "linux":
		return "/usr/lib/yudesk/yudesk"
	default:
		return ""
	}
}

func installedDesktopMarkerPath(platform string) string {
	if platform == "darwin" {
		// macOS treats files in Contents/MacOS as nested executable code.
		// Keep metadata in the resource seal, not alongside signed executables.
		return "/Applications/YuDesk.app/Contents/Resources/yudesk-install.json"
	}
	if platform == "linux" {
		return "/usr/lib/yudesk/yudesk-install.json"
	}
	return ""
}

func desktopInstallationStatus() desktopInstallState {
	s := desktopInstallState{State: winhost.Status(), Platform: runtime.GOOS}
	path := installedDesktopPath(runtime.GOOS)
	if path == "" {
		return s
	}
	s.PackageManaged = true
	s.Supported = true
	s.Path = path
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	helperPath := filepath.Join(filepath.Dir(path), "yudesk-window")
	markerPath := installedDesktopMarkerPath(runtime.GOOS)
	s.Installed = protectedInstallFile(path, true) && protectedInstallFile(helperPath, true) && protectedInstallFile(markerPath, false)
	if s.Installed {
		f, openErr := os.Open(markerPath)
		if openErr != nil {
			s.Installed = false
		} else {
			data, readErr := io.ReadAll(io.LimitReader(f, 4097))
			f.Close()
			s.Installed = readErr == nil && validInstallMarker(data, runtime.GOOS, runtime.GOARCH, path)
		}
	}
	s.TrustedClient = s.Installed && err == nil && exe == path
	s.Running = s.TrustedClient
	s.Ready = s.TrustedClient
	if s.TrustedClient {
		s.Message = "安装版 · 原生无标题栏窗口"
	} else {
		s.Message = "请使用官网安装包完成安装，再从应用菜单打开 YuDesk"
	}
	return s
}

func validInstallMarker(data []byte, platform, architecture, path string) bool {
	if len(data) > 4096 || path == "" || path != installedDesktopPath(platform) {
		return false
	}
	var m struct {
		SchemaVersion   int    `json:"schemaVersion"`
		Product         string `json:"product"`
		Version         string `json:"version"`
		PackageRevision string `json:"packageRevision"`
		Platform        string `json:"platform"`
		Architecture    string `json:"architecture"`
		Installed       bool   `json:"installed"`
		Executable      string `json:"executable"`
		Helper          string `json:"helper"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF {
		return false
	}
	revision, err := strconv.Atoi(m.PackageRevision)
	return err == nil && revision > 0 && m.SchemaVersion == 1 && m.Product == "YuDesk" && m.Version == releaseinfo.Version && m.Platform == platform && m.Architecture == architecture && m.Installed && m.Executable == path && m.Helper == pathpkg.Join(pathpkg.Dir(path), "yudesk-window")
}
