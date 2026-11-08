// Package mousecal is the mouse-calibration engine of 2T5 DECK+: where on an
// app's window a mouse action (a colour-wheel drag, a click on a button, a
// value box) lands, stored per app in that app's preset and resolved against
// what the screen looks like NOW.
//
// It is pure Go (no cgo, no input package): everything it needs from the
// machine arrives through the System interface, so the whole flow runs in the
// --sim dev server and in tests without moving a pointer. Posting real input
// stays in internal/mouse behind its arm guard; this package only asks System
// to do it and System refuses when disarmed.
//
// Borrowed: the capture idea (read the pointer position on a keypress) and the
// target list (DeriveRequiredCalibPoints) from internal/engine + cmd/2t5-deck-plus
// --calibrate; the "calibrate under the same conditions it is used" lesson from
// ld-resolve's LD-RESOLVE calibration notes. What it replaces: absolute screen
// coordinates in config/calibration.json, which break when the window moves,
// the display changes, the window is resized or Resolve's layout changes.
package mousecal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// Pt is a point in global display points (top-left origin), the space
// internal/mouse posts in.
type Pt struct{ X, Y float64 }

// Rect is a rectangle in global display points.
type Rect struct{ X, Y, W, H float64 }

// Contains reports whether the point lies inside the rectangle.
func (r Rect) Contains(p Pt) bool {
	return p.X >= r.X && p.X <= r.X+r.W && p.Y >= r.Y && p.Y <= r.Y+r.H
}

func (r Rect) area() float64 { return r.W * r.H }

func (r Rect) inter(o Rect) float64 {
	w := math.Min(r.X+r.W, o.X+o.W) - math.Max(r.X, o.X)
	h := math.Min(r.Y+r.H, o.Y+o.H) - math.Max(r.Y, o.Y)
	if w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}

// Display is one monitor.
type Display struct {
	ID    string  `json:"id"`
	Name  string  `json:"name,omitempty"`
	Frame Rect    `json:"frame"`
	Scale float64 `json:"scale"` // backing scale: 2 on a Retina mode, 1 otherwise
}

// Class is the part of a display that decides how an app lays itself out:
// size in points and scale. Two monitors of the same class share a calibration.
func (d Display) Class() string {
	s := d.Scale
	if s <= 0 {
		s = 1
	}
	return fmt.Sprintf("%dx%d@%g", int(math.Round(d.Frame.W)), int(math.Round(d.Frame.H)), s)
}

// Window is one on-screen window of the target app.
type Window struct {
	Title string `json:"title,omitempty"`
	Frame Rect   `json:"frame"`
}

// Env is what the machine looks like now, for one app.
type Env struct {
	Running   bool
	Frontmost bool
	Windows   []Window // normal windows, front to back
	Displays  []Display
	// Layout is the app's own layout signature (for Resolve the page: "color",
	// "edit", ...). "" = the app cannot tell.
	Layout string
	// LayoutRequired: the app has pages / layouts and Layout must be known to
	// calibrate or click (Resolve, whose page comes from the sidecar). With it
	// set and Layout empty the service says "layout unknown" instead of guessing.
	LayoutRequired bool
}

// Errors of window selection.
var (
	ErrNoWindow  = errors.New("the app has no window on screen")
	ErrAmbiguous = errors.New("the app has two big windows open")
)

// minWindow is the smallest window taken for the app's main window.
const minWindowW, minWindowH = 400, 300

// PickWindow chooses the main window: the biggest on-screen window. When a
// second window is nearly as big (>= 80% of its area) but is a different
// rectangle, the choice is ambiguous: the caller must not guess.
func PickWindow(ws []Window) (Window, error) {
	var big []Window
	for _, w := range ws {
		if w.Frame.W >= minWindowW && w.Frame.H >= minWindowH {
			big = append(big, w)
		}
	}
	if len(big) == 0 {
		return Window{}, ErrNoWindow
	}
	sort.SliceStable(big, func(i, j int) bool { return big[i].Frame.area() > big[j].Frame.area() })
	if len(big) > 1 && big[1].Frame.area() >= 0.8*big[0].Frame.area() && big[1].Frame != big[0].Frame {
		return Window{}, ErrAmbiguous
	}
	return big[0], nil
}

// Context is the situation a calibration is valid for.
type Context struct {
	Display     string // Display.Class of the monitor holding most of the window
	DisplayName string
	Win         Rect
	Layout      string
}

// Label is a short human description for the editor.
func (c Context) Label() string {
	l := c.Layout
	if l == "" {
		l = "layout unknown"
	}
	return fmt.Sprintf("%s · screen %s · window %d×%d", l, c.Display, int(math.Round(c.Win.W)), int(math.Round(c.Win.H)))
}

