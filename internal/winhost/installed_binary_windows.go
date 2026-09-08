//go:build windows

package winhost

import (
	"crypto/sha256"
	"io"
	"os"
	"sync"
)

var binaryHashes struct {
	sync.Mutex
	values map[string]binaryDigest
}

type binaryDigest struct {
	info os.FileInfo
	sum  [32]byte
}

// Hash only when file identity/size/time changes; polling settings must not
// repeatedly read two large executables during an active remote session.
func installedBinaryMatches(source, target string) bool {
	binaryHashes.Lock()
	defer binaryHashes.Unlock()
	digest := func(path string) ([32]byte, bool) {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return [32]byte{}, false
		}
		if old, ok := binaryHashes.values[path]; ok && os.SameFile(old.info, info) && old.info.Size() == info.Size() && old.info.ModTime() == info.ModTime() {
			return old.sum, true
		}
		f, err := os.Open(path)
		if err != nil {
			return [32]byte{}, false
		}
		defer f.Close()
		hash := sha256.New()
		if _, err = io.Copy(hash, f); err != nil {
			return [32]byte{}, false
		}
		after, err := f.Stat()
		if err != nil || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
			return [32]byte{}, false
		}
		var sum [32]byte
		copy(sum[:], hash.Sum(nil))
		if len(binaryHashes.values) > 8 {
			binaryHashes.values = nil
		}
		if binaryHashes.values == nil {
			binaryHashes.values = make(map[string]binaryDigest)
		}
		binaryHashes.values[path] = binaryDigest{info, sum}
		return sum, true
	}
	a, ok := digest(source)
	if !ok {
		return false
	}
	b, ok := digest(target)
	return ok && a == b
}
