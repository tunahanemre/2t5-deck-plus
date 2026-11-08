package mousecal

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"time"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/preset"
)

// Capture states.
const (
	CWaiting   = "waiting"   // armed for a deck key or counting down
	CCaptured  = "captured"  // a point was read; waiting for confirm / redo
	CFailed    = "failed"    // could not capture (message says why)
	CConfirmed = "confirmed" // saved
	CDiscarded = "discarded"
	CExpired   = "expired"
)

// Capture sources.
const (
	ViaNow       = "cursor"    // read the pointer right now
	ViaCountdown = "countdown" // read it after a delay
	ViaDeckKey   = "deck-key"  // read it when a deck button is pressed
)

// Errors a caller maps to HTTP statuses.
var (
	ErrNoTarget   = errors.New("this app has no such mouse target")
	ErrNoCapture  = errors.New("no such capture (it expired or was already used)")
	ErrWrongState = errors.New("the capture is not in a state that allows this")
)

type pending struct {
	id       string
	app      string
	key      string
	via      string
	state    string
	msg      string
	fix      string
	deadline time.Time
	created  time.Time
	entry    Entry
	ctx      Context
	pt       Pt
	pic      image.Image
	cancel   func()
	replaces []string
}

// Marker is where the captured point lies, for the editor to draw.
type Marker struct {
	Screen Pt   `json:"screen"`
	Window Rect `json:"window"`
	// Pct is the point as a fraction (0..1) of the window, for a schematic.
	Pct Pt `json:"pct"`
}

// PendingView is a capture as the editor sees it.
type PendingView struct {
	ID         string  `json:"id"`
	App        string  `json:"app"`
	Target     string  `json:"target"`
	Label      string  `json:"label"`
	Via        string  `json:"via"`
	State      string  `json:"state"`
	Message    string  `json:"message,omitempty"`
	Fix        string  `json:"fix,omitempty"`
	DeadlineMs int64   `json:"deadlineMs,omitempty"`
	Marker     *Marker `json:"marker,omitempty"`
	Context    string  `json:"context,omitempty"`
	HasPreview bool    `json:"hasPreview"`
	// Replaces: how many stored calibrations of this target this one replaces
	// (same layout, screen and window size).
	Replaces int `json:"replaces"`
}

func (s *Service) viewOf(p *pending) *PendingView {
	l, _, _, _ := Describe(p.app, p.key)
	v := &PendingView{ID: p.id, App: p.app, Target: p.key, Label: l, Via: p.via, State: p.state, Message: p.msg, Fix: p.fix, Replaces: len(p.replaces)}
	if !p.deadline.IsZero() && p.state == CWaiting {
		v.DeadlineMs = p.deadline.UnixMilli()
	}
	if p.state == CCaptured || p.state == CConfirmed {
		v.Marker = &Marker{Screen: p.pt, Window: p.ctx.Win,
			Pct: Pt{(p.pt.X - p.ctx.Win.X) / p.ctx.Win.W, (p.pt.Y - p.ctx.Win.Y) / p.ctx.Win.H}}
		v.Context = p.ctx.Label()
		v.HasPreview = p.pic != nil
	}
	return v
}

func (s *Service) activeView(appID string) *PendingView {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *pending
	for _, p := range s.pend {
		if p.app == appID && (best == nil || p.created.After(best.created)) {
			best = p
		}
	}
	if best == nil || best.state == CConfirmed || best.state == CDiscarded {
		return nil
	}
	return s.viewOf(best)
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Service) known(appID, key string) (Target, bool) {
	if s.Preset == nil {
		return Target{}, false
	}
	for _, t := range DeriveTargets(s.Preset(appID)) {
		if t.Key == key {
			return t, true
		}
	}
	return Target{}, false
}