// ContextOf derives the context from an environment.
func ContextOf(env Env) (Context, error) {
	w, err := PickWindow(env.Windows)
	if err != nil {
		return Context{}, err
	}
	best, bestA := Display{}, -1.0
	for _, d := range env.Displays {
		if a := w.Frame.inter(d.Frame); a > bestA {
			best, bestA = d, a
		}
	}
	if bestA < 0 {
		return Context{}, errors.New("no display found for the window")
	}
	return Context{Display: best.Class(), DisplayName: best.Name, Win: w.Frame, Layout: env.Layout}, nil
}

// Entry is one calibration of one target in one context. The point is stored
// relative to the window's top-left corner, with the window size it was taken
// at, so a moved window or another monitor of the same class changes nothing and
// a resized window can be extrapolated (see FitModel).
type Entry struct {
	ID      string  `json:"id"`
	At      string  `json:"at"`
	Via     string  `json:"via"` // cursor | countdown | deck-key
	Display string  `json:"display"`
	Layout  string  `json:"layout,omitempty"`
	WinW    float64 `json:"winW"`
	WinH    float64 `json:"winH"`
	PX      float64 `json:"px"`
	PY      float64 `json:"py"`
	// Axis is the drag direction of a drag target ("h", "v") or "click".
	Axis string `json:"axis,omitempty"`
	// Abs marks an imported entry: an absolute screen point (AX, AY) taken
	// before windows were tracked (ld-resolve's calibration.json). It has no window
	// size (WinW / WinH are 0) and is trusted only on the same display class and
	// layout, and only while the point lies inside the app's window. PX / PY are
	// not used. Source names where it came from.
	Abs    bool    `json:"abs,omitempty"`
	AX     float64 `json:"ax,omitempty"`
	AY     float64 `json:"ay,omitempty"`
	Source string  `json:"source,omitempty"`
	// Probe is a coarse picture of the screen around the point (hex), compared
	// before a click to notice that the layout changed. Optional.
	Probe string `json:"probe,omitempty"`
}

// Mode says how one axis of a point follows a resized window.
type Mode string

// Axis modes.
const (
	ModeFrac  Mode = "frac"  // keeps its fraction of the window size
	ModeStart Mode = "start" // keeps its distance from the left / top edge
	ModeEnd   Mode = "end"   // keeps its distance from the right / bottom edge
	ModeMid   Mode = "mid"   // keeps its distance from the window centre
	ModeNone  Mode = ""      // unknown: only the exact window size is trusted
)

var allModes = []Mode{ModeStart, ModeEnd, ModeFrac, ModeMid}

func predict(m Mode, p, size, newSize float64) float64 {
	switch m {
	case ModeFrac:
		if size == 0 {
			return p
		}
		return p / size * newSize
	case ModeStart:
		return p
	case ModeEnd:
		return newSize - (size - p)
	case ModeMid:
		return newSize/2 + (p - size/2)
	}
	return p
}

// Prior is a hypothesis about an axis that is not yet proven by two captures.
// A point extrapolated by a prior is only used after the pixel probe agrees.
type Prior struct{ X, Y Mode }

// AxisModel is the fitted behaviour of one axis.
type AxisModel struct {
	Mode    Mode    // ModeNone when nothing fits
	Learned bool    // proven by two captures at different sizes
	Err     float64 // worst prediction error of the fit, points
	Lo, Hi  float64 // window sizes (this axis) the fit was learned between
}

// Model is the fitted behaviour of a target's two axes.
type Model struct{ X, Y AxisModel }

// FitTolerance is how far (points) a mode may miss a second capture and still
// count as the rule of that axis.
const FitTolerance = 2.5

// sizeTolerance is the window-size difference (points) that still counts as the
// same size.
const sizeTolerance = 2.0

// FitModel learns, per axis, which rule predicts the other captures from one.
// es must be entries of ONE layout and display class. An axis is learned when
// two captures differ in that window dimension and exactly one family of modes
// predicts both within FitTolerance; the first matching mode in the order
// start, end, frac, mid wins.
func FitModel(es []Entry) Model {
	return Model{
		X: fitAxis(es, func(e Entry) (float64, float64) { return e.PX, e.WinW }),
		Y: fitAxis(es, func(e Entry) (float64, float64) { return e.PY, e.WinH }),
	}
}

