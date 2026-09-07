package account

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

func ValidDeviceCode(code string) bool {
	if len(code) != 9 || code[0] == '0' {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Short codes are unique, persistent aliases. They are never recycled, even
// when an administrator deletes a device. The cryptographic identity is intact.
func (s *Store) EnsureDeviceCode(id string) (string, error) {
	id = strings.ToUpper(strings.TrimSpace(id))
	var existing string
	err := s.db.QueryRow(`SELECT code FROM device_codes WHERE device_id=?`, id).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var exists int
	if err := s.db.QueryRow(`SELECT 1 FROM licensed_devices WHERE device_id=?`, id).Scan(&exists); err != nil {
		return "", err
	}
	for i := 0; i < 100; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(900000000))
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%09d", 100000000+n.Int64())
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO device_codes(device_id,code) VALUES(?,?)`, id, code); err != nil {
			return "", err
		}
		if err := s.db.QueryRow(`SELECT code FROM device_codes WHERE device_id=?`, id).Scan(&existing); err == nil {
			return existing, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return "", errors.New("device code allocation failed")
}

func (s *Store) ResolveDeviceCode(code string) (string, error) {
	if !ValidDeviceCode(code) {
		return "", errors.New("invalid device code")
	}
	var id string
	err := s.db.QueryRow(`SELECT c.device_id FROM device_codes c JOIN licensed_devices d ON d.device_id=c.device_id WHERE c.code=?`, code).Scan(&id)
	return id, err
}

func (s *Store) DevicePublicLabel(id string) (name, code string) {
	_ = s.db.QueryRow(`SELECT d.name,COALESCE(c.code,'') FROM licensed_devices d LEFT JOIN device_codes c ON c.device_id=d.device_id WHERE d.device_id=?`, id).Scan(&name, &code)
	return
}

func (s *Store) BackfillDeviceCodes() error {
	rows, err := s.db.Query(`SELECT device_id FROM licensed_devices WHERE device_id NOT IN (SELECT device_id FROM device_codes)`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.EnsureDeviceCode(id); err != nil {
			return err
		}
	}
	return nil
}
