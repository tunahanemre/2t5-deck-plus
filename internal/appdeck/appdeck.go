// Package appdeck is the executor for deck input while a NON-Resolve app is
// frontmost (or when Resolve has no binding and Global supplies one).
//
// It asks a preset.Snapshot which assignment applies (app rung, then Global,
// with per-value inheritance, see preset.Snapshot.ResolveForMod) and runs only
// the generic and browser verbs: key, keyDelta, scrollDelta, shell, layer,
// modifierHold, toggleHud and none. Every other verb (all Resolve verbs,
// finderTag) is reported as not handled so the caller decides; the engine
// never lets those reach Resolve from a foreign app.
//
// Plan: plans/2026-11-03-app-manager-ld-deck-global-editor.md (spec 4.2).
package appdeck

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"2t5-deck-plus/internal/accel"
	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/keys"
	"2t5-deck-plus/internal/mouse"
	"2t5-deck-plus/internal/preset"
)

// Input kinds.
const (
	KindPress   = "press"
	KindRelease = "release"
	KindTurn    = "turn"
)

// Defaults mirrored from the Resolve dispatchers so a bare assignment feels
// the same: internal/engine defaultSelectClipDetentThreshold / defaultLinesPerDetent.
const (
	defaultDetentThreshold = 1
	defaultLinesPerDetent  = 1
	shellTimeout           = 2 * time.Second
)

// Input is one deck event. Step is the raw ticks accumulated since the last
// call for a turn (signed); ignored for press/release.
type Input struct {
	Bundle string // frontmost bundle id, or the Resolve id for the Global fall-through
	Mod    string // held modifier overlay ("", "shift", "cmd", ...)
	Layer  int    // deck layer, 1 = base
	// PMode is the live slider mode ("hue", "sat", "lum"; "" = unknown). Only the
	// P1-P8 / P1btn-P8btn controls read it: it selects their per-mode bank.
	PMode   string
	Control string
	Kind    string
	Step    int
	// Now stamps a turn for the velocity feel (rate window, momentum). Zero =
	// the executor's clock. Only read when the assignment has a velocityProfile
	// other than linear, or a momentumMs.
	Now time.Time
}

// Effect is state the engine itself must apply (appdeck has no access to it).
type Effect struct {
	Kind     string // "layer" | "modifierHold" | "modifierRelease" | "toggleHud"
	Layer    int
	Modifier string
}

// Result is what happened. Handled=false means appdeck did nothing and owns
// no part of the event (unassigned, a verb it does not run, or a turn that has
// not crossed a detent yet is still Handled=true with Fired=false).
type Result struct {
	Handled  bool
	Fired    bool // an action was actually executed this call
	Text     string
	Value    float64
	Unit     string
	Effect   *Effect
	PresetID string
	Source   string // "app" | "global"
	Bank     string
	Action   string
	// Accel is the velocity multiplier that was applied to this turn (the
	// profile's multiplier at the measured rate). 0 = none applied (linear or
	// not a turn); consumers treat 0 as 1.
	Accel float64
	// LatencyMs is how long this Handle call took from entry to the post being
	// issued (resolve the assignment, accumulate the turn, call the key / scroll /
	// command seam), in ms with microsecond resolution. It does NOT include the
	// MIDI read, the frontmost lookup before the call, the observer queue, or the
	// target app's own delay. 0 = nothing was posted.
	LatencyMs float64
}

