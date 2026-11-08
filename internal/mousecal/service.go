package mousecal

import (
	"errors"
	"fmt"
	"image"
	"math"
	"sort"
	"sync"
	"time"

	"2t5-deck-plus/internal/preset"
)

// System is everything the service needs from the machine. The packaged app
// implements it over macapp + internal/mouse; the dev server over a fake desktop.
type System interface {
	// Env describes the app now. A not-running app is Env{Running:false}, not an error.
	Env(appID string) (Env, error)
	// AppName is the display name of the app.
	AppName(appID string) string
	// Cursor reads the pointer position. Read-only.
	Cursor() Pt
	// MoveTo moves the pointer WITHOUT clicking. false = nothing was done
	// (the process is not armed, or the safety switch is off).
	MoveTo(p Pt) bool
	// Click posts a real click. false = nothing was done (same guard).
	Click(p Pt) bool
	// Grab returns the screen around centre, size x size display points.
	// nil + error when Screen Recording is missing or the host cannot.
	Grab(centre Pt, size float64) (image.Image, error)
	// Perms reports Accessibility and Screen Recording.
	Perms() (accessibility, screenRecording bool)
	// Armed is the editor's safety switch (true = real input allowed).
	Armed() bool
}

// SettingsStore reads and atomically updates an app's calibration block.
type SettingsStore interface {
	Get(appID string) (Block, error)
	Update(appID, label string, fn func(Block) (Block, error)) error
}

// Service is the calibration engine for all apps.
type Service struct {
	Sys   System
	Store SettingsStore
	// Preset returns the preset of an app (for the target list); nil = unknown app.
	Preset func(appID string) *preset.Preset
	// Legacy looks up an old absolute point of calibration.json (nil = none).
	Legacy func(key string) (p Pt, axis string, ok bool)
	Now    func() time.Time
	Logf   func(format string, args ...any)
	// After schedules fn (default time.AfterFunc); tests inject their own.
	After func(d time.Duration, fn func()) (stop func())

	mu     sync.Mutex
	pend   map[string]*pending
	env    map[string]envEntry
	blocks []Blocked
	logged map[string]time.Time
	probes map[string]probeEntry
}

type envEntry struct {
	at  time.Time
	env Env
	err error
}

type probeEntry struct {
	at time.Time
	pc ProbeCheck
}

// EnvTTL is how long a read of the desktop is reused (the engine asks per tick).
const EnvTTL = 250 * time.Millisecond

// ProbeTTL is how long the result of a layout check is reused for one spot.
const ProbeTTL = 3 * time.Second

// Capture timings.
const (
	DefaultCountdown = 5 * time.Second
	MaxCountdown     = 30 * time.Second
	DeckKeyTimeout   = 60 * time.Second
	PendingTTL       = 10 * time.Minute
)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

func (s *Service) after(d time.Duration, fn func()) func() {
	if s.After != nil {
		return s.After(d, fn)
	}
	t := time.AfterFunc(d, fn)
	return func() { t.Stop() }
}

func (s *Service) getEnv(appID string) (Env, error) {
	s.mu.Lock()
	if e, ok := s.env[appID]; ok && s.now().Sub(e.at) < EnvTTL {
		s.mu.Unlock()
		return e.env, e.err
	}
	s.mu.Unlock()
	env, err := s.Sys.Env(appID)
	s.mu.Lock()
	if s.env == nil {
		s.env = map[string]envEntry{}
	}
	s.env[appID] = envEntry{s.now(), env, err}
	s.mu.Unlock()
	return env, err
}

func (s *Service) dropEnv(appID string) {
	s.mu.Lock()
	delete(s.env, appID)
	s.mu.Unlock()
}

// Problem is a reason the app cannot be calibrated or clicked right now.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
	// SettingsURL deep-links the macOS privacy pane to switch on.
	SettingsURL string `json:"settingsUrl,omitempty"`
}

// Privacy pane links.
const (
	URLAccessibility   = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
	URLScreenRecording = "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
)