// StartCapture begins a capture of one target. Only one capture per app is
// live: starting another ends the previous. Nothing is saved until Confirm.
// A capture only READS the pointer; it never posts input.
func (s *Service) StartCapture(appID, key, via string, delay time.Duration) (*PendingView, error) {
	t, ok := s.known(appID, key)
	if !ok {
		return nil, ErrNoTarget
	}
	switch via {
	case ViaNow, ViaCountdown, ViaDeckKey:
	case "":
		via = ViaNow
	default:
		return nil, &actions.FieldError{Field: "via", Msg: fmt.Sprintf("unknown capture source %q (cursor | countdown | deck-key)", via)}
	}
	p := &pending{id: newID(), app: appID, key: key, via: via, created: s.now()}
	s.mu.Lock()
	if s.pend == nil {
		s.pend = map[string]*pending{}
	}
	for id, o := range s.pend {
		if o.app == appID {
			if o.cancel != nil {
				o.cancel()
			}
			delete(s.pend, id)
		}
		if s.now().Sub(o.created) > PendingTTL {
			delete(s.pend, id)
		}
	}
	s.pend[p.id] = p
	s.mu.Unlock()

	switch via {
	case ViaNow:
		s.take(p, t)
	case ViaCountdown:
		if delay <= 0 {
			delay = DefaultCountdown
		}
		if delay > MaxCountdown {
			delay = MaxCountdown
		}
		s.mu.Lock()
		p.state, p.deadline = CWaiting, s.now().Add(delay)
		p.cancel = s.after(delay, func() { s.take(p, t) })
		s.mu.Unlock()
	case ViaDeckKey:
		s.mu.Lock()
		p.state, p.deadline = CWaiting, s.now().Add(DeckKeyTimeout)
		p.cancel = s.after(DeckKeyTimeout, func() {
			s.mu.Lock()
			if p.state == CWaiting {
				p.state, p.msg = CExpired, "no deck button was pressed in time"
			}
			s.mu.Unlock()
		})
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewOf(p), nil
}

// DeckKeyWaiting reports whether a capture is waiting for a deck button.
func (s *Service) DeckKeyWaiting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pend {
		if p.state == CWaiting && p.via == ViaDeckKey {
			return true
		}
	}
	return false
}

// OnDeckKey is called by the engine for every deck button press. It returns
// true when the press was used as the capture trigger: the engine must then
// swallow it (the button does nothing else this time).
func (s *Service) OnDeckKey() bool {
	s.mu.Lock()
	var hit *pending
	for _, p := range s.pend {
		if p.state == CWaiting && p.via == ViaDeckKey && (hit == nil || p.created.After(hit.created)) {
			hit = p
		}
	}
	s.mu.Unlock()
	if hit == nil {
		return false
	}
	t, _ := s.known(hit.app, hit.key)
	s.take(hit, t)
	return true
}

// take reads the pointer and fills the pending capture. Read-only.
func (s *Service) take(p *pending, t Target) {
	fail := func(code, msg, fix string) {
		s.mu.Lock()
		p.state, p.msg, p.fix = CFailed, msg, fix
		s.mu.Unlock()
	}
	s.dropEnv(p.app)
	name := s.name(p.app)
	env, ctx, prob := s.context(p.app)
	if prob != nil {
		fail(prob.Code, prob.Message, prob.Fix)
		return
	}
	cur := s.Sys.Cursor()
	if !env.Frontmost {
		fail(PNotFront, name+" was not the front app when the point was read, so the pointer is probably over something else", "Click "+name+" to bring it to the front, hover the target, then capture again")
		return
	}
	if !ctx.Win.Contains(cur) {
		fail("outside_window", fmt.Sprintf("the pointer was not over %s's window (it was at %d, %d)", name, int(cur.X), int(cur.Y)), "Hover the target inside the "+name+" window, then capture again")
		return
	}
	e := NewEntry(ctx, cur, p.via, t.Axis, s.now())
	var pic image.Image
	if _, scr := s.Sys.Perms(); scr {
		if img, err := s.Sys.Grab(cur, ProbeSize); err == nil && img != nil {
			e.Probe = ProbeOf(img)
		}
		if img, err := s.Sys.Grab(cur, PreviewSize); err == nil {
			pic = img
		}
	}
	blk, _ := s.Store.Get(p.app)
	var rep []string
	for _, o := range blk.Entries(p.key) {
		if o.SameContext(ctx) {
			rep = append(rep, o.ID)
		}
	}
	s.mu.Lock()
	p.state, p.entry, p.ctx, p.pt, p.pic, p.replaces, p.deadline = CCaptured, e, ctx, cur, pic, rep, time.Time{}
	s.mu.Unlock()
}

// PreviewSize is the side (display points) of the picture shown back to the user.
const PreviewSize = 360

func (s *Service) pendingFor(appID, id string) (*pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pend[id]
	if p == nil || p.app != appID {
		return nil, ErrNoCapture
	}
	return p, nil
}