// Executor runs generic verbs. Zero seams use the real keys, mouse and exec;
// tests replace them. Safe for concurrent use.
type Executor struct {
	snap atomic.Pointer[preset.Snapshot]
	idx  atomic.Pointer[map[string]struct{}]
	// panic: controls assigned to the disarm action anywhere (IsPanicControl)
	panic atomic.Pointer[map[string]struct{}]

	// Seams.
	SendKey    func(spec string) error
	Scroll     func(delta int, flags uint64)
	ParseFlags func(spec string) (uint64, error)
	Shell      func(cmd string, args []string)

	// Velocity feel seams (all optional).
	// Now is the clock (nil = time.Now). After schedules f once after d and
	// returns a cancel (nil = time.AfterFunc). TailGuard says whether the
	// momentum tail of a turn that began under bundle may still act (e.g. the
	// same app is still frontmost); nil = no momentum tail at all, which keeps
	// the feature inert until the app wires it. OnTail receives the result of
	// every tail step so the live view can show it.
	Now       func() time.Time
	After     func(d time.Duration, f func()) (cancel func())
	TailGuard func(bundle string) bool
	OnTail    func(in Input, res Result)

	// disarmed is the safety switch (wave D): while set, Handle still resolves
	// and reports what WOULD fire (text prefixed "disarmed: ") but posts no
	// key, no scroll and runs no command. Zero value = armed; never persisted.
	disarmed atomic.Bool

	// OnArmChange is told when the arm switch really changes (armed, source):
	// source is "editor" (the API), "deck-key <control>" (the panic action) or
	// whatever the caller of SetArmedFrom passed. nil = not reported.
	OnArmChange func(armed bool, source string)

	// OnShell is told when a command started by the default runner has finished:
	// the program as given, its exit status (0 ok, -1 could not run or timed
	// out) and a short note ("", "timeout", "start failed"). Never the arguments.
	// Not called for a test's Shell seam. nil = not reported.
	OnShell func(cmd string, exit int, note string)

	// OnArmRequest is told of EVERY SetArmedFrom call, whether or not it changed
	// the switch (an explicit disarm of an already disarmed deck still refreshes
	// the .disarmed marker). Runs under the arm lock: keep it quick. nil = none.
	OnArmRequest func(armed bool, source string)

	// HoldGuard says whether a hold-to-fire press that began under bundle may
	// still fire: the same app must still be frontmost (the engine wires the
	// same check the momentum tail uses). nil = no check (tests).
	HoldGuard func(bundle string) bool

	armMu    sync.Mutex    // serialises SetArmed (flag + key + mouse pause)
	armEpoch atomic.Uint64 // bumped by every SetArmed: a hold that began before it must not fire after it

	// OnHold receives the result of a hold-to-fire action when it fires at its
	// threshold (off the Handle call). nil = not reported.
	OnHold func(in Input, res Result)

	hmu   sync.Mutex // guards holds / hgen (hold.go)
	holds map[string]*holdPend
	hgen  uint64

	mu  sync.Mutex
	acc map[string]*detent
	fs  map[string]*feelState
}

// New returns an Executor wired to the real keyboard, mouse and exec.
func New() *Executor {
	return &Executor{SendKey: keys.Send, Scroll: mouse.Scroll, ParseFlags: keys.ParseModifierFlags, Shell: runShell, acc: map[string]*detent{}, fs: map[string]*feelState{}}
}

// NewDisarmed is New with the safety switch already off: nothing is posted,
// not even by a seam a test forgot to replace. Tests that only look at what
// WOULD fire should start from this.
func NewDisarmed() *Executor {
	x := New()
	x.SetArmed(false)
	return x
}

// Armed reports whether the executor posts input (the default). The engine
// reads this too: while it is false, the sidecar's mutating ops and the Finder
// tag writes are held back as well (see ArmCoverage).
func (x *Executor) Armed() bool { return !x.disarmed.Load() }

// ArmCoverage names everything the switch holds back while disarmed, for GET
// /api/live: key presses (keys), mouse clicks and drags (mouse), scroll
// events (scroll), commands (shell), Resolve sidecar ops that change Resolve
// (sidecar-writes; the engine allows only the read-only and undo ops) and
// Finder tag writes (finder-tags).
func (x *Executor) ArmCoverage() []string {
	return []string{"keys", "mouse", "scroll", "shell", "sidecar-writes", "finder-tags"}
}

// SetArmed arms or disarms the executor. Disarmed, nothing reaches the
// keyboard, the mouse, the scroll wheel or exec: the key and mouse packages
// are paused too (so Resolve's own key, click, drag and scroll verbs go
// quiet), and the engine holds back the sidecar's mutating ops and the Finder
// tag writes. Events and results are unchanged except that the result text is
// prefixed "disarmed: ". State layer, modifier and HUD effects still apply:
// they post no input. The pause is process-wide; there is one switch per app.
func (x *Executor) SetArmed(armed bool) {
	x.armMu.Lock()
	x.setArmedLocked(armed)
	x.armMu.Unlock()
	// whatever was being held for a delayed fire belongs to the previous state
	// (arm -> disarm -> arm quickly must not let an old hold fire)
	x.CancelHolds()
}

// setArmedLocked changes the three pieces of state together. Caller holds armMu.
func (x *Executor) setArmedLocked(armed bool) {
	x.disarmed.Store(!armed)
	keys.SetPaused(!armed)
	mouse.SetPaused(!armed)
	x.armEpoch.Add(1)
}

// ArmEpoch counts the arm switch changes made so far (every SetArmed call). The
// engine's Resolve hold-to-fire compares it at press and at fire.
func (x *Executor) ArmEpoch() uint64 { return x.armEpoch.Load() }

