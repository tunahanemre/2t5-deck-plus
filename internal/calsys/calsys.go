// Package calsys is the macOS implementation of mousecal.System: windows and
// displays from CoreGraphics, the pointer and posting from internal/mouse
// (arm-guarded), pixels from ScreenCaptureKit. Cgo lives in macapp and mouse;
// this package is plumbing. Its tests use the pure helpers only: nothing here
// is exercised against the real desktop under go test.
package calsys

import (
	"errors"
	"image"
	"strings"

	"2t5-deck-plus/internal/macapp"
	"2t5-deck-plus/internal/mouse"
	"2t5-deck-plus/internal/mousecal"
)

// System implements mousecal.System on macOS.
type System struct {
	// Layout returns the app's layout signature (Resolve: its page). nil = "".
	Layout func(appID string) string
}

var _ mousecal.System = (*System)(nil)

// Env implements mousecal.System.
func (s *System) Env(appID string) (mousecal.Env, error) {
	ws, running := macapp.AppWindows(appID)
	env := mousecal.Env{Running: running, Displays: convertDisplays(macapp.Displays())}
	if !running {
		return env, nil
	}
	env.Frontmost = strings.EqualFold(macapp.FrontmostBundleID(), appID)
	for _, w := range ws {
		env.Windows = append(env.Windows, mousecal.Window{Title: w.Title, Frame: mousecal.Rect{X: w.X, Y: w.Y, W: w.W, H: w.H}})
	}
	if s.Layout != nil {
		env.Layout = s.Layout(appID)
	}
	return env, nil
}

func convertDisplays(ds []macapp.DisplayInfo) []mousecal.Display {
	out := make([]mousecal.Display, 0, len(ds))
	for _, d := range ds {
		out = append(out, mousecal.Display{ID: d.ID, Name: d.Name, Frame: mousecal.Rect{X: d.X, Y: d.Y, W: d.W, H: d.H}, Scale: d.Scale})
	}
	return out
}

// AppName implements mousecal.System ("" when unknown).
func (s *System) AppName(appID string) string { return macapp.AppName(appID) }

// Cursor implements mousecal.System (read-only).
func (s *System) Cursor() mousecal.Pt {
	x, y := mouse.CursorPos()
	return mousecal.Pt{X: x, Y: y}
}

// MoveTo implements mousecal.System: arm-guarded in internal/mouse.
func (s *System) MoveTo(p mousecal.Pt) bool { return mouse.MoveTo(p.X, p.Y) }

// Click implements mousecal.System: only when internal/mouse would post.
func (s *System) Click(p mousecal.Pt) bool {
	if !mouse.CanPost() {
		return false
	}
	mouse.Click(p.X, p.Y)
	return true
}

// Grab implements mousecal.System.
func (s *System) Grab(c mousecal.Pt, size float64) (image.Image, error) {
	img, err := macapp.GrabRect(c.X-size/2, c.Y-size/2, size, size)
	if err != nil {
		return nil, err
	}
	if img == nil {
		return nil, errors.New("no image")
	}
	return img, nil
}

// Perms implements mousecal.System.
func (s *System) Perms() (bool, bool) {
	return macapp.AccessibilityTrusted(), macapp.ScreenRecordingTrusted()
}

// Armed implements mousecal.System: real input is allowed right now.
func (s *System) Armed() bool { return mouse.CanPost() }
