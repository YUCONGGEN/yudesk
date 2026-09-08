// Package releaseinfo is the common desktop/mobile/server product version.
package releaseinfo

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const Version = "2.0.0"

type Manifest struct {
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"publishedAt"`
}

var semanticVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Use explicit publication metadata, never filesystem copy/build timestamps.
func Read(root string) (Manifest, error) {
	f, err := os.Open(filepath.Join(root, "release.json"))
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	var value Manifest
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return Manifest{}, errors.New("release metadata too large or unreadable")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&value); err != nil {
		return Manifest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Manifest{}, errors.New("invalid release metadata")
	}
	if !semanticVersion.MatchString(value.Version) || value.PublishedAt.IsZero() || value.PublishedAt.After(time.Now().Add(5*time.Minute)) {
		return Manifest{}, errors.New("invalid release version or date")
	}
	return value, nil
}

func (m Manifest) Date() string {
	return m.PublishedAt.In(time.FixedZone("UTC+08:00", 8*3600)).Format("2006-01-02 15:04")
}

func Filename(platform, suffix, version string) string {
	if !semanticVersion.MatchString(version) {
		return "yudesk" + suffix
	}
	return "YuDesk-" + version + "-" + platform + suffix
}
