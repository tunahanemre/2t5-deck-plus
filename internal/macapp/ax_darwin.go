package macapp

/*
#cgo LDFLAGS: -framework ApplicationServices

#include <ApplicationServices/ApplicationServices.h>

// isTrusted wraps AXIsProcessTrusted.
static int isTrusted(void) {
    return AXIsProcessTrusted() ? 1 : 0;
}
*/
import "C"

// AccessibilityTrusted returns true when the running process has been granted
// Accessibility (assistive device) access in macOS System Settings →
// Privacy & Security → Accessibility.
//
// Posting CGEvents to other applications requires this permission.
// Without it, Drag() calls are silently ignored by the OS.
func AccessibilityTrusted() bool {
	return C.isTrusted() != 0
}