// SetArmedFrom is SetArmed that also reports a real change, with its source,
// to OnArmChange.
func (x *Executor) SetArmedFrom(armed bool, source string) {
	x.armMu.Lock()
	// compare-and-set: whether it really changed is decided under the same lock
	// that changes it, and OnArmChange runs under it too, so two racing callers
	// report in the order the state actually changed (the disarmed marker must
	// never end up saying the opposite of the switch). The change itself is
	// already in effect when the callback runs; the callback must be quick.
	changed := x.Armed() != armed
	x.setArmedLocked(armed)
	if x.OnArmRequest != nil {
		x.OnArmRequest(armed, source)
	}
	if changed && x.OnArmChange != nil {
		x.OnArmChange(armed, source)
	}
	x.armMu.Unlock()
	x.CancelHolds()
}

// IsDisarmPress reports, without running anything, whether a press of control
// resolves to the panic "disarm" action for this bundle (the engine uses it
// while the editor window is frontmost, where every other press is swallowed).
func (x *Executor) IsDisarmPress(bundle, mod string, layer int, control string) bool {
	snap := x.snap.Load()
	if snap == nil || control == "" {
		return false
	}
	if layer < 1 {
		layer = 1
	}
	eb, ok := snap.ResolveForMod(bundle, mod, layer, control)
	if !ok {
		return false
	}
	typ := eb.Binding.Type
	if typ == "" {
		typ = eb.Binding.Method
	}
	return typ == "disarm"
}

// PressType reports, without running anything, the action type a press of
// control resolves to for this bundle (and, for a modifierHold, which modifier).
// The engine uses it while the editor window is frontmost, where every press is
// swallowed except the ones that post nothing into the editor: disarm, toggleHud
// and the modifier holds that pair with them (Fn + C6).
func (x *Executor) PressType(bundle, mod string, layer int, control string) (typ, modifier string) {
	snap := x.snap.Load()
	if snap == nil || control == "" {
		return "", ""
	}
	if layer < 1 {
		layer = 1
	}
	eb, ok := snap.ResolveForMod(bundle, mod, layer, control)
	if !ok {
		return "", ""
	}
	typ = eb.Binding.Type
	if typ == "" {
		typ = eb.Binding.Method
	}
	return typ, eb.Binding.Modifier
}

// DisarmedPrefix starts the result text of an action that did not fire for real.
const DisarmedPrefix = "disarmed: "

// said prefixes text when disarmed.
func (x *Executor) said(text string) string {
	if x.disarmed.Load() {
		return DisarmedPrefix + text
	}
	return text
}

// SetSnapshot swaps the preset snapshot (call from Store.OnChange). Never
// blocks.
func (x *Executor) SetSnapshot(s *preset.Snapshot) {
	x.snap.Store(s)
	x.stopTails()
	x.CancelHolds()
	idx := map[string]struct{}{}
	if s != nil {
		for _, p := range s.Presets() {
			if p.ID == preset.ResolveID {
				continue
			}
			for _, bank := range p.Banks {
				for c := range bank {
					idx[c] = struct{}{}
				}
			}
		}
	}
	x.idx.Store(&idx)
	x.panic.Store(panicControls(s))
}

// panicControls lists every control that ANY preset (an app, Global, Resolve)
// assigns to the panic "disarm" action, in any bank. Such a control disarms the
// deck wherever it is pressed: the engine checks this set before the app and
// Resolve cascades (and before any sidecar round trip), so nothing can shadow it.
func panicControls(s *preset.Snapshot) *map[string]struct{} {
	m := map[string]struct{}{}
	if s != nil {
		for _, p := range s.Presets() {
			for _, bank := range p.Banks {
				for c, a := range bank {
					if actions.EffectiveType(a) == "disarm" {
						m[c] = struct{}{}
					}
				}
			}
		}
	}
	return &m
}

// IsPanicControl reports whether control is assigned to the panic disarm action
// anywhere. A cheap map lookup, safe on the MIDI callback.
func (x *Executor) IsPanicControl(control string) bool {
	m := x.panic.Load()
	if m == nil {
		return false
	}
	_, ok := (*m)[control]
	return ok
}

// Assigns reports whether any non-Resolve preset (Global included) assigns
// control in any bank. A cheap map lookup, safe on the MIDI callback.
func (x *Executor) Assigns(control string) bool {
	m := x.idx.Load()
	if m == nil {
		return false
	}
	_, ok := (*m)[control]
	return ok
}

// detent is the keyDeltaDetent logic of internal/engine (2t5-deck-plus
// engine.go keyDeltaDetent.add, copied: it is unexported there and appdeck
// must not import engine). Positive result = clockwise detents.
type detent struct{ acc int }

