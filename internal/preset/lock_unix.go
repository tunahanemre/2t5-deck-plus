//go:build unix

package preset

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// tryLock takes an exclusive, non-blocking flock on path (created if
// missing). On success it writes this process's pid into the file and returns
// the open file (keep it open: closing releases the lock). When another
// process holds the lock it returns ok=false and that process's pid (0 when
// unknown). The kernel drops the lock when the holder dies, so a crash never
// leaves a stale lock behind.
func tryLock(path string) (f *os.File, ok bool, holder int, err error) {
	f, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, false, 0, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, false, readPID(path), nil
		}
		return nil, false, 0, err
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return f, true, os.Getpid(), nil
}

func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