func fitAxis(es []Entry, get func(Entry) (p, size float64)) AxisModel {
	var out AxisModel
	lo, hi := math.Inf(1), math.Inf(-1)
	differ := false
	for i, a := range es {
		_, sa := get(a)
		lo, hi = math.Min(lo, sa), math.Max(hi, sa)
		for _, b := range es[i+1:] {
			if _, sb := get(b); math.Abs(sa-sb) > sizeTolerance {
				differ = true
			}
		}
	}
	out.Lo, out.Hi = lo, hi
	if !differ {
		return out
	}
	best, bestErr := ModeNone, math.Inf(1)
	for _, m := range allModes {
		worst := 0.0
		for i, a := range es {
			pa, sa := get(a)
			for j, b := range es {
				if i == j {
					continue
				}
				pb, sb := get(b)
				if e := math.Abs(predict(m, pa, sa, sb) - pb); e > worst {
					worst = e
				}
			}
		}
		if worst < bestErr-1e-9 {
			best, bestErr = m, worst
		}
	}
	if bestErr <= FitTolerance {
		out.Mode, out.Learned, out.Err = best, true, bestErr
	} else {
		out.Err = bestErr
	}
	return out
}

// Confidence levels of a resolved point.
const (
	ConfExact   = "exact"   // same window size as when calibrated
	ConfLearned = "learned" // another window size, rule proven by two captures
	ConfPrior   = "prior"   // another window size, rule only assumed: probe required
	ConfLegacy  = "legacy"  // an old absolute screen point, not window-relative
	// ConfImported: an absolute point imported from ld-resolve's calibration into
	// the store; used while it lies inside the window, never re-verified.
	ConfImported = "imported"
)

// Status of a target in the current context.
const (
	StOK    = "ok"
	StNeeds = "needs_calibration"
)

// Reason codes of a refusal.
const (
	ReasonNone        = "not_calibrated"
	ReasonOtherLayout = "other_layout"
	ReasonOtherScreen = "other_screen"
	ReasonResized     = "window_resized"
	ReasonOutside     = "outside_window"
	ReasonImportedOff = "imported_outside_window"
)

// Result is the answer of Resolve.
type Result struct {
	Status     string
	Reason     string
	Message    string
	Point      Pt // global, valid when Status is ok
	Rel        Pt // window-relative
	Confidence string
	// NeedsProbe: the point is extrapolated from an assumption; use it only if
	// the pixel probe of Entry agrees.
	NeedsProbe bool
	Entry      *Entry // the entry the point came from
	// OtherContexts lists the contexts calibrated for, when none matches.
	OtherContexts []string
}

// layoutMatches is strict: a calibration made on one page is not valid on
// another, and one made with the page unknown is only valid with it unknown.
func layoutMatches(entry, cur string) bool { return entry == cur }

// Resolve turns the entries of one target into a global point for ctx.
func Resolve(entries []Entry, ctx Context, prior Prior) Result {
	if len(entries) == 0 {
		return Result{Status: StNeeds, Reason: ReasonNone, Message: "not calibrated yet"}
	}
	var cands []Entry
	var otherLayout, otherScreen bool
	var others []string
	for _, e := range entries {
		switch {
		case !layoutMatches(e.Layout, ctx.Layout):
			otherLayout = true
		case e.Display != ctx.Display:
			otherScreen = true
		default:
			cands = append(cands, e)
			continue
		}
		others = append(others, fmt.Sprintf("%s on %s", orUnknown(e.Layout), e.Display))
	}
	if len(cands) == 0 {
		r := Result{Status: StNeeds, OtherContexts: dedupe(others)}
		switch {
		case otherLayout:
			r.Reason = ReasonOtherLayout
			r.Message = fmt.Sprintf("needs calibration for this layout (%s on screen %s); calibrated for: %s", orUnknown(ctx.Layout), ctx.Display, join(r.OtherContexts))
		case otherScreen:
			r.Reason = ReasonOtherScreen
			r.Message = fmt.Sprintf("needs calibration for this screen (%s); calibrated for: %s", ctx.Display, join(r.OtherContexts))
		}
		return r
	}
	// newest first, so an exact match is the latest capture
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].At > cands[j].At })
	// Imported absolute points are the fallback: any window-relative calibration
	// of this layout and screen (fresh, newer) decides alone.
	var rel, abs []Entry
	for _, e := range cands {
		if e.Abs {
			abs = append(abs, e)
		} else {
			rel = append(rel, e)
		}
	}
	if len(rel) == 0 {
		return resolveImported(abs[0], ctx)
	}
	cands = rel
	cw, ch := ctx.Win.W, ctx.Win.H
	for i := range cands {
		e := cands[i]
		if math.Abs(e.WinW-cw) <= sizeTolerance && math.Abs(e.WinH-ch) <= sizeTolerance {
			return finish(ctx, e, e.PX, e.PY, ConfExact, false)
		}
	}
	model := FitModel(cands)
	// the base entry: the capture whose window is closest in size
	base := cands[0]
	bd := math.Inf(1)
	for _, e := range cands {
		if d := math.Abs(e.WinW-cw) + math.Abs(e.WinH-ch); d < bd {
			base, bd = e, d
		}
	}
	px, conf1, ok1 := axis(base.PX, base.WinW, cw, model.X, prior.X)
	py, conf2, ok2 := axis(base.PY, base.WinH, ch, model.Y, prior.Y)
	if !ok1 || !ok2 {
		return Result{Status: StNeeds, Reason: ReasonResized, Entry: &base,
			Message: fmt.Sprintf("the window is %d×%d, it was calibrated at %s; calibrate again at this size",
				int(math.Round(cw)), int(math.Round(ch)), sizes(cands))}
	}
	conf := ConfLearned
	probe := false
	if conf1 == ConfPrior || conf2 == ConfPrior {
		conf, probe = ConfPrior, true
	}
	if conf1 == ConfExact && conf2 == ConfExact {
		conf = ConfExact
	}
	return finish(ctx, base, px, py, conf, probe)
}

