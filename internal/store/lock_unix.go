//go:build unix

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// dirLock is an exclusive advisory lock on <data dir>/.lock. Two processes must never write the
// same sealed database, and the second one would also delete the first one's live database from
// tmpfs at start, so the lock is taken before anything else is touched. The kernel drops it when
// the process dies, so a crash never leaves a stale lock.
type dirLock struct{ f *os.File }

func lockDir(dir string) (*dirLock, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("store: lock data directory: %w", err)
	}
	return &dirLock{f: f}, nil
}

func (l *dirLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
