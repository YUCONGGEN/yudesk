package viewerapp

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yudesk/yudesk/internal/relay"
)

// Deliberately excludes PINs, tokens and private keys.
type connectionRecord struct {
	DeviceID      string    `json:"deviceID"`
	Name          string    `json:"name"`
	LastConnected time.Time `json:"lastConnected"`
}

var historyMu sync.Mutex

func loadHistory(directory string) ([]connectionRecord, error) {
	historyMu.Lock()
	defer historyMu.Unlock()
	return readHistory(directory)
}

func readHistory(directory string) ([]connectionRecord, error) {
	data, err := os.ReadFile(filepath.Join(directory, "connection-history.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []connectionRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if len(records) > 30 {
		records = records[:30]
	}
	return records, nil
}

func updateHistory(directory string, record connectionRecord, remove bool) error {
	historyMu.Lock()
	defer historyMu.Unlock()
	record.DeviceID = strings.ToUpper(strings.TrimSpace(record.DeviceID))
	if raw, err := hex.DecodeString(record.DeviceID); !relay.IsDeviceCode(record.DeviceID) && (err != nil || len(raw) != 12) {
		return os.ErrInvalid
	}
	records, err := readHistory(directory)
	if err != nil {
		return err
	}
	kept := make([]connectionRecord, 0, 30)
	if !remove {
		record.LastConnected = time.Now()
		kept = append(kept, record)
	}
	for _, old := range records {
		if old.DeviceID != record.DeviceID && len(kept) < 30 {
			kept = append(kept, old)
		}
	}
	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, "connection-history.json"))
}
