package macapp

import (
	"bytes"
	"image/png"
	"runtime"
	"testing"
)

// Read-only Launch Services lookup; touches no input, MIDI or app state.
func TestAppIconPNG(t *testing.T) {
	if AppIconPNG("") != nil || AppIconPNG("no.such.app.zzz") != nil {
		t.Fatal("empty or unknown bundle id must give nil")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	b := AppIconPNG("com.apple.finder")
	if b == nil {
		t.Skip("Finder icon not available in this environment")
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if bd := img.Bounds(); bd.Dx() != 256 || bd.Dy() != 256 {
		t.Errorf("size %v", bd)
	}
}