func (d *detent) add(step, threshold int) int {
	if threshold <= 0 {
		threshold = defaultDetentThreshold
	}
	d.acc += step
	n := 0
	for d.acc >= threshold {
		d.acc -= threshold
		n++
	}
	for d.acc <= -threshold {
		d.acc += threshold
		n--
	}
	return n
}

func (x *Executor) accumulator(key string) *detent {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.acc == nil {
		x.acc = map[string]*detent{}
	}
	d := x.acc[key]
	if d == nil {
		d = &detent{}
		x.acc[key] = d
	}
	return d
}

// ResetAccumulators drops all partial detents (e.g. on app switch).
func (x *Executor) ResetAccumulators() {
	x.CancelHolds() // a held key must not fire into the app we switched to
	x.mu.Lock()
	x.acc = map[string]*detent{}
	x.mu.Unlock()
	x.stopTails()
	x.mu.Lock()
	x.fs = map[string]*feelState{}
	x.mu.Unlock()
}

// Handle resolves and runs one event. When it posts something, Result.LatencyMs
// says how long that took (see Result.LatencyMs for exactly what is measured).
func (x *Executor) Handle(in Input) Result {
	start := x.now()
	res := x.handle(in)
	if res.Fired && in.Kind != KindRelease && !x.disarmed.Load() { // disarmed: nothing was posted
		if d := x.now().Sub(start); d > 0 {
			res.LatencyMs = math.Round(float64(d)/float64(time.Microsecond)) / 1000
		}
	}
	return res
}

func (x *Executor) handle(in Input) Result {
	snap := x.snap.Load()
	if snap == nil || in.Control == "" {
		return Result{}
	}
	if in.Layer < 1 {
		in.Layer = 1
	}
	if in.Kind == KindRelease {
		// released before the hold threshold: nothing fires, and the tap is reported
		if ms, base, was := x.cancelHold(in.Control); was {
			base.Handled = true
			base.Text = fmt.Sprintf("hold to fire (%d ms)", ms)
			return base
		}
	}
	eb, ok := snap.ResolveForModeMod(in.Bundle, in.Mod, in.PMode, in.Layer, in.Control)
	if !ok {
		return Result{}
	}
	b := eb.Binding
	typ := b.Type
	if typ == "" {
		typ = b.Method
	}
	res := Result{PresetID: eb.PresetID, Source: eb.Source, Bank: eb.Bank, Action: eb.Action}
	switch in.Kind {
	case KindPress:
		switch typ {
		case "key":
			res.Handled = true
			if b.Key == "" {
				return res
			}
			if ms := holdOf(eb.Raw); ms > 0 {
				return x.armHold(in, ms, typ, b.Key, "", nil, res)
			}
			if err := x.sendKey(b.Key); err != nil {
				log.Printf("[appdeck] control=%q key=%q error: %v", in.Control, b.Key, err)
				return res
			}
			res.Fired, res.Text = true, x.said(b.Key)
		case "shell":
			res.Handled = true
			if b.Cmd == "" {
				return res
			}
			if ms := holdOf(eb.Raw); ms > 0 {
				return x.armHold(in, ms, typ, "", b.Cmd, b.Args, res)
			}
			x.shell(b.Cmd, b.Args)
			res.Fired, res.Text = true, x.said("run "+b.Cmd)
		case "layer":
			res.Handled, res.Fired = true, true
			res.Effect = &Effect{Kind: "layer", Layer: b.Layer}
			res.Text = fmt.Sprintf("Layer %d", b.Layer)
		case "modifierHold":
			res.Handled, res.Fired = true, true
			res.Effect = &Effect{Kind: "modifierHold", Modifier: b.Modifier}
			res.Text = in.Control + " held"
		case "toggleHud":
			res.Handled, res.Fired = true, true
			res.Effect = &Effect{Kind: "toggleHud"}
			res.Text = "Toggle HUD"
		case "disarm":
			// Panic: the full disarm POST /api/arm {armed:false} does (appdeck,
			// key and mouse packages, and through Armed() the engine's sidecar
			// and Finder gates). Runs from any app; never delayed.
			res.Handled, res.Fired = true, true
			res.Effect = &Effect{Kind: "disarm"}
			if x.Armed() {
				res.Text = "deck disarmed"
			} else {
				res.Text = "deck already disarmed"
			}
			x.SetArmedFrom(false, "deck-key "+in.Control)
		case "none":
			res.Handled = true
		}
	case KindRelease:
		if typ == "modifierHold" {
			res.Handled, res.Fired = true, true
			res.Effect = &Effect{Kind: "modifierRelease", Modifier: b.Modifier}
			res.Text = in.Control + " released"
		}
	case KindTurn:
		x.turn(in, b, typ, eb.Raw, &res)
	}
	return res
}

