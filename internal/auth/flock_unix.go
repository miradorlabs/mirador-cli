//go:build unix

package auth

import (
	"fmt"
	"os"
	"syscall"
)

// lockCredentialFile takes an exclusive advisory lock keyed to the credential file, so
// two mirador processes serialize their read-modify-write of it rather than racing.
// It blocks until the lock is available and returns a function that releases it.
//
// The lock lives on a sidecar `.lock` file rather than the credential file itself: the
// atomic write renames a fresh temp file over the credential path, which would drop a
// lock held on the original inode.
func lockCredentialFile(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open credential lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock credentials: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