// Capture returns the current state of a capture.
func (s *Service) Capture(appID, id string) (*PendingView, error) {
	p, err := s.pendingFor(appID, id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewOf(p), nil
}

// Confirm saves a captured point into the app's preset as ONE undoable change.
func (s *Service) Confirm(appID, id string) (*PendingView, error) {
	p, err := s.pendingFor(appID, id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	st, e, key := p.state, p.entry, p.key
	s.mu.Unlock()
	if st != CCaptured {
		return nil, fmt.Errorf("%w: it is %s", ErrWrongState, st)
	}
	label, _, _, _ := Describe(appID, key)
	if err := s.Store.Update(appID, "Calibrate "+label, func(b Block) (Block, error) { return b.WithEntry(key, e), nil }); err != nil {
		return nil, err
	}
	s.mu.Lock()
	p.state = CConfirmed
	s.mu.Unlock()
	s.dropEnv(appID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewOf(p), nil
}

// Discard ends a capture without saving ("redo" starts a new one).
func (s *Service) Discard(appID, id string) (*PendingView, error) {
	p, err := s.pendingFor(appID, id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	if p.state != CConfirmed {
		p.state = CDiscarded
	}
	return s.viewOf(p), nil
}

// PreviewPNG draws the captured spot back: the screen around the point as it
// was at capture, with a marker on it. Needs Screen Recording at capture time.
func (s *Service) PreviewPNG(appID, id string) ([]byte, error) {
	p, err := s.pendingFor(appID, id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	pic, st := p.pic, p.state
	s.mu.Unlock()
	if pic == nil || (st != CCaptured && st != CConfirmed) {
		return nil, errors.New("no picture: Screen Recording is off or nothing was captured")
	}
	return MarkedPNG(pic), nil
}

// MarkedPNG draws a crosshair ring (orange accent, dark outline) on the centre of img and encodes it.
func MarkedPNG(img image.Image) []byte {
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, img, b.Min, draw.Src)
	cx, cy := (b.Min.X+b.Max.X)/2, (b.Min.Y+b.Max.Y)/2
	r := b.Dx() / 14
	if r < 8 {
		r = 8
	}
	// the editor's orange accent ring, edged in near-black so it reads on light and dark screens alike
	mag, white := color.RGBA{255, 122, 26, 255}, color.RGBA{18, 18, 20, 255}
	for a := 0.0; a < 2*math.Pi; a += 0.01 {
		for t := -1; t <= 1; t++ {
			rr := float64(r + t)
			c := mag
			if t != 0 {
				c = white
			}
			dst.Set(cx+int(math.Round(rr*math.Cos(a))), cy+int(math.Round(rr*math.Sin(a))), c)
		}
	}
	for d := -r - 6; d <= r+6; d++ {
		if d > -3 && d < 3 {
			continue
		}
		dst.Set(cx+d, cy-1, white)
		dst.Set(cx+d, cy+1, white)
		dst.Set(cx-1, cy+d, white)
		dst.Set(cx+1, cy+d, white)
		dst.Set(cx+d, cy, mag)
		dst.Set(cx, cy+d, mag)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, dst)
	return buf.Bytes()
}

// ---- test (dry run / real) ------------------------------------------------

// TestResult is the answer of Test.
type TestResult struct {
	Target     string     `json:"target"`
	Label      string     `json:"label"`
	Mode       string     `json:"mode"` // move | click
	Found      bool       `json:"found"`
	Point      *Pt        `json:"point,omitempty"`
	Rel        *Pt        `json:"rel,omitempty"`
	Marker     *Marker    `json:"marker,omitempty"`
	Confidence string     `json:"confidence,omitempty"`
	Moved      bool       `json:"moved"`
	Clicked    bool       `json:"clicked"`
	Probe      ProbeCheck `json:"probe"`
	Warn       string     `json:"warn,omitempty"`
	Problem    *Problem   `json:"problem,omitempty"`
	Message    string     `json:"message"`
	NeedsRecal bool       `json:"needsCalibration"`
}

// Test locates a target and, in mode "move", moves the pointer there WITHOUT
// clicking; in mode "click" it also clicks, but only when confirm is true, the
// switch is armed and the app is in front. When the process is not armed the
// pointer does not move either (Moved false), the answer still names the spot.
func (s *Service) Test(appID, key, mode string, confirm bool) (TestResult, error) {
	t, ok := s.known(appID, key)
	if !ok {
		return TestResult{}, ErrNoTarget
	}
	if mode == "" {
		mode = "move"
	}
	if mode != "move" && mode != "click" {
		return TestResult{}, &actions.FieldError{Field: "mode", Msg: fmt.Sprintf("unknown test mode %q (move | click)", mode)}
	}
	r := TestResult{Target: key, Label: t.Label, Mode: mode}
	s.dropEnv(appID)
	l := s.Locate(appID, key, LocateOpts{Input: mode == "click", RequireFront: mode == "click"})
	r.Probe, r.Warn = l.Probe, l.Warn
	if l.Blocked != nil {
		r.Problem, r.Message = l.Blocked, l.Blocked.Message
		r.NeedsRecal = l.Blocked.Code == PUnverified || l.Blocked.Code == PLayoutChanged
		return r, nil
	}
	if l.Status != StOK {
		r.Message, r.NeedsRecal = l.Message, true
		return r, nil
	}
	pt, rel := l.Point, l.Rel
	r.Found, r.Point, r.Rel, r.Confidence = true, &pt, &rel, l.Confidence
	if l.Ctx != nil {
		r.Marker = &Marker{Screen: pt, Window: l.Ctx.Win, Pct: Pt{rel.X / l.Ctx.Win.W, rel.Y / l.Ctx.Win.H}}
	}
	switch mode {
	case "move":
		r.Moved = s.Sys.MoveTo(pt)
		if r.Moved {
			r.Message = "The pointer is on the spot. Nothing was clicked."
		} else {
			r.Message = "This is the spot (marked on the picture). The pointer was not moved because the deck is disarmed."
		}
	case "click":
		switch {
		case !confirm:
			r.Message = "A real click needs confirm: true."
		case !s.Sys.Armed():
			r.Message = "The deck is disarmed: no click was sent. Arm it first."
		default:
			if s.Sys.MoveTo(pt) {
				r.Moved = true
			}
			r.Clicked = s.Sys.Click(pt)
			if r.Clicked {
				r.Message = "Clicked once on the spot."
			} else {
				r.Message = "Nothing was clicked (input is switched off in this process)."
			}
		}
	}
	return r, nil
}

// ---- clear, settings, session ----------------------------------------------

// Clear removes one stored calibration (id) or every one of the target (id "").
func (s *Service) Clear(appID, key, id string) error {
	if _, ok := s.known(appID, key); !ok {
		return ErrNoTarget
	}
	label, _, _, _ := Describe(appID, key)
	what := "Clear calibration of "
	if id != "" {
		what = "Clear one calibration of "
	}
	err := s.Store.Update(appID, what+label, func(b Block) (Block, error) {
		if id != "" {
			found := false
			for _, e := range b.Entries(key) {
				found = found || e.ID == id
			}
			if !found {
				return b, ErrNoCapture
			}
		}
		return b.Without(key, id), nil
	})
	s.dropEnv(appID)
	return err
}

// SetOptions changes the calibration settings of an app. nil = unchanged.
func (s *Service) SetOptions(appID string, verify *string, blockLegacy *bool) error {
	if verify != nil {
		switch *verify {
		case VerifyBlock, VerifyWarn, VerifyOff:
		default:
			return &actions.FieldError{Field: "verify", Msg: "verify must be block, warn or off"}
		}
	}
	return s.Store.Update(appID, "Calibration settings", func(b Block) (Block, error) {
		if verify != nil {
			b.Verify = *verify
			if b.Verify == VerifyBlock {
				b.Verify = ""
			}
		}
		if blockLegacy != nil {
			b.BlockLegacy = *blockLegacy
		}
		return b, nil
	})
}

// Step is one step of a guided session.
type Step struct {
	Target
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
}

// Session lists, in order, the targets a guided pass should capture.
// scope "missing": the ones that are not calibrated for this context.
// scope "layout": every target, to redo them all for this layout.
func (s *Service) Session(appID, scope string) ([]Step, *Problem, error) {
	ov, err := s.Overview(appID)
	if err != nil {
		return nil, nil, err
	}
	if ov.Problem != nil {
		return []Step{}, ov.Problem, nil
	}
	steps := []Step{}
	for _, t := range ov.Targets {
		if scope == "layout" || t.Status != TCalibrated {
			steps = append(steps, Step{Target: t.Target, Status: t.Status, StatusText: t.StatusText})
		}
	}
	return steps, nil, nil
}

// ---- adapter over the preset store ----------------------------------------

// PresetSettings stores the block in a preset's settings (undoable).
type PresetSettings struct{ Store *preset.Store }

// Get reads the block of an app.
func (ps PresetSettings) Get(appID string) (Block, error) {
	p, ok := ps.Store.Snapshot().Preset(appID)
	if !ok {
		return Block{}, fmt.Errorf("no preset %q", appID)
	}
	return ParseBlock(p.Settings[SettingKey])
}

// Update applies fn to the block inside one store change.
func (ps PresetSettings) Update(appID, label string, fn func(Block) (Block, error)) error {
	return ps.Store.UpdateSetting(appID, SettingKey, label, func(old any) (any, error) {
		b, err := ParseBlock(old)
		if err != nil {
			return nil, err
		}
		nb, err := fn(b)
		if err != nil {
			return nil, err
		}
		if len(nb.Targets) == 0 && nb.Verify == "" && !nb.BlockLegacy && nb.Import == nil {
			return nil, nil // nothing left: remove the key
		}
		return nb.Value(), nil
	})
}
