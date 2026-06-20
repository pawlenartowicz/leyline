package daemon

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// PidLock holds an exclusive flock on a PID file. Release on shutdown.
type PidLock struct {
	f *os.File
}

// AcquirePidLock tries to create+lock path. Returns error if another process
// already holds the lock (EWOULDBLOCK).
func AcquirePidLock(path string) (*PidLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("daemon already running (lock held on %s)", path)
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	if err := f.Truncate(0); err != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	if _, err := f.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	return &PidLock{f: f}, nil
}

// Release unlocks and removes the PID file.
func (l *PidLock) Release() error {
	if l.f == nil {
		return nil
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	name := l.f.Name()
	_ = l.f.Close()
	l.f = nil
	return os.Remove(name)
}
