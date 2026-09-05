package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "yudesk", "session-token"), nil
}
func Load() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("saved session token is empty")
	}
	return token, nil
}
func Save(token string) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.TrimSpace(token)), 0600)
}
func Clear() error {
	path, err := Path()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
