package macapp

/*
#cgo LDFLAGS: -framework CoreMIDI -framework CoreFoundation
#include <CoreMIDI/CoreMIDI.h>
#include <CoreFoundation/CoreFoundation.h>

// midiSourceCount / midiSourceName only LIST the Mac's MIDI sources. They create
// no client and no port and connect to nothing, so a running deck app is not
// disturbed and nothing is ever opened.
static int midiSourceCount(void) { return (int)MIDIGetNumberOfSources(); }

static int midiSourceName(int i, char *buf, int n) {
    MIDIEndpointRef e = MIDIGetSource((ItemCount)i);
    if (e == 0) return 0;
    CFStringRef s = NULL;
    OSStatus st = MIDIObjectGetStringProperty(e, kMIDIPropertyDisplayName, &s);
    if (st != noErr || s == NULL) {
        st = MIDIObjectGetStringProperty(e, kMIDIPropertyName, &s);
        if (st != noErr || s == NULL) return 0;
    }
    Boolean ok = CFStringGetCString(s, buf, n, kCFStringEncodingUTF8);
    CFRelease(s);
    return ok ? 1 : 0;
}
*/
import "C"

import (
	"strings"
	"syscall"
)

// MIDISourceNames lists the display names of the Mac's MIDI sources. Read
// only: it opens nothing (see the C helpers above).
func MIDISourceNames() ([]string, error) {
	n := int(C.midiSourceCount())
	names := make([]string, 0, n)
	var buf [512]C.char
	for i := 0; i < n; i++ {
		if C.midiSourceName(C.int(i), &buf[0], C.int(len(buf))) == 1 {
			names = append(names, C.GoString(&buf[0]))
		}
	}
	return names, nil
}

// OSVersion is the macOS product version ("15.3.1") from the kernel.
func OSVersion() string {
	v, err := syscall.Sysctl("kern.osproductversion")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}
