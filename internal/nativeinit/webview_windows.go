//go:build windows

package nativeinit

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"

	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

var (
	webViewOnce sync.Once
	webViewErr  error
	webViewDLL  *windows.DLL
)

// Prepare extracts the Microsoft loader to a private, stable location before
// go-webview2 initializes COM. Keeping the DLL handle alive prevents another
// process from replacing the loader while the installer is open.
func Prepare(directory string) error {
	webViewOnce.Do(func() {
		dir := filepath.Join(directory, "runtime")
		if err := os.MkdirAll(dir, 0700); err != nil {
			webViewErr = err
			return
		}
		path := filepath.Join(dir, "WebView2Loader.dll")
		current, _ := os.ReadFile(path)
		if !bytes.Equal(current, webviewloader.WebView2Loader) {
			if err := os.WriteFile(path, webviewloader.WebView2Loader, 0600); err != nil {
				webViewErr = err
				return
			}
		}
		webViewDLL, webViewErr = windows.LoadDLL(path)
	})
	return webViewErr
}
