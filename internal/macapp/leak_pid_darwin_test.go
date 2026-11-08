//go:build darwin && leakcheck

package macapp

import "os"

func pidSelf() int { return os.Getpid() }
