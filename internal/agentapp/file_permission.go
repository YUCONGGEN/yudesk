package agentapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/yudesk/yudesk/internal/filetransfer"
	"github.com/yudesk/yudesk/internal/protocol"
)

func (a *agent) configureFilePermission(identityDir string) error {
	if a.shareDir != "" {
		root, err := filepath.Abs(a.shareDir)
		if err != nil {
			return err
		}
		a.shareDir = root // Explicit CLI root is pre-authorized by its local owner.
		return nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	a.shareDir = filepath.Join(homeDir, "Downloads", "YuDesk")
	a.fileConsent = true
	a.filePermissionPath = filepath.Join(identityDir, "file-permission.json")
	data, err := os.ReadFile(a.filePermissionPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved struct {
		Enabled bool `json:"enabled"`
	}
	if json.Unmarshal(data, &saved) != nil {
		return errors.New("文件传输授权配置损坏，请在被控端重新设置")
	}
	if saved.Enabled {
		if err := prepareReceiveDirectory(a.shareDir); err != nil {
			return err
		}
		a.fileEnabled = true
	}
	return nil
}

func prepareReceiveDirectory(root string) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("文件传输目录必须是普通目录，不能是链接")
	}
	return nil
}

func (a *agent) fileRoot() string {
	a.fileMu.RLock()
	defer a.fileMu.RUnlock()
	if a.fileConsent && !a.fileEnabled {
		return ""
	}
	return a.shareDir
}

func (a *agent) setFilePermission(enabled bool) error {
	a.fileMu.Lock()
	defer a.fileMu.Unlock()
	if enabled {
		if a.shareDir == "" {
			return errors.New("文件传输目录未配置")
		}
		if err := prepareReceiveDirectory(a.shareDir); err != nil {
			return err
		}
	}
	if a.filePermissionPath != "" {
		data, _ := json.Marshal(struct {
			Enabled bool `json:"enabled"`
		}{enabled})
		f, err := os.CreateTemp(filepath.Dir(a.filePermissionPath), ".file-permission-*")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(name, a.filePermissionPath); err != nil {
			return err
		}
	}
	a.fileConsent, a.fileEnabled = true, enabled
	a.fileEpoch++
	return nil
}

func (a *agent) syncFilePermission(files *filetransfer.Manager, epoch *uint64) {
	a.fileMu.RLock()
	defer a.fileMu.RUnlock()
	if *epoch != a.fileEpoch {
		_ = files.Reset()
		*epoch = a.fileEpoch
	}
}

func (a *agent) handleFileRequest(files *filetransfer.Manager, epoch *uint64, m protocol.Message) ([]byte, map[string]any, error) {
	// A permission change cannot acknowledge completion while a previously
	// authorized operation is still publishing a file. Every later operation
	// sees the new epoch, even if permission was switched off then on quickly.
	a.fileMu.RLock()
	defer a.fileMu.RUnlock()
	if *epoch != a.fileEpoch {
		_ = files.Reset()
		*epoch = a.fileEpoch
	}
	root := a.shareDir
	if a.fileConsent && !a.fileEnabled {
		root = ""
	}
	data, meta, err := files.Handle(root, m.Method, m.Params, m.Data)
	var failure *filetransfer.Error
	if errors.As(err, &failure) {
		err = fmt.Errorf("%s: %s", failure.Code, failure.Message)
	}
	return data, meta, err
}

func (a *agent) registerFilePermission(mux *http.ServeMux, token string) {
	mux.HandleFunc("/files/permission", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil {
			http.Error(w, "请求无效", 400)
			return
		}
		if err := a.setFilePermission(r.FormValue("enabled") == "1"); err != nil {
			http.Error(w, "修改文件授权失败："+err.Error(), 400)
			return
		}
		http.Redirect(w, r, "/?access_token="+url.QueryEscape(token), http.StatusSeeOther)
	})
}
