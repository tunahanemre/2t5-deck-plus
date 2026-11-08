package macapp

/*
#cgo LDFLAGS: -framework AppKit -framework CoreGraphics
#include <stdlib.h>

// frontmostBundleID is implemented in frontmost_darwin.m (Objective-C).
extern char* frontmostBundleID(void);
*/
import "C"
import "unsafe"

const resolveBundleID = "com.blackmagic-design.DaVinciResolve"

// finderBundleID identifies the macOS Finder. Also the NAME of 2T5 DECK+'s
// Finder profile bank in bindings.json -- see engine.go's finderBankName,
// which is deliberately the bundle id so the config is already data the
// global spec's app rung can consume rather than a special case to migrate.
const finderBundleID = "com.apple.finder"

// FrontmostBundleID returns the bundle identifier of the currently active
// (frontmost) application.  Returns an empty string if unavailable.
func FrontmostBundleID() string {
	cs := C.frontmostBundleID()
	if cs == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cs))
	return C.GoString(cs)
}

// BundleIDIsResolve returns true when bid matches the DaVinci Resolve bundle id.
// Use this when you have already called FrontmostBundleID() to avoid a second
// CGWindowList query.
func BundleIDIsResolve(bid string) bool {
	return bid == resolveBundleID
}

// BundleIDIsFinder returns true when bid is the macOS Finder. Same
// call-with-an-already-fetched-id shape as BundleIDIsResolve above, so a
// caller that needs to test several apps pays for only one CGWindowList walk.
func BundleIDIsFinder(bid string) bool {
	return bid == finderBundleID
}

// IsResolveFrontmost returns true when DaVinci Resolve is the frontmost app.
// Prefer calling FrontmostBundleID + BundleIDIsResolve when the caller also
// needs the raw bundle id string (saves one CGWindowList call).
func IsResolveFrontmost() bool {
	return BundleIDIsResolve(FrontmostBundleID())
}
