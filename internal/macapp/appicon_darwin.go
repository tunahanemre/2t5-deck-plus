package macapp

/*
#cgo LDFLAGS: -framework AppKit
#include <stdlib.h>

extern unsigned char* appIconPNG(const char* bundleID, int* outLen);
*/
import "C"

import "unsafe"

// AppIconPNG renders the icon of the installed app with this bundle id as a
// 256x256 PNG (NSWorkspace). nil when the app is not installed.
func AppIconPNG(bundleID string) []byte {
	if bundleID == "" {
		return nil
	}
	cb := C.CString(bundleID)
	defer C.free(unsafe.Pointer(cb))
	var n C.int
	p := C.appIconPNG(cb, &n)
	if p == nil || n <= 0 {
		return nil
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoBytes(unsafe.Pointer(p), n)
}