func (x *Executor) turn(in Input, b config.Binding, typ string, raw map[string]any, res *Result) {
	switch typ {
	case "keyDelta", "scrollDelta":
	case "none":
		res.Handled = true
		return
	default:
		return
	}
	res.Handled = true
	// One accumulator per bundle/bank/control: a switch of app or layer never
	// inherits a half-turned detent.
	key := in.Bundle + "|" + res.Bank + "|" + in.Control
	d := x.accumulator(key)
	var count int
	if profile, momentumMs := feelOf(raw); accel.IsLinear(profile) && momentumMs == 0 {
		// Today's path, untouched: no rate tracking, no carry.
		count = d.add(in.Step, b.DetentThreshold)
	} else {
		count = x.feelTurn(key, d, in, b, typ, profile, momentumMs, res)
	}
	if count == 0 {
		return
	}
	x.fire(in.Control, b, typ, count, res)
}

// fire runs count detents of a keyDelta / scrollDelta binding and fills res.
func (x *Executor) fire(control string, b config.Binding, typ string, count int, res *Result) {
	switch typ {
	case "keyDelta":
		key, n := b.KeyCW, count
		if count < 0 {
			key, n = b.KeyCCW, -count
		}
		if key == "" {
			log.Printf("[appdeck] control=%q keyDelta SKIP empty key spec count=%d", control, count)
			return
		}
		for i := 0; i < n; i++ {
			if err := x.sendKey(key); err != nil {
				log.Printf("[appdeck] control=%q keyDelta key=%q error: %v", control, key, err)
				return
			}
		}
		res.Fired = true
		res.Text = x.said(fmt.Sprintf("%s x%d", key, n))
		res.Value, res.Unit = float64(count), "keys"
	case "scrollDelta":
		lines := b.LinesPerDetent
		if lines == 0 {
			lines = defaultLinesPerDetent
		}
		var flags uint64
		if b.Flags != "" {
			f, err := x.parseFlags(b.Flags)
			if err != nil {
				log.Printf("[appdeck] control=%q scrollDelta flags=%q error: %v", control, b.Flags, err)
				return
			}
			flags = f
		}
		delta := count * lines
		x.scroll(delta, flags)
		res.Fired = true
		res.Text = x.said(fmt.Sprintf("scroll %+d", delta))
		res.Value, res.Unit = float64(delta), "lines"
	}
}

func (x *Executor) sendKey(spec string) error {
	if x.disarmed.Load() {
		return nil
	}
	if x.SendKey != nil {
		return x.SendKey(spec)
	}
	return keys.Send(spec)
}

func (x *Executor) scroll(delta int, flags uint64) {
	if x.disarmed.Load() {
		return
	}
	if x.Scroll != nil {
		x.Scroll(delta, flags)
		return
	}
	mouse.Scroll(delta, flags)
}

func (x *Executor) parseFlags(spec string) (uint64, error) {
	if x.ParseFlags != nil {
		return x.ParseFlags(spec)
	}
	return keys.ParseModifierFlags(spec)
}

func (x *Executor) shell(cmd string, args []string) {
	if x.disarmed.Load() {
		return
	}
	if x.Shell != nil {
		x.Shell(cmd, args)
		return
	}
	runShellWith(cmd, args, x.OnShell)
}

// execFn is the exec seam for runShell.
var execFn = func(ctx context.Context, cmd string, args []string) error {
	return exec.CommandContext(ctx, cmd, args...).Run()
}

// runShell starts cmd with args in its own goroutine and a 2 s timeout. No
// shell is involved, so nothing is expanded. Never blocks the caller.
func runShell(cmd string, args []string) { runShellWith(cmd, args, nil) }

// runShellWith is runShell that also reports the outcome to done (when not nil).
func runShellWith(cmd string, args []string, done func(cmd string, exit int, note string)) {
	args = append([]string(nil), args...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), shellTimeout)
		defer cancel()
		err := execFn(ctx, cmd, args)
		if err != nil {
			log.Printf("[appdeck] shell %q %v: %v", cmd, args, err)
		} else {
			log.Printf("[appdeck] shell %q %v ok", cmd, args)
		}
		if done != nil {
			exit, note := shellOutcome(ctx, err)
			done(cmd, exit, note)
		}
	}()
}

// shellOutcome maps a command's error to (exit status, note).
func shellOutcome(ctx context.Context, err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	if ctx.Err() == context.DeadlineExceeded {
		return -1, "timeout"
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), ""
	}
	return -1, "start failed"
}