// axis predicts one coordinate at a new window size.
func axis(p, size, newSize float64, m AxisModel, prior Mode) (v float64, conf string, ok bool) {
	if math.Abs(size-newSize) <= sizeTolerance {
		return p, ConfExact, true
	}
	if m.Learned {
		// trust a learned rule only a little beyond the sizes it was learned between
		slack := 0.10 * m.Hi
		if newSize >= m.Lo-slack && newSize <= m.Hi+slack {
			return predict(m.Mode, p, size, newSize), ConfLearned, true
		}
		return 0, "", false
	}
	if prior != ModeNone {
		return predict(prior, p, size, newSize), ConfPrior, true
	}
	return 0, "", false
}

// resolveImported serves an imported absolute point while it lies inside the
// window. It does not know the window it was taken in, so it is never "exact".
func resolveImported(e Entry, ctx Context) Result {
	p := Pt{e.AX, e.AY}
	if !ctx.Win.Contains(p) {
		return Result{Status: StNeeds, Reason: ReasonImportedOff, Entry: &e,
			Message: fmt.Sprintf("imported from %s, but that screen spot is outside the window now (the window moved); calibrate again", orUnknown2(e.Source, "the old calibration"))}
	}
	return Result{Status: StOK, Point: p, Rel: Pt{p.X - ctx.Win.X, p.Y - ctx.Win.Y}, Confidence: ConfImported, Entry: &e}
}

func orUnknown2(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func finish(ctx Context, e Entry, px, py float64, conf string, probe bool) Result {
	r := Result{Status: StOK, Rel: Pt{px, py}, Point: Pt{ctx.Win.X + px, ctx.Win.Y + py}, Confidence: conf, NeedsProbe: probe, Entry: &e}
	if px < 0 || py < 0 || px > ctx.Win.W || py > ctx.Win.H {
		return Result{Status: StNeeds, Reason: ReasonOutside, Entry: &e,
			Message: "the calibrated spot would fall outside the window at this size; calibrate again"}
	}
	return r
}

func orUnknown(s string) string {
	if s == "" {
		return "layout unknown"
	}
	return s
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += "; "
		}
		out += v
	}
	return out
}

func sizes(es []Entry) string {
	var s []string
	for _, e := range es {
		s = append(s, fmt.Sprintf("%d×%d", int(math.Round(e.WinW)), int(math.Round(e.WinH))))
	}
	return join(dedupe(s))
}

// NewEntry builds the entry for a pointer position p captured in ctx.
func NewEntry(ctx Context, p Pt, via, axis string, now time.Time) Entry {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return Entry{
		ID: fmt.Sprintf("c%x-%s", now.UnixMilli(), hex.EncodeToString(b[:])),
		At: now.UTC().Format(time.RFC3339), Via: via,
		Display: ctx.Display, Layout: ctx.Layout, WinW: round2(ctx.Win.W), WinH: round2(ctx.Win.H),
		PX: round2(p.X - ctx.Win.X), PY: round2(p.Y - ctx.Win.Y), Axis: axis,
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// SameContext reports whether e was taken in the same layout, display class and
// window size (within the tolerance) as ctx: capturing it again replaces it.
func (e Entry) SameContext(ctx Context) bool {
	return e.Display == ctx.Display && e.Layout == ctx.Layout &&
		math.Abs(e.WinW-ctx.Win.W) <= sizeTolerance && math.Abs(e.WinH-ctx.Win.H) <= sizeTolerance
}
