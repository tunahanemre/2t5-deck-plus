package macapp

/*
#cgo LDFLAGS: -framework CoreGraphics

#include <CoreGraphics/CoreGraphics.h>

// preflightScreenCapture wraps CGPreflightScreenCaptureAccess.
static int preflightScreenCapture(void) {
    return CGPreflightScreenCaptureAccess() ? 1 : 0;
}
*/
import "C"

// ScreenRecordingTrusted returns true when the running process has already
// been granted Screen Recording access in macOS System Settings →
// Privacy & Security → Screen Recording.
//
// CGPreflightScreenCaptureAccess is a read-only query — unlike
// CGRequestScreenCaptureAccess, it never triggers the system permission
// prompt — so it is safe to call from polling paths (Engine.Status(),
// App.RecheckPermissions()) and from tests without side effects.
//
// The window-title safety guard (kCGWindowName, read via
// ResolveWindowTitle and used for the project-name guard)
// requires this permission on macOS 10.15+; without it, window titles read
// back as empty strings rather than erroring.
func ScreenRecordingTrusted() bool {
	return C.preflightScreenCapture() != 0
}