// Problem codes.
const (
	PNotRunning    = "app_not_running"
	PNoWindow      = "no_window"
	PAmbiguous     = "ambiguous_windows"
	PNotFront      = "not_frontmost"
	PNoAccess      = "no_accessibility"
	PNoScreenRec   = "no_screen_recording"
	PLayoutChanged = "layout_changed"
	PUnverified    = "unverified_resize"
	PNoTarget      = "unknown_target"
	PLayoutUnknown = "layout_unknown"
)

// context reads the world and returns the context or the problem that stops it.
func (s *Service) name(appID string) string {
	if n := s.Sys.AppName(appID); n != "" {
		return n
	}
	if s.Preset != nil {
		if p := s.Preset(appID); p != nil && p.Name != "" {
			return p.Name
		}
	}
	return appID
}

func (s *Service) context(appID string) (Env, Context, *Problem) {
	name := s.name(appID)
	env, err := s.getEnv(appID)
	if err != nil {
		return env, Context{}, &Problem{Code: PNoWindow, Message: "cannot read " + name + "'s windows: " + err.Error()}
	}
	if !env.Running {
		return env, Context{}, &Problem{Code: PNotRunning, Message: name + " is not running", Fix: "Open " + name + " (with a project window), then try again"}
	}
	if env.LayoutRequired && env.Layout == "" {
		return env, Context{}, &Problem{Code: PLayoutUnknown, Message: name + "'s page is not known right now (its link to 2T5 DECK+ is not connected yet)",
			Fix: "Wait a moment for " + name + " to connect, or check the Resolve sidecar in the preflight list"}
	}
	ctx, err := ContextOf(env)
	switch {
	case errors.Is(err, ErrNoWindow):
		return env, Context{}, &Problem{Code: PNoWindow, Message: name + " has no window on screen", Fix: "Bring a " + name + " window onto a screen (not minimised, not on another desktop)"}
	case errors.Is(err, ErrAmbiguous):
		return env, Context{}, &Problem{Code: PAmbiguous, Message: name + " has two big windows open and the calibration cannot tell which one is the main one", Fix: "Close or shrink the extra window, then try again"}
	case err != nil:
		return env, Context{}, &Problem{Code: PNoWindow, Message: err.Error()}
	}
	return env, ctx, nil
}

// ProbeCheck is the outcome of the pixel check at a spot.
type ProbeCheck struct {
	Checked bool    `json:"checked"`
	OK      bool    `json:"ok"`
	Dist    float64 `json:"dist"`
	Skipped string  `json:"skipped,omitempty"` // why it did not run
}

// Located is the answer of Locate.
type Located struct {
	Result
	Target  string
	Ctx     *Context
	Probe   ProbeCheck
	Blocked *Problem // set: do not click; Message says why
	Warn    string   // a non-blocking note (verify mode warn)
}

// OK reports whether the point may be used.
func (l Located) OK() bool { return l.Blocked == nil && l.Status == StOK }

// Say is the sentence for the user, whatever the outcome.
func (l Located) Say() string {
	if l.Blocked != nil {
		return l.Blocked.Message
	}
	return l.Message
}

// LocateOpts controls a Locate.
type LocateOpts struct {
	// Input: the point will be used for real input (Accessibility needed).
	Input bool
	// RequireFront: the app must be frontmost.
	RequireFront bool
}

