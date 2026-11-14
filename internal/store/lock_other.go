//go:build !unix

package store

// dirLock is a no-op where advisory file locks are not available (a Windows dev box). Production
// runs on Linux, where the lock is real and tested in CI.
type dirLock struct{}

func lockDir(string) (*dirLock, error) { return &dirLock{}, nil }

func (l *dirLock) release() {}
