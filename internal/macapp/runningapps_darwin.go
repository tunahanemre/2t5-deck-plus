package macapp

/*
#cgo LDFLAGS: -framework AppKit
#include <stdlib.h>

extern char* runningApps(void);
extern char* appNameForBundleID(const char* bundleID);
extern char* selfBundleID(void);
*/
import "C"

import (
	"strings"
	"unsafe"
)

// App is a running GUI application.
type App struct {
	ID   string
	Name string
}

// RunningApps lists running applications with a regular activation policy
// (the ones in the Dock), via NSWorkspace. Needs no Screen Recording
// permission. Order is the system's.
func RunningApps() []App {
	cs := C.runningApps()
	if cs == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(cs))
	return parseApps(C.GoString(cs))
}

// AppName is the localized name of a running app, or "" when it is not running.
func AppName(bundleID string) string {
	if bundleID == "" {
		return ""
	}
	cb := C.CString(bundleID)
	defer C.free(unsafe.Pointer(cb))
	cs := C.appNameForBundleID(cb)
	if cs == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cs))
	return C.GoString(cs)
}

// SelfBundleID is this process's own bundle id ("" when not run from a .app).
func SelfBundleID() string {
	cs := C.selfBundleID()
	if cs == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cs))
	return C.GoString(cs)
}

func parseApps(s string) []App {
	var out []App
	for _, line := range strings.Split(s, "\n") {
		id, name, ok := strings.Cut(line, "\t")
		if !ok || id == "" {
			continue
		}
		out = append(out, App{ID: id, Name: name})
	}
	return out
}
