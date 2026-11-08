package macapp

/*
#cgo LDFLAGS: -framework AppKit -framework CoreGraphics -framework ScreenCaptureKit
#include <stdlib.h>

extern char* appWindowsC(const char *bundleID);
extern char* displaysC(void);
extern int grabRectC(double x, double y, double w, double h, unsigned char **out, int *ow, int *oh, char **err);
*/
import "C"

import (
	"errors"
	"image"
	"strconv"
	"strings"
	"unsafe"
)

// WindowRect is one on-screen window in global display points (top-left origin).
type WindowRect struct {
	X, Y, W, H float64
	Title      string
}

// DisplayInfo is one active display.
type DisplayInfo struct {
	ID         string
	X, Y, W, H float64
	Scale      float64
	Name       string
}

func parseF(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }

// AppWindows lists the on-screen normal windows of the app front to back.
// running is false when the app is not running. Read-only; titles are empty
// without Screen Recording.
func AppWindows(bundleID string) (ws []WindowRect, running bool) {
	cs := C.CString(bundleID)
	defer C.free(unsafe.Pointer(cs))
	out := C.appWindowsC(cs)
	if out == nil {
		return nil, false
	}
	defer C.free(unsafe.Pointer(out))
	for _, ln := range strings.Split(C.GoString(out), "\n") {
		f := strings.SplitN(ln, "\t", 5)
		if len(f) < 4 {
			continue
		}
		w := WindowRect{X: parseF(f[0]), Y: parseF(f[1]), W: parseF(f[2]), H: parseF(f[3])}
		if len(f) == 5 {
			w.Title = f[4]
		}
		ws = append(ws, w)
	}
	return ws, true
}

// Displays lists the active displays. Read-only.
func Displays() []DisplayInfo {
	out := C.displaysC()
	if out == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(out))
	var ds []DisplayInfo
	for _, ln := range strings.Split(C.GoString(out), "\n") {
		f := strings.SplitN(ln, "\t", 7)
		if len(f) < 6 {
			continue
		}
		d := DisplayInfo{ID: f[0], X: parseF(f[1]), Y: parseF(f[2]), W: parseF(f[3]), H: parseF(f[4]), Scale: parseF(f[5])}
		if len(f) == 7 {
			d.Name = f[6]
		}
		ds = append(ds, d)
	}
	return ds
}

// GrabRect reads the screen inside the rectangle (global points). It needs
// Screen Recording and macOS 15.2+; otherwise it returns an error. Read-only:
// it never posts input.
func GrabRect(x, y, w, h float64) (image.Image, error) {
	if !ScreenRecordingTrusted() {
		return nil, errors.New("Screen Recording is off")
	}
	var buf *C.uchar
	var ow, oh C.int
	var cerr *C.char
	if C.grabRectC(C.double(x), C.double(y), C.double(w), C.double(h), &buf, &ow, &oh, &cerr) == 0 {
		msg := "the screen could not be read"
		if cerr != nil {
			msg = C.GoString(cerr)
			C.free(unsafe.Pointer(cerr))
		}
		return nil, errors.New(msg)
	}
	defer C.free(unsafe.Pointer(buf))
	img := image.NewRGBA(image.Rect(0, 0, int(ow), int(oh)))
	copy(img.Pix, C.GoBytes(unsafe.Pointer(buf), ow*oh*4))
	return img, nil
}
