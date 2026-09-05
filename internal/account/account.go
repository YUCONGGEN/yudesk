package account

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yudesk/yudesk/internal/secureconn"
	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

const sessionLifetime = 24 * time.Hour
const permanentDeviceLicenseUnix int64 = 253402300799 // 9999-12-31 23:59:59 UTC

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,48}$`)

type Store struct{ db *sql.DB }

type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	LastSeen time.Time `json:"lastSeen"`
	Online   bool      `json:"online,omitempty"`
}

type LicensedDevice struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PairingPIN  string    `json:"-"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	ActiveUntil time.Time `json:"activeUntil,omitempty"`
	RevokedAt   time.Time `json:"revokedAt,omitempty"`
	Online      bool      `json:"online,omitempty"`
}

type AuditEntry struct {
	At         time.Time `json:"at"`
	Username   string    `json:"username,omitempty"`
	Action     string    `json:"action"`
	DeviceID   string    `json:"deviceId,omitempty"`
	RemoteAddr string    `json:"remoteAddr,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

type ActivationKeyInfo struct {
	Fingerprint string        `json:"fingerprint"`
	Duration    time.Duration `json:"-"`
	DurationSec int64         `json:"durationSeconds"`
	CreatedAt   time.Time     `json:"createdAt"`
	RedeemBy    time.Time     `json:"redeemBy"`
	RedeemedBy  string        `json:"redeemedBy,omitempty"`
	RedeemedAt  time.Time     `json:"redeemedAt,omitempty"`
	RevokedAt   time.Time     `json:"revokedAt,omitempty"`
}

func Open(path string) (*Store, error) {
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS accounts (username TEXT PRIMARY KEY,password_hash BLOB NOT NULL,salt BLOB NOT NULL,created_at INTEGER NOT NULL,active_until INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY,username TEXT NOT NULL REFERENCES accounts(username) ON DELETE CASCADE,expires_at INTEGER NOT NULL,revoked_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions(username);
CREATE TABLE IF NOT EXISTS devices (device_id TEXT PRIMARY KEY,owner TEXT NOT NULL REFERENCES accounts(username) ON DELETE CASCADE,public_key BLOB NOT NULL,name TEXT NOT NULL DEFAULT '',approved_at INTEGER NOT NULL,last_seen INTEGER NOT NULL,revoked_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS devices_owner_idx ON devices(owner);
CREATE TABLE IF NOT EXISTS activation_keys (key_hash TEXT PRIMARY KEY,duration_seconds INTEGER NOT NULL,created_at INTEGER NOT NULL,redeem_before INTEGER NOT NULL,redeemed_by TEXT NOT NULL DEFAULT '',redeemed_at INTEGER NOT NULL DEFAULT 0,revoked_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS licensed_devices (device_id TEXT PRIMARY KEY,public_key BLOB NOT NULL,name TEXT NOT NULL DEFAULT '',pairing_pin TEXT NOT NULL DEFAULT '',first_seen INTEGER NOT NULL,last_seen INTEGER NOT NULL,active_until INTEGER NOT NULL DEFAULT 0,revoked_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS licensed_devices_last_seen_idx ON licensed_devices(last_seen DESC);
CREATE TABLE IF NOT EXISTS server_settings (setting_key TEXT PRIMARY KEY,setting_value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT,at INTEGER NOT NULL,username TEXT NOT NULL DEFAULT '',action TEXT NOT NULL,device_id TEXT NOT NULL DEFAULT '',remote_addr TEXT NOT NULL DEFAULT '',detail TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS audit_at_idx ON audit_log(at DESC);`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Early development builds did not have a license expiry column. Keep an
	// existing server database usable when it is upgraded in place.
	if err := s.ensureColumn("accounts", "active_until", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("licensed_devices", "name", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return s.ensureColumn("licensed_devices", "pairing_pin", "TEXT NOT NULL DEFAULT ''")
}

func (s *Store) ensureColumn(table, column, definition string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}

func passwordHash(salt []byte, password string) []byte {
	return argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
}

func (s *Store) Register(username, password string) error {
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return errors.New("username must be 3-48 letters, digits, dot, dash or underscore")
	}
	if len(password) < 10 || len(password) > 256 {
		return errors.New("password must be 10-256 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO accounts(username,password_hash,salt,created_at,active_until) VALUES(?,?,?,?,0)`, username, passwordHash(salt, password), salt, time.Now().Unix())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return errors.New("username already exists")
		}
		return err
	}
	return nil
}

func (s *Store) Login(username, password string) (string, error) {
	username = strings.TrimSpace(username)
	var expected, salt []byte
	err := s.db.QueryRow(`SELECT password_hash,salt FROM accounts WHERE username=?`, username).Scan(&expected, &salt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if errors.Is(err, sql.ErrNoRows) {
		salt = make([]byte, 16)
		expected = make([]byte, 32)
	}
	candidate := passwordHash(salt, password)
	if err != nil || subtle.ConstantTimeCompare(candidate, expected) != 1 {
		return "", errors.New("invalid username or password")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	_, err = s.db.Exec(`INSERT INTO sessions(token_hash,username,expires_at,revoked_at) VALUES(?,?,?,0)`, hashToken(token), username, time.Now().Add(sessionLifetime).Unix())
	if err != nil {
		return "", err
	}
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE expires_at<? OR revoked_at>0`, time.Now().Unix())
	return token, nil
}

func (s *Store) Authenticate(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	var username string
	err := s.db.QueryRow(`SELECT username FROM sessions WHERE token_hash=? AND revoked_at=0 AND expires_at>?`, hashToken(token), time.Now().Unix()).Scan(&username)
	return username, err == nil
}

func (s *Store) Valid(token string) bool { _, ok := s.Authenticate(token); return ok }

func (s *Store) Logout(token string) error {
	_, err := s.db.Exec(`UPDATE sessions SET revoked_at=? WHERE token_hash=?`, time.Now().Unix(), hashToken(token))
	return err
}

func (s *Store) HasActiveLicense(username string) bool {
	var activeUntil int64
	err := s.db.QueryRow(`SELECT active_until FROM accounts WHERE username=?`, username).Scan(&activeUntil)
	return err == nil && activeUntil > time.Now().Unix()
}

func (s *Store) LicenseExpiry(username string) (time.Time, error) {
	var activeUntil int64
	if err := s.db.QueryRow(`SELECT active_until FROM accounts WHERE username=?`, username).Scan(&activeUntil); err != nil {
		return time.Time{}, err
	}
	if activeUntil == 0 {
		return time.Time{}, nil
	}
	return time.Unix(activeUntil, 0), nil
}

func (s *Store) GenerateActivationKey(duration, redeemWindow time.Duration) (string, error) {
	if duration < time.Hour || duration > 10*365*24*time.Hour {
		return "", errors.New("activation duration must be between 1 hour and 10 years")
	}
	if redeemWindow <= 0 {
		redeemWindow = 90 * 24 * time.Hour
	}
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	parts := []string{"YU"}
	for len(encoded) > 0 {
		size := 4
		if len(encoded) < size {
			size = len(encoded)
		}
		parts = append(parts, encoded[:size])
		encoded = encoded[size:]
	}
	key := strings.Join(parts, "-")
	now := time.Now()
	_, err := s.db.Exec(`INSERT INTO activation_keys(key_hash,duration_seconds,created_at,redeem_before) VALUES(?,?,?,?)`, hashToken(key), int64(duration/time.Second), now.Unix(), now.Add(redeemWindow).Unix())
	return key, err
}

func (s *Store) ListActivationKeys(limit int) ([]ActivationKeyInfo, error) {
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT key_hash,duration_seconds,created_at,redeem_before,redeemed_by,redeemed_at,revoked_at FROM activation_keys ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ActivationKeyInfo
	for rows.Next() {
		var hash, redeemedBy string
		var duration, createdAt, redeemBefore, redeemedAt, revokedAt int64
		if err := rows.Scan(&hash, &duration, &createdAt, &redeemBefore, &redeemedBy, &redeemedAt, &revokedAt); err != nil {
			return nil, err
		}
		entry := ActivationKeyInfo{
			Fingerprint: strings.ToUpper(hash[:min(12, len(hash))]),
			Duration:    time.Duration(duration) * time.Second,
			DurationSec: duration,
			CreatedAt:   time.Unix(createdAt, 0),
			RedeemBy:    time.Unix(redeemBefore, 0),
			RedeemedBy:  redeemedBy,
		}
		if redeemedAt != 0 {
			entry.RedeemedAt = time.Unix(redeemedAt, 0)
		}
		if revokedAt != 0 {
			entry.RevokedAt = time.Unix(revokedAt, 0)
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (s *Store) RevokeActivationKey(key string) error {
	normalized := strings.ToUpper(strings.TrimSpace(key))
	result, err := s.db.Exec(`UPDATE activation_keys SET revoked_at=? WHERE key_hash=? AND redeemed_at=0 AND revoked_at=0`, time.Now().Unix(), hashToken(normalized))
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return errors.New("activation key not found, already used, or already revoked")
	}
	return nil
}

func (s *Store) RevokeActivationKeyByFingerprint(fingerprint string) error {
	fingerprint = strings.ToUpper(strings.TrimSpace(fingerprint))
	if len(fingerprint) != 12 {
		return errors.New("activation key fingerprint must contain 12 characters")
	}
	result, err := s.db.Exec(`UPDATE activation_keys SET revoked_at=? WHERE UPPER(SUBSTR(key_hash,1,12))=? AND redeemed_at=0 AND revoked_at=0`, time.Now().Unix(), fingerprint)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("unused activation key not found")
	}
	return nil
}

func (s *Store) RedeemActivationKey(username, key string) (time.Time, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	var duration, redeemBefore, redeemedAt, revokedAt int64
	var redeemedBy string
	err = tx.QueryRow(`SELECT duration_seconds,redeem_before,redeemed_by,redeemed_at,revoked_at FROM activation_keys WHERE key_hash=?`, hashToken(strings.ToUpper(strings.TrimSpace(key)))).Scan(&duration, &redeemBefore, &redeemedBy, &redeemedAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, errors.New("invalid activation key")
	}
	if err != nil {
		return time.Time{}, err
	}
	now := time.Now().Unix()
	if revokedAt != 0 {
		return time.Time{}, errors.New("activation key was revoked")
	}
	if redeemedBy != "" || redeemedAt != 0 {
		return time.Time{}, errors.New("activation key was already used")
	}
	if redeemBefore < now {
		return time.Time{}, errors.New("activation key expired before redemption")
	}
	var current int64
	if err := tx.QueryRow(`SELECT active_until FROM accounts WHERE username=?`, username).Scan(&current); err != nil {
		return time.Time{}, err
	}
	base := now
	if current > base {
		base = current
	}
	newExpiry := base + duration
	result, err := tx.Exec(`UPDATE activation_keys SET redeemed_by=?,redeemed_at=? WHERE key_hash=? AND redeemed_by='' AND revoked_at=0`, username, now, hashToken(strings.ToUpper(strings.TrimSpace(key))))
	if err != nil {
		return time.Time{}, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return time.Time{}, errors.New("activation key is no longer available")
	}
	if _, err = tx.Exec(`UPDATE accounts SET active_until=? WHERE username=?`, newExpiry, username); err != nil {
		return time.Time{}, err
	}
	if err = tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return time.Unix(newExpiry, 0), nil
}

func (s *Store) RegisterLicensedDevice(deviceID string, publicKey []byte) error {
	return s.RegisterLicensedDeviceNamed(deviceID, publicKey, "")
}

func (s *Store) RegisterLicensedDeviceNamed(deviceID string, publicKey []byte, name string) error {
	deviceID = strings.ToUpper(strings.TrimSpace(deviceID))
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 80 {
		return errors.New("device name is too long")
	}
	if len(publicKey) != ed25519.PublicKeySize || !strings.EqualFold(secureconn.DeviceID(ed25519.PublicKey(publicKey)), deviceID) {
		return errors.New("device ID does not match public key")
	}
	now := time.Now().Unix()
	var existingKey []byte
	var revokedAt int64
	err := s.db.QueryRow(`SELECT public_key,revoked_at FROM licensed_devices WHERE device_id=?`, deviceID).Scan(&existingKey, &revokedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = s.db.Exec(`INSERT INTO licensed_devices(device_id,public_key,name,first_seen,last_seen,active_until,revoked_at) VALUES(?,?,?,?,?,0,0)`, deviceID, publicKey, name, now, now)
		return err
	case err != nil:
		return err
	case subtle.ConstantTimeCompare(existingKey, publicKey) != 1:
		return errors.New("device identity key changed")
	case revokedAt != 0:
		return errors.New("device has been revoked")
	default:
		_, err = s.db.Exec(`UPDATE licensed_devices SET last_seen=?,name=CASE WHEN ?='' THEN name ELSE ? END WHERE device_id=?`, now, name, name, deviceID)
		return err
	}
}

func (s *Store) RenameLicensedDevice(deviceID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return errors.New("device name must contain 1-80 characters")
	}
	result, err := s.db.Exec(`UPDATE licensed_devices SET name=? WHERE device_id=?`, name, strings.ToUpper(strings.TrimSpace(deviceID)))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("device not found")
	}
	return nil
}

func (s *Store) UpdateLicensedDevicePIN(deviceID, pin string) error {
	pin = strings.TrimSpace(pin)
	if len(pin) < 6 || len(pin) > 8 {
		return errors.New("pairing PIN must contain 6-8 digits")
	}
	for _, character := range pin {
		if character < '0' || character > '9' {
			return errors.New("pairing PIN must contain 6-8 digits")
		}
	}
	result, err := s.db.Exec(`UPDATE licensed_devices SET pairing_pin=? WHERE device_id=?`, pin, strings.ToUpper(strings.TrimSpace(deviceID)))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("device not found")
	}
	return nil
}

func (s *Store) DeviceLicenseExpiry(deviceID string) (time.Time, error) {
	var activeUntil, revokedAt int64
	err := s.db.QueryRow(`SELECT active_until,revoked_at FROM licensed_devices WHERE device_id=?`, strings.ToUpper(strings.TrimSpace(deviceID))).Scan(&activeUntil, &revokedAt)
	if err != nil {
		return time.Time{}, err
	}
	if revokedAt != 0 {
		return time.Time{}, errors.New("device has been revoked")
	}
	if activeUntil == 0 {
		return time.Time{}, nil
	}
	return time.Unix(activeUntil, 0), nil
}

func (s *Store) HasActiveDeviceLicense(deviceID string) bool {
	expires, err := s.DeviceLicenseExpiry(deviceID)
	return err == nil && expires.After(time.Now())
}

func IsPermanentDeviceLicense(expiry time.Time) bool {
	return expiry.Unix() >= permanentDeviceLicenseUnix
}

func (s *Store) EnsurePermanentDeviceLicense(deviceID string) (bool, error) {
	result, err := s.db.Exec(`UPDATE licensed_devices SET active_until=? WHERE device_id=? AND revoked_at=0 AND active_until<=?`, permanentDeviceLicenseUnix, strings.ToUpper(strings.TrimSpace(deviceID)), time.Now().Unix())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) DeviceAutoActivation() (bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT setting_value FROM server_settings WHERE setting_key='device_auto_activation'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return value == "1", err
}

func (s *Store) SetDeviceAutoActivation(enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}
	_, err := s.db.Exec(`INSERT INTO server_settings(setting_key,setting_value) VALUES('device_auto_activation',?) ON CONFLICT(setting_key) DO UPDATE SET setting_value=excluded.setting_value`, value)
	return err
}

func (s *Store) RedeemDeviceActivationKey(deviceID, key string) (time.Time, error) {
	deviceID = strings.ToUpper(strings.TrimSpace(deviceID))
	tx, err := s.db.Begin()
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	var current, deviceRevoked int64
	if err := tx.QueryRow(`SELECT active_until,revoked_at FROM licensed_devices WHERE device_id=?`, deviceID).Scan(&current, &deviceRevoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, errors.New("device has not connected to the server")
		}
		return time.Time{}, err
	}
	if deviceRevoked != 0 {
		return time.Time{}, errors.New("device has been revoked")
	}
	if current >= permanentDeviceLicenseUnix {
		return time.Time{}, errors.New("device already has a permanent license")
	}
	var duration, redeemBefore, redeemedAt, revokedAt int64
	var redeemedBy string
	normalizedKey := strings.ToUpper(strings.TrimSpace(key))
	err = tx.QueryRow(`SELECT duration_seconds,redeem_before,redeemed_by,redeemed_at,revoked_at FROM activation_keys WHERE key_hash=?`, hashToken(normalizedKey)).Scan(&duration, &redeemBefore, &redeemedBy, &redeemedAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, errors.New("invalid activation key")
	}
	if err != nil {
		return time.Time{}, err
	}
	now := time.Now().Unix()
	if revokedAt != 0 {
		return time.Time{}, errors.New("activation key was revoked")
	}
	if redeemedBy != "" || redeemedAt != 0 {
		return time.Time{}, errors.New("activation key was already used")
	}
	if redeemBefore < now {
		return time.Time{}, errors.New("activation key expired before redemption")
	}
	base := now
	if current > base {
		base = current
	}
	newExpiry := base + duration
	result, err := tx.Exec(`UPDATE activation_keys SET redeemed_by=?,redeemed_at=? WHERE key_hash=? AND redeemed_by='' AND revoked_at=0`, "device:"+deviceID, now, hashToken(normalizedKey))
	if err != nil {
		return time.Time{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return time.Time{}, errors.New("activation key is no longer available")
	}
	if _, err = tx.Exec(`UPDATE licensed_devices SET active_until=?,last_seen=? WHERE device_id=?`, newExpiry, now, deviceID); err != nil {
		return time.Time{}, err
	}
	if err = tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return time.Unix(newExpiry, 0), nil
}

func (s *Store) GrantDeviceLicense(deviceID string, duration time.Duration) (time.Time, error) {
	if duration < time.Hour || duration > 10*365*24*time.Hour {
		return time.Time{}, errors.New("license duration must be between 1 hour and 10 years")
	}
	deviceID = strings.ToUpper(strings.TrimSpace(deviceID))
	now := time.Now().Unix()
	seconds := int64(duration / time.Second)
	result, err := s.db.Exec(`UPDATE licensed_devices SET active_until=CASE WHEN active_until>=? THEN active_until WHEN active_until>? THEN active_until+? ELSE ? END,revoked_at=0 WHERE device_id=?`, permanentDeviceLicenseUnix, now, seconds, now+seconds, deviceID)
	if err != nil {
		return time.Time{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return time.Time{}, errors.New("device not found; start the controlled client first")
	}
	return s.DeviceLicenseExpiry(deviceID)
}

func (s *Store) RevokeLicensedDevice(deviceID string) error {
	result, err := s.db.Exec(`UPDATE licensed_devices SET revoked_at=? WHERE device_id=? AND revoked_at=0`, time.Now().Unix(), strings.ToUpper(strings.TrimSpace(deviceID)))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("device not found or already revoked")
	}
	return nil
}

func (s *Store) UnrevokeLicensedDevice(deviceID string) error {
	result, err := s.db.Exec(`UPDATE licensed_devices SET revoked_at=0 WHERE device_id=? AND revoked_at>0`, strings.ToUpper(strings.TrimSpace(deviceID)))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("device not found or is not disabled")
	}
	return nil
}

func (s *Store) DeleteLicensedDevice(deviceID string) error {
	result, err := s.db.Exec(`DELETE FROM licensed_devices WHERE device_id=?`, strings.ToUpper(strings.TrimSpace(deviceID)))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("device not found")
	}
	return nil
}

func (s *Store) ListLicensedDevices(limit int) ([]LicensedDevice, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT device_id,name,pairing_pin,first_seen,last_seen,active_until,revoked_at FROM licensed_devices ORDER BY last_seen DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devices []LicensedDevice
	for rows.Next() {
		var device LicensedDevice
		var firstSeen, lastSeen, activeUntil, revokedAt int64
		if err := rows.Scan(&device.ID, &device.Name, &device.PairingPIN, &firstSeen, &lastSeen, &activeUntil, &revokedAt); err != nil {
			return nil, err
		}
		device.FirstSeen = time.Unix(firstSeen, 0)
		device.LastSeen = time.Unix(lastSeen, 0)
		if activeUntil != 0 {
			device.ActiveUntil = time.Unix(activeUntil, 0)
		}
		if revokedAt != 0 {
			device.RevokedAt = time.Unix(revokedAt, 0)
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (s *Store) BindDevice(username, deviceID string, publicKey []byte, name string) error {
	if len(publicKey) != ed25519.PublicKeySize || !strings.EqualFold(secureconn.DeviceID(ed25519.PublicKey(publicKey)), deviceID) {
		return errors.New("device ID does not match public key")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	var existingKey []byte
	var revokedAt int64
	err = tx.QueryRow(`SELECT owner,public_key,revoked_at FROM devices WHERE device_id=?`, strings.ToUpper(deviceID)).Scan(&owner, &existingKey, &revokedAt)
	now := time.Now().Unix()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.Exec(`INSERT INTO devices(device_id,owner,public_key,name,approved_at,last_seen,revoked_at) VALUES(?,?,?,?,?,?,0)`, strings.ToUpper(deviceID), username, publicKey, name, now, now)
	case err != nil:
		return err
	case owner != username:
		return errors.New("device belongs to another account")
	case subtle.ConstantTimeCompare(existingKey, publicKey) != 1:
		return errors.New("device identity key changed")
	case revokedAt != 0:
		return errors.New("device has been revoked")
	default:
		_, err = tx.Exec(`UPDATE devices SET last_seen=?,name=CASE WHEN ?='' THEN name ELSE ? END WHERE device_id=?`, now, name, name, strings.ToUpper(deviceID))
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) OwnsDevice(username, deviceID string) bool {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM devices WHERE owner=? AND device_id=? AND revoked_at=0`, username, strings.ToUpper(deviceID)).Scan(&exists)
	return err == nil
}

func (s *Store) TouchDevice(deviceID string) {
	_, _ = s.db.Exec(`UPDATE devices SET last_seen=? WHERE device_id=?`, time.Now().Unix(), strings.ToUpper(deviceID))
}

func (s *Store) ListDevices(username string) ([]Device, error) {
	rows, err := s.db.Query(`SELECT device_id,name,last_seen FROM devices WHERE owner=? AND revoked_at=0 ORDER BY last_seen DESC`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devices []Device
	for rows.Next() {
		var device Device
		var lastSeen int64
		if err := rows.Scan(&device.ID, &device.Name, &lastSeen); err != nil {
			return nil, err
		}
		device.LastSeen = time.Unix(lastSeen, 0)
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (s *Store) RevokeDevice(username, deviceID string) error {
	result, err := s.db.Exec(`UPDATE devices SET revoked_at=? WHERE owner=? AND device_id=? AND revoked_at=0`, time.Now().Unix(), username, strings.ToUpper(deviceID))
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return errors.New("device not found")
	}
	return nil
}

func (s *Store) Audit(entry AuditEntry) {
	if entry.At.IsZero() {
		entry.At = time.Now()
	}
	_, _ = s.db.Exec(`INSERT INTO audit_log(at,username,action,device_id,remote_addr,detail) VALUES(?,?,?,?,?,?)`, entry.At.Unix(), entry.Username, entry.Action, entry.DeviceID, entry.RemoteAddr, entry.Detail)
}

func (s *Store) RecentAudit(username string, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT at,username,action,device_id,remote_addr,detail FROM audit_log WHERE username=? ORDER BY at DESC LIMIT ?`, username, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var entry AuditEntry
		var at int64
		if err := rows.Scan(&at, &entry.Username, &entry.Action, &entry.DeviceID, &entry.RemoteAddr, &entry.Detail); err != nil {
			return nil, err
		}
		entry.At = time.Unix(at, 0)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) RecentAuditAll(limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT at,username,action,device_id,remote_addr,detail FROM audit_log ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var entry AuditEntry
		var at int64
		if err := rows.Scan(&at, &entry.Username, &entry.Action, &entry.DeviceID, &entry.RemoteAddr, &entry.Detail); err != nil {
			return nil, err
		}
		entry.At = time.Unix(at, 0)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func PublicError(err error) map[string]any {
	return map[string]any{"ok": false, "error": fmt.Sprint(err)}
}
