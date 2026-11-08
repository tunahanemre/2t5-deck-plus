//go:build darwin && leakcheck

package macapp

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Leak regression for F12 / F13 (wave E, commit e3c8fc8): the frontmost,
// window-title and icon queries ran on Go threads without an autorelease pool
// and the icon path leaked its bitmap rep, so the app's memory grew with every
// 250 ms frontmost tick. The queries are now wrapped in @autoreleasepool.
//
// Why this is a build-tagged test and not part of the sealed run: it needs a
// real login session (NSWorkspace, WindowServer), it takes seconds, and RSS is
// a noisy proxy (the unfixed code leaks ~0.3 KB per call, so the loop has to
// be long and the bound generous to be both reliable and meaningful). Run it by
// hand before and after a change to the .m files:
//
//	go test -tags leakcheck -count=1 -run TestLeak ./internal/macapp/
//
// Measured on the dev Mac: FrontmostBundleID grew RSS about 0.3 KB per call
// before the fix (1.9 MB per 6000 calls) and about 0.025 KB after (1.5 MB per
// 60000). ResolveWindowTitle returns early when Resolve is not running, so it
// only guards the no-Resolve path here. Each bound sits well above the fixed
// figure and below the unfixed one for its loop size.

func rssBytes(t *testing.T) int64 {
	t.Helper()
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pidSelf())).Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Skipf("unreadable ps output %q", out)
	}
	return kb * 1024
}

func growth(t *testing.T, name string, loops int, bound int64, fn func()) {
	t.Helper()
	for i := 0; i < loops/10; i++ { // warm up: caches, lazy class loading
		fn()
	}
	runtime.GC()
	before := rssBytes(t)
	for i := 0; i < loops; i++ {
		fn()
	}
	runtime.GC()
	grew := rssBytes(t) - before
	t.Logf("%s: %d calls, RSS grew %.2f MB", name, loops, float64(grew)/(1<<20))
	if grew > bound {
		t.Errorf("%s: RSS grew %.1f MB over %d calls (bound %d MB): the query is leaking again",
			name, float64(grew)/(1<<20), loops, bound>>20)
	}
}

func TestLeak_FrontmostBundleID(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	growth(t, "FrontmostBundleID", 20000, 4<<20, func() { FrontmostBundleID() }) // ~20 s; unfixed ~6 MB
}

func TestLeak_ResolveWindowTitle(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	growth(t, "ResolveWindowTitle", 10000, 4<<20, func() { ResolveWindowTitle() })
}

func TestLeak_AppIconPNG(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	growth(t, "AppIconPNG", 3000, 10<<20, func() { AppIconPNG("com.apple.finder") })
}
