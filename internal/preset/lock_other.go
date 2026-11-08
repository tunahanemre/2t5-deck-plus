//go:build !unix

package preset

import "os"

// tryLock is a no-op where flock is unavailable: the lock is always granted.
func tryLock(path string) (*os.File, bool, int, error) { return nil, true, os.Getpid(), nil }
