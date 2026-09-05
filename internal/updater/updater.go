package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxDownloadSize = 256 << 20

type Manifest struct {
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"publishedAt"`
	Files       []File    `json:"files"`
	Signature   string    `json:"signature"`
}

type File struct {
	Component string `json:"component"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
}

func SigningPayload(manifest Manifest) ([]byte, error) {
	manifest.Signature = ""
	return json.Marshal(manifest)
}

func Verify(data []byte, publicKey ed25519.PublicKey) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("decode update manifest: %w", err)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return manifest, errors.New("invalid update signing public key")
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return manifest, errors.New("invalid update manifest signature")
	}
	payload, err := SigningPayload(manifest)
	if err != nil {
		return manifest, err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return manifest, errors.New("update manifest signature verification failed")
	}
	if strings.TrimSpace(manifest.Version) == "" || len(manifest.Files) == 0 {
		return manifest, errors.New("update manifest is incomplete")
	}
	return manifest, nil
}

func Select(manifest Manifest, component, goos, goarch string) (File, error) {
	for _, file := range manifest.Files {
		if file.Component == component && file.OS == goos && file.Arch == goarch {
			if _, err := expectedHash(file.SHA256); err != nil {
				return File{}, err
			}
			return file, nil
		}
	}
	return File{}, fmt.Errorf("no update for %s on %s/%s", component, goos, goarch)
}

func Fetch(ctx context.Context, rawURL string, allowHTTP bool) ([]byte, error) {
	parsed, err := validateURL(rawURL, allowHTTP)
	if err != nil {
		return nil, err
	}
	client := secureHTTPClient(allowHTTP)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, errors.New("update manifest is too large")
	}
	return data, nil
}

// Install downloads one verified manifest entry and atomically places it at
// destination. An existing binary is retained as a timestamped backup.
func Install(ctx context.Context, file File, destination string, allowHTTP bool) (string, error) {
	parsed, err := validateURL(file.URL, allowHTTP)
	if err != nil {
		return "", err
	}
	expected, err := expectedHash(file.SHA256)
	if err != nil {
		return "", err
	}
	absDestination, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	directory := filepath.Dir(absDestination)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(directory, ".yudesk-update-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		_ = temporary.Close()
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	response, err := secureHTTPClient(allowHTTP).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %s", response.Status)
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, maxDownloadSize+1))
	if err != nil {
		return "", err
	}
	if written > maxDownloadSize {
		return "", errors.New("update file is too large")
	}
	if !equalBytes(hash.Sum(nil), expected) {
		return "", errors.New("update SHA-256 does not match signed manifest")
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(temporaryPath, 0755); err != nil {
		return "", err
	}
	backup := ""
	if _, err := os.Stat(absDestination); err == nil {
		backup = fmt.Sprintf("%s.backup-%d", absDestination, time.Now().Unix())
		if err := os.Rename(absDestination, backup); err != nil {
			return "", fmt.Errorf("move current binary (stop it before updating): %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(temporaryPath, absDestination); err != nil {
		if backup != "" {
			_ = os.Rename(backup, absDestination)
		}
		return "", err
	}
	keepTemporary = true
	return backup, nil
}

func validateURL(rawURL string, allowHTTP bool) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "https" && parsed.Host != "" {
		return parsed, nil
	}
	if allowHTTP && parsed.Scheme == "http" && parsed.Host != "" {
		return parsed, nil
	}
	return nil, errors.New("update URLs must use HTTPS")
}

func secureHTTPClient(allowHTTP bool) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			_, err := validateURL(request.URL.String(), allowHTTP)
			return err
		},
	}
}

func expectedHash(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("invalid update SHA-256")
	}
	return decoded, nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}
