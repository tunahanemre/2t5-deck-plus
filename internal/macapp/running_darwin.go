package macapp

/*
#include <stdlib.h>

// isAppRunning is implemented in running_darwin.m (Objective-C).
extern int isAppRunning(const char* bundleID);
*/
import "C"
import "unsafe"

// IsAppRunning reports whether any process with the given bundle identifier is
// currently running, regardless of whether it is frontmost or has a visible
// window.
//
// This is a Launch Services lookup, NOT the CGWindowList walk FrontmostBundleID
// uses: it needs no Screen Recording permission and no on-screen window, so an
// app sitting behind others or minimised still reads as running.
func IsAppRunning(bundleID string) bool {
	if bundleID == "" {
		return false
	}
	cs := C.CString(bundleID)
	defer C.free(unsafe.Pointer(cs))
	return C.isAppRunning(cs) != 0
}

// IsResolveRunning reports whether DaVinci Resolve is running at all.
//
// This is the correct gate for "should Resolve-dependent UI be on screen",
// and it is deliberately NOT the same question as "is the sidecar connected".
// The sidecar times out routinely while Resolve is merely BUSY -- this repo
// already settled that a timeout must never be read as death, only a hard
// transport error may be (commit 4a1f0ba, after the same-process GIL-probe
// dead end in 6ec9052). Gating visible UI on sidecar liveness would make it
// flap every time the user does something slow in Resolve; process presence
// changes exactly twice per Resolve session.
func IsResolveRunning() bool {
	return IsAppRunning(resolveBundleID)
}
