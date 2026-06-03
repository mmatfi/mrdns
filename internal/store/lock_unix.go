//go:build unix

package store

import (
	"os"
	"syscall"
)

// flockFile takes an exclusive advisory lock on a lock file, returning a
// release function. This guards against a second process mutating the same
// zone concurrently.
func flockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
