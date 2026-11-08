package keys

/*
#cgo LDFLAGS: -framework AppKit -framework CoreGraphics
void ldPostMediaKey(long long data1, unsigned long long flags);
*/
import "C"

// sendMedia posts a media key press (down then up) for spec, behind the same
// guard as Send: while unarmed or paused it posts nothing and records a
// NoopCall (Kind "media"). Spec errors are returned identically either way.
func sendMedia(spec string) error {
	code, err := ParseMediaSpec(spec)
	if err != nil {
		return err
	}
	if !canPost() {
		recordNoop(spec, 0, "media")
		return nil
	}
	C.ldPostMediaKey(C.longlong(MediaData1(code, true)), C.ulonglong(MediaModifierFlags(true)))
	C.ldPostMediaKey(C.longlong(MediaData1(code, false)), C.ulonglong(MediaModifierFlags(false)))
	return nil
}
