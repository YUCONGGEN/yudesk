package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/yudesk/yudesk/internal/secureconn"
)

type Identity struct {
	ID         string
	PIN        string
	PrivateKey ed25519.PrivateKey
}

// RotatePIN commits a fresh secret before the caller changes its live value.
// Failed writes leave the original PIN and private device identity untouched.
func RotatePIN(configDir, previous string) (string, error) {
	var pin string
	for {
		value, err := rand.Int(rand.Reader, big.NewInt(1000000))
		if err != nil {
			return "", err
		}
		pin = fmt.Sprintf("%06d", value.Int64())
		if pin != previous {
			break
		}
	}
	f, err := os.CreateTemp(configDir, ".pairing-pin-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if _, err = f.WriteString(pin); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(name, filepath.Join(configDir, "pairing-pin")); err != nil {
		return "", err
	}
	return pin, nil
}

func Load(configDir, requestedPIN string) (Identity, error) {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return Identity{}, err
	}
	idPath, pinPath, keyPath := filepath.Join(configDir, "device-id"), filepath.Join(configDir, "pairing-pin"), filepath.Join(configDir, "device-key")
	privateKey, err := loadOrCreateKey(keyPath)
	if err != nil {
		return Identity{}, err
	}
	id := secureconn.DeviceID(privateKey.Public().(ed25519.PublicKey))
	if err := os.WriteFile(idPath, []byte(id), 0600); err != nil {
		return Identity{}, err
	}
	pin := strings.TrimSpace(requestedPIN)
	if pin == "" {
		b, readErr := os.ReadFile(pinPath)
		if readErr == nil {
			pin = strings.TrimSpace(string(b))
		}
		// Upgrade old generated 8-digit PINs without changing the key or license.
		if len(pin) != 6 || strings.IndexFunc(pin, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			value, err := rand.Int(rand.Reader, big.NewInt(1000000))
			if err != nil {
				return Identity{}, err
			}
			pin = fmt.Sprintf("%06d", value.Int64())
			if err := os.WriteFile(pinPath, []byte(pin), 0600); err != nil {
				return Identity{}, err
			}
		}
	}
	if len(pin) < 4 || len(pin) > 32 {
		return Identity{}, fmt.Errorf("PIN must be 4-32 characters")
	}
	return Identity{ID: id, PIN: pin, PrivateKey: privateKey}, nil
}

func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	encoded, err := os.ReadFile(path)
	if err == nil {
		raw, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
		if decodeErr != nil || len(raw) != ed25519.PrivateKeySize {
			return nil, errors.New("invalid persisted device identity key")
		}
		return ed25519.PrivateKey(raw), nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	encoded = []byte(base64.StdEncoding.EncodeToString(privateKey))
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		return nil, err
	}
	return privateKey, nil
}