// Locate resolves a target for the app NOW and checks that the screen still
// looks like it did at calibration. It never posts anything.
func (s *Service) Locate(appID, key string, o LocateOpts) Located {
	out := Located{Target: key}
	env, ctx, prob := s.context(appID)
	if prob != nil {
		out.Blocked = prob
		out.Status = StNeeds
		return out
	}
	out.Ctx = &ctx
	acc, scr := s.Sys.Perms()
	if o.Input && !acc {
		out.Blocked = &Problem{Code: PNoAccess, Message: "Accessibility permission is off: no click or drag can be sent",
			Fix: "System Settings > Privacy & Security > Accessibility: switch 2T5 DECK+ on (off and on again if it already is)", SettingsURL: URLAccessibility}
		return out
	}
	if o.RequireFront && !env.Frontmost {
		out.Blocked = &Problem{Code: PNotFront, Message: s.name(appID) + " is not the front app", Fix: "Click " + s.name(appID) + " to bring it to the front"}
		return out
	}
	blk, err := s.Store.Get(appID)
	if err != nil {
		out.Blocked = &Problem{Code: PNoTarget, Message: "cannot read the calibration: " + err.Error()}
		return out
	}
	entries := blk.Entries(key)
	res := refuseImported(Resolve(entries, ctx, PriorFor(appID)), blk)
	if res.Status != StOK && res.Reason == ReasonNone && !blk.BlockLegacy && s.Legacy != nil {
		if p, axis, ok := s.Legacy(key); ok {
			out.Result = Result{Status: StOK, Point: p, Rel: Pt{p.X - ctx.Win.X, p.Y - ctx.Win.Y}, Confidence: ConfLegacy,
				Message: "old absolute point (calibrated before windows were tracked): calibrate again to make it follow the window"}
			_ = axis
			return out
		}
	}
	out.Result = res
	if res.Status != StOK {
		return out
	}
	// the layout check
	mode := blk.VerifyMode()
	switch {
	case mode == VerifyOff && !res.NeedsProbe:
		out.Probe = ProbeCheck{Skipped: "layout check is off"}
	case res.Entry == nil || res.Entry.Probe == "":
		out.Probe = ProbeCheck{Skipped: "no picture was stored with this calibration"}
	case !scr:
		out.Probe = ProbeCheck{Skipped: "Screen Recording is off"}
	default:
		out.Probe = s.probeAt(appID, key, res.Point, res.Entry.Probe)
		if !out.Probe.Checked {
			out.Probe.Skipped = "the screen could not be read"
		}
	}
	if res.NeedsProbe && !(out.Probe.Checked && out.Probe.OK) {
		why := "the window is a different size than when calibrated and the layout cannot be verified"
		if out.Probe.Checked {
			why = "the window is a different size than when calibrated and the screen there does not look the same"
		}
		out.Blocked = &Problem{Code: PUnverified, Message: "needs calibration for this window size: " + why,
			Fix: "Calibrate again at this size" + ifStr(!scr, " (or switch Screen Recording on so the layout can be verified)", "")}
		if !scr {
			out.Blocked.SettingsURL = URLScreenRecording
		}
		return out
	}
	if out.Probe.Checked && !out.Probe.OK {
		msg := "the screen around the " + s.targetLabel(appID, key) + " does not look like when it was calibrated: the layout probably changed"
		if mode == VerifyBlock {
			out.Blocked = &Problem{Code: PLayoutChanged, Message: "needs calibration for this layout: " + msg, Fix: "Calibrate again (if nothing changed, the check is wrong: set the layout check to warn)"}
			return out
		}
		out.Warn = msg
	}
	return out
}

// refuseImported: "Refuse old-style points" also covers the imported absolute points.
func refuseImported(r Result, blk Block) Result {
	if blk.BlockLegacy && r.Status == StOK && r.Confidence == ConfImported {
		return Result{Status: StNeeds, Reason: ReasonImportedOff, Entry: r.Entry,
			Message: "the imported old-style point is refused (Refuse old-style points is on); calibrate this target"}
	}
	return r
}

