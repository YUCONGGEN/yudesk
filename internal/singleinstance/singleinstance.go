package singleinstance

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Lock is an operating-system-backed exclusive process lock. The lock file can
// remain on disk after a crash; the operating system releases the actual lock
// when the owning process exits.
type Lock struct {
	file *os.File
	once sync.Once
	err  error
}

// Acquire tries to lock path without waiting. acquired is false when another
// process already holds the lock.
func Acquire(path string) (lock *Lock, acquired bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, false, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	acquired, err = tryLockFile(file)
	if err != nil || !acquired {
		_ = file.Close()
		return nil, acquired, err
	}
	if err := file.Truncate(0); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, false, err
	}
	if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, false, err
	}
	return &Lock{file: file}, true, nil
}

// Close releases the process lock.
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.once.Do(func() {
		if err := unlockFile(l.file); err != nil {
			l.err = err
		}
		if err := l.file.Close(); l.err == nil {
			l.err = err
		}
	})
	return l.err
}
