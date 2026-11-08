package macapp

/*
#cgo LDFLAGS: -framework AppKit -framework CoreGraphics
#include <stdlib.h>

extern char* resolveWindowTitleC(void);
*/
import "C"
import (
	"sync"
	"time"
	"unsafe"
)

// windowTitleCacheTTL is how long the resolved window title is cached.
const windowTitleCacheTTL = 1 * time.Second

var (
	wintitleMu       sync.Mutex
	wintitleCached   string
	wintitleCachedAt time.Time
)

// ResolveWindowTitle returns the window title of the frontmost DaVinci Resolve
// window. It uses CGWindowListCopyWindowInfo + kCGWindowName and caches the
// result for ~1 second to avoid per-tick CGWindowList round-trips.
//
// Returns an empty string when Resolve has no visible window or the title is
// unavailable.
func ResolveWindowTitle() string {
	wintitleMu.Lock()
	defer wintitleMu.Unlock()
	if time.Since(wintitleCachedAt) < windowTitleCacheTTL {
		return wintitleCached
	}
	cs := C.resolveWindowTitleC()
	var title string
	if cs != nil {
		title = C.GoString(cs)
		C.free(unsafe.Pointer(cs))
	}
	wintitleCached = title
	wintitleCachedAt = time.Now()
	return title
}
