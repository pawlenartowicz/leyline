package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// ReadAndHash reads a file from disk and returns its contents plus the hex
// SHA-256 of those contents. If the file's size at stat-time differs from the
// length of bytes read (an in-progress rename), ReadAndHash retries once after
// 10 ms — leyline-server's rename completes in microseconds, so a single
// retry suffices.
//
// On a second mismatch, ReadAndHash returns the second read anyway: the cache
// key includes the SHA-256 of what we actually saw, so any cached entry built
// from this read is correct for that exact byte sequence. The next request
// will re-read and re-hash, blocking only one stale render.
func ReadAndHash(path string) ([]byte, string, error) {
	return readAndHashCustom(path, defaultStat, defaultReadFile)
}

func defaultStat(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("not a file: %s", path)
	}
	return info.Size(), nil
}

func defaultReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func readAndHashCustom(
	path string,
	stat func(string) (int64, error),
	read func(string) ([]byte, error),
) ([]byte, string, error) {
	const retryDelay = 10 * time.Millisecond

	for attempt := 0; attempt < 2; attempt++ {
		size, err := stat(path)
		if err != nil {
			return nil, "", err
		}
		data, err := read(path)
		if err != nil {
			return nil, "", err
		}
		if int64(len(data)) == size {
			return data, hashHex(data), nil
		}
		if attempt == 0 {
			time.Sleep(retryDelay)
			continue
		}
		return data, hashHex(data), nil
	}
	return nil, "", fmt.Errorf("unreachable")
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