func ifStr(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

func (s *Service) targetLabel(appID, key string) string {
	l, _, _, _ := Describe(appID, key)
	return l
}

func (s *Service) probeAt(appID, key string, p Pt, want string) ProbeCheck {
	ck := fmt.Sprintf("%s|%s|%.0f|%.0f", appID, key, p.X, p.Y)
	s.mu.Lock()
	if e, ok := s.probes[ck]; ok && s.now().Sub(e.at) < ProbeTTL {
		s.mu.Unlock()
		return e.pc
	}
	s.mu.Unlock()
	pc := ProbeCheck{}
	if img, err := s.Sys.Grab(p, ProbeSize); err == nil && img != nil {
		if got := ProbeOf(img); got != "" {
			if d, ok := ProbeDistance(want, got); ok {
				pc = ProbeCheck{Checked: true, Dist: math.Round(d*1000) / 1000, OK: d <= ProbeThreshold}
			}
		}
	}
	s.mu.Lock()
	if s.probes == nil {
		s.probes = map[string]probeEntry{}
	}
	if len(s.probes) > 200 {
		s.probes = map[string]probeEntry{}
	}
	s.probes[ck] = probeEntry{s.now(), pc}
	s.mu.Unlock()
	return pc
}

// Blocked is a refused engine action, for the editor to show.
type Blocked struct {
	At      int64  `json:"at"`
	App     string `json:"app"`
	Target  string `json:"target"`
	Label   string `json:"label"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EngineResolver is what the engine's mouse actions call instead of reading an
// absolute point: it returns the point for the front window now, or ok=false
// and records why (visible in the editor), never clicking blindly.
func (s *Service) EngineResolver(appID string) func(key string) (x, y float64, axis string, ok bool) {
	return func(key string) (float64, float64, string, bool) {
		l := s.Locate(appID, key, LocateOpts{Input: true})
		if !l.OK() {
			code, msg := l.Reason, l.Say()
			if l.Blocked != nil {
				code = l.Blocked.Code
			}
			if code == "" {
				code = ReasonNone
			}
			s.noteBlocked(appID, key, code, msg)
			return 0, 0, "", false
		}
		axis := ""
		if l.Entry != nil {
			axis = l.Entry.Axis
		}
		if axis == "" {
			_, _, axis, _ = Describe(appID, key)
		}
		return l.Point.X, l.Point.Y, axis, true
	}
}

func (s *Service) noteBlocked(appID, key, code, msg string) {
	now := s.now()
	b := Blocked{At: now.UnixMilli(), App: appID, Target: key, Label: s.targetLabel(appID, key), Code: code, Message: msg}
	s.mu.Lock()
	if s.logged == nil {
		s.logged = map[string]time.Time{}
	}
	k := appID + "|" + key + "|" + code
	quiet := now.Sub(s.logged[k]) < 2*time.Second
	if !quiet {
		s.logged[k] = now
		s.blocks = append(s.blocks, b)
		if len(s.blocks) > 20 {
			s.blocks = s.blocks[len(s.blocks)-20:]
		}
	}
	s.mu.Unlock()
	if !quiet {
		s.logf("[mousecal] %s: %s (%s) refused: %s", appID, key, code, msg)
	}
}

// RecentBlocked returns the refused engine actions of an app, newest first.
func (s *Service) RecentBlocked(appID string) []Blocked {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Blocked{}
	for i := len(s.blocks) - 1; i >= 0; i-- {
		if s.blocks[i].App == appID {
			out = append(out, s.blocks[i])
		}
	}
	return out
}

// ---- overview -------------------------------------------------------------

// Target statuses.
const (
	TCalibrated = "calibrated"
	TNeeds      = "needs_calibration" // never calibrated
	TStale      = "stale"             // calibrated, but not for this context
	TLegacy     = "legacy"            // only an old absolute point
	TImported   = "imported"          // imported from ld-resolve: used, never re-verified
	TUnknown    = "unknown"           // the app is not in a state to judge
)

// EntryView is one stored calibration for the editor.
type EntryView struct {
	ID      string  `json:"id"`
	At      string  `json:"at"`
	Via     string  `json:"via"`
	Display string  `json:"display"`
	Layout  string  `json:"layout"`
	WinW    float64 `json:"winW"`
	WinH    float64 `json:"winH"`
	Source  string  `json:"source,omitempty"`
	Current bool    `json:"current"`
	HasPic  bool    `json:"hasProbe"`
}

// TargetView is one row of the calibration list.
type TargetView struct {
	Target
	Status     string      `json:"status"`
	StatusText string      `json:"statusText"`
	Confidence string      `json:"confidence,omitempty"`
	Entries    []EntryView `json:"entries"`
}

// Summary counts the rows by status.
type Summary struct {
	Total      int `json:"total"`
	Calibrated int `json:"calibrated"`
	Stale      int `json:"stale"`
	Needs      int `json:"needs"`
	Legacy     int `json:"legacy"`
	Imported   int `json:"imported"`
	Unknown    int `json:"unknown"`
}

// ContextView is the current context for the editor.
type ContextView struct {
	Display     string `json:"display"`
	DisplayName string `json:"displayName,omitempty"`
	Window      Rect   `json:"window"`
	Layout      string `json:"layout"`
	Label       string `json:"label"`
}

// AppView is the app block of the overview.
type AppView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Running     bool   `json:"running"`
	Frontmost   bool   `json:"frontmost"`
	WindowCount int    `json:"windowCount"`
}

// Overview is GET /api/calibration/{app}.
type Overview struct {
	App         AppView        `json:"app"`
	Permissions PermView       `json:"permissions"`
	Armed       bool           `json:"armed"`
	Context     *ContextView   `json:"context"`
	Problem     *Problem       `json:"problem"`
	Summary     Summary        `json:"summary"`
	Targets     []TargetView   `json:"targets"`
	Settings    SettingsView   `json:"settings"`
	Blocked     []Blocked      `json:"blocked"`
	Capture     *PendingView   `json:"capture"`
	Notes       []string       `json:"notes"`
	Contexts    []ContextCount `json:"contexts"`
}

// PermView is the permission block.
type PermView struct {
	Accessibility      bool   `json:"accessibility"`
	ScreenRecording    bool   `json:"screenRecording"`
	AccessibilityURL   string `json:"accessibilityUrl"`
	ScreenRecordingURL string `json:"screenRecordingUrl"`
}

// SettingsView is the calibration settings of an app.
type SettingsView struct {
	Verify      string `json:"verify"`
	BlockLegacy bool   `json:"blockLegacy"`
}

// ContextCount says how many targets are calibrated for a context: the
// "already calibrated layouts" list.
type ContextCount struct {
	Display string `json:"display"`
	Layout  string `json:"layout"`
	Targets int    `json:"targets"`
	Current bool   `json:"current"`
}

// Overview builds the whole picture for one app.
func (s *Service) Overview(appID string) (Overview, error) {
	p := (*preset.Preset)(nil)
	if s.Preset != nil {
		p = s.Preset(appID)
	}
	if p == nil {
		return Overview{}, fmt.Errorf("no preset for %q", appID)
	}
	s.dropEnv(appID)
	env, ctx, prob := s.context(appID)
	acc, scr := s.Sys.Perms()
	name := s.name(appID)
	ov := Overview{
		App:         AppView{ID: appID, Name: name, Running: env.Running, Frontmost: env.Frontmost, WindowCount: len(env.Windows)},
		Permissions: PermView{Accessibility: acc, ScreenRecording: scr, AccessibilityURL: URLAccessibility, ScreenRecordingURL: URLScreenRecording},
		Armed:       s.Sys.Armed(),
		Problem:     prob,
		Blocked:     s.RecentBlocked(appID),
		Notes:       []string{},
		Targets:     []TargetView{},
	}
	blk, err := s.Store.Get(appID)
	if err != nil {
		return ov, err
	}
	ov.Settings = SettingsView{Verify: blk.VerifyMode(), BlockLegacy: blk.BlockLegacy}
	if prob == nil {
		ov.Context = &ContextView{Display: ctx.Display, DisplayName: ctx.DisplayName, Window: ctx.Win, Layout: ctx.Layout, Label: ctx.Label()}
	}
	if prob != nil && prob.Code == PAmbiguous || prob == nil && len(env.Windows) > 1 {
		ov.Notes = append(ov.Notes, "More than one window of "+name+" is open; the biggest one is used.")
	}
	if !acc {
		ov.Notes = append(ov.Notes, "Accessibility is off: calibrations can be captured and tested on screen, but no click or drag will be sent until it is switched on.")
	}
	if !scr {
		ov.Notes = append(ov.Notes, "Screen Recording is off: the picture check that notices a changed layout, and the preview picture, are not available. Window size and screen are still checked.")
	}
	counts := map[string]*ContextCount{}
	for _, t := range DeriveTargets(p) {
		tv := TargetView{Target: t, Entries: []EntryView{}}
		entries := blk.Entries(t.Key)
		for _, e := range entries {
			cur := prob == nil && e.Display == ctx.Display && layoutMatches(e.Layout, ctx.Layout)
			tv.Entries = append(tv.Entries, EntryView{ID: e.ID, At: e.At, Via: e.Via, Display: e.Display, Layout: e.Layout, WinW: e.WinW, WinH: e.WinH, Source: e.Source, Current: cur, HasPic: e.Probe != ""})
			ck := e.Display + "|" + e.Layout
			if counts[ck] == nil {
				counts[ck] = &ContextCount{Display: e.Display, Layout: e.Layout, Current: cur}
			}
			counts[ck].Targets++
		}
		switch {
		case prob != nil:
			tv.Status, tv.StatusText = TUnknown, prob.Message
			if len(entries) == 0 {
				if _, _, ok := s.legacy(t.Key); ok && !blk.BlockLegacy {
					tv.Status, tv.StatusText = TLegacy, "old absolute point; "+prob.Message
				}
			}
			ov.Summary.Unknown++
		default:
			res := refuseImported(Resolve(entries, ctx, PriorFor(appID)), blk)
			switch {
			case res.Status == StOK && res.Confidence == ConfImported:
				tv.Status, tv.Confidence = TImported, res.Confidence
				tv.StatusText = confText(res)
				ov.Summary.Imported++
			case res.Status == StOK:
				tv.Status, tv.Confidence = TCalibrated, res.Confidence
				tv.StatusText = confText(res)
				ov.Summary.Calibrated++
			case res.Reason == ReasonNone:
				if _, _, ok := s.legacy(t.Key); ok && !blk.BlockLegacy {
					tv.Status, tv.Confidence = TLegacy, ConfLegacy
					tv.StatusText = "old absolute point: it does not follow the window; calibrate again"
					ov.Summary.Legacy++
				} else {
					tv.Status, tv.StatusText = TNeeds, "not calibrated yet"
					ov.Summary.Needs++
				}
			default:
				tv.Status, tv.StatusText = TStale, res.Message
				ov.Summary.Stale++
			}
		}
		ov.Summary.Total++
		ov.Targets = append(ov.Targets, tv)
	}
	ov.Contexts = []ContextCount{}
	for _, c := range counts {
		ov.Contexts = append(ov.Contexts, *c)
	}
	sort.Slice(ov.Contexts, func(i, j int) bool {
		a, b := ov.Contexts[i], ov.Contexts[j]
		if a.Current != b.Current {
			return a.Current
		}
		return a.Layout+a.Display < b.Layout+b.Display
	})
	ov.Capture = s.activeView(appID)
	return ov, nil
}

func (s *Service) legacy(key string) (Pt, string, bool) {
	if s.Legacy == nil {
		return Pt{}, "", false
	}
	return s.Legacy(key)
}

func confText(r Result) string {
	switch r.Confidence {
	case ConfExact:
		return "calibrated for this window"
	case ConfLearned:
		return "calibrated; follows this window size (learned from two sizes)"
	case ConfImported:
		src := "ld-resolve"
		if r.Entry != nil && r.Entry.Source != "" {
			src = r.Entry.Source
		}
		return "imported from " + src + " (fixed screen position, not re-verified); calibrate to make it follow the window"
	case ConfPrior:
		return "calibrated at another window size; will be checked against the screen before it clicks"
	}
	return "calibrated"
}
