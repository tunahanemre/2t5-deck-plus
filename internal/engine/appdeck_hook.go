package engine

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"2t5-deck-plus/internal/appdeck"
	"2t5-deck-plus/internal/calib"
	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/layer"
	"2t5-deck-plus/internal/macapp"
	"2t5-deck-plus/internal/preset"
)

// InputOut is the human-readable result of an input event.
type InputOut struct {
	Text  string
	Value float64
	Unit  string
}

// InputEvent is one handled deck input as the live test view shows it. Its
// fields mirror editorapi.Event (minus Seq/Type, which the bus stamps); the app
// package converts, so this package does not import editorapi. T is Unix ms.
type InputEvent struct {
	T           int64
	Control     string
	Kind        string // press | release | turn
	RawTicks    int
	TicksPerSec float64
	Accel       float64
	Out         InputOut
	Fine        bool
	PresetID    string
	Source      string // app | global
	Bank        string
	Action      string
	// LatencyMs: appdeck's own time from handing the event to the executor to the
	// post being issued (see appdeck.Result.LatencyMs). 0 = not measured.
	LatencyMs float64
}

const (
	inputObserverQueue = 256
	// pressFrontmostMaxAge / turnFrontmostMaxAge bound how stale a cached
	// frontmost lookup may be. The lookup is a ~1-3 ms CGWindowList walk.
	pressFrontmostMaxAge = 30 * time.Millisecond
	turnFrontmostMaxAge  = 40 * time.Millisecond
	rateWindow           = 250 * time.Millisecond
)

// appHooks holds the state of the app-aware layer: appdeck, the input
// observer and the cached frontmost. Zero value = everything off, which keeps
// the engine byte-identical to the Resolve-only behaviour.
type appHooks struct {
	appdeck atomic.Pointer[appdeck.Executor]

	// capture, when set, sees every deck button press first and may consume it
	// (the mouse-calibration "capture on a deck key"); see SetCaptureHook.
	capture atomic.Pointer[func() bool]

	observer atomic.Pointer[func(InputEvent)]
	// obsMu guards obsCh: emitInput sends under the read lock, Stop closes
	// under the write lock, so a send can never hit a closed channel.
	obsMu sync.RWMutex
	obsCh chan InputEvent

	fmMu sync.Mutex
	fmID string
	fmAt time.Time

	// Layer per app (F9): the deck layer is one shared value, so a layer set
	// in one app must not follow the user into the next. On a change of the
	// frontmost app (seen in frontmostFor) the layer of the app being left is
	// remembered and the one of the app entered (default 1) is restored.
	// Resolve counts as one app. Guarded by fmMu.
	layerCtl *layer.State
	layerKey string
	layerBy  map[string]int

	// appHeld: controls whose press appdeck handled -> bundle/mod/layer to
	// pair the release with.
	appHeld sync.Map

	rateMu sync.Mutex
	rates  map[string]*rateWin
}

type heldPress struct {
	bundle string
	mod    string
	layer  int
	// holdMod is the overlay this press raised ("" = it was not a
	// modifierHold). The release clears it no matter what the binding is by
	// then (hot reload) or whether appdeck is still installed.
	holdMod string
}

// selfBundleIDFn is 2T5 DECK+'s own bundle id ("" when not run from a .app).
// Package var so tests can fake it.
var selfBundleIDFn = macapp.SelfBundleID

// editorFocusedText is the live-test marker for a press or turn that arrived
// while the editor window itself was frontmost.
const editorFocusedText = "handled by: editor focused, not fired"

// isSelfFront reports whether the frontmost app is 2T5 DECK+ itself. Bundle ids
// match case-insensitively; an unknown own id matches nothing.
func isSelfFront(id string) bool {
	self := selfBundleIDFn()
	return self != "" && id != "" && strings.EqualFold(id, self)
}

// emitEditorFocused reports a press / release / turn that was swallowed
// because the editor is frontmost: nothing is posted (a key or scroll would
// land in the editor's own web view), but the live test still shows it.
func (e *Engine) emitEditorFocused(control, kind string, step int, fine bool, now time.Time) {
	if !e.observing() {
		return
	}
	ev := InputEvent{Control: control, Kind: kind, RawTicks: step, Accel: 1,
		Out: InputOut{Text: editorFocusedText}, Fine: fine}
	if kind == "turn" {
		ev.TicksPerSec = e.ticksPerSec(control, step, now)
	}
	e.emitInput(ev)
}

// setLayerState gives the hooks the console's layer so app changes can
// restore a per-app layer. Called by Start with its fresh layer.State.
func (h *appHooks) setLayerState(l *layer.State) {
	h.fmMu.Lock()
	h.layerCtl, h.layerKey, h.layerBy = l, "", map[string]int{}
	h.fmMu.Unlock()
}

// noteFrontLocked applies a fresh frontmost lookup to the per-app layers.
// Caller holds fmMu. Unknown ("") and 2T5 DECK+ itself are not "an app change":
// a transient failed lookup or a click on the editor must not shuffle layers.
func (h *appHooks) noteFrontLocked(id string) {
	if h.layerCtl == nil || id == "" || isSelfFront(id) {
		return
	}
	key := strings.ToLower(id)
	if macapp.BundleIDIsResolve(id) {
		key = strings.ToLower(preset.ResolveID)
	}
	if h.layerKey == "" {
		h.layerKey = key // first sample: adopt the current layer for this app
		return
	}
	if key == h.layerKey {
		return
	}
	h.layerBy[h.layerKey] = h.layerCtl.Get()
	l := h.layerBy[key]
	if l < layer.Min {
		l = layer.Min
	}
	h.layerCtl.Set(l)
	h.layerKey = key
}

// SetCaptureHook installs (or, with nil, removes) the calibration capture hook.
// fn is called for every button press before anything else acts on it (after
// the panic key and the debounce); when it returns true the press was used as
// the capture trigger and does nothing else. Safe from any goroutine.
func (e *Engine) SetCaptureHook(fn func() bool) {
	if fn == nil {
		e.hooks.capture.Store(nil)
		return
	}
	e.hooks.capture.Store(&fn)
}

// SetCalibResolver routes every mouse point the engine uses (wheel drags,
// clicks, value boxes) through r instead of the absolute coordinates of
// calibration.json. Call it before Start: the engine reads e.cal without a
// lock. The old file points stay reachable through LegacyCalibPoint.
func (e *Engine) SetCalibResolver(r calib.Resolver) {
	if e.cal == nil {
		e.cal = calib.Empty()
	}
	e.cal.SetResolver(r)
}

// LegacyCalibPoint returns the absolute point calibration.json holds for key.
func (e *Engine) LegacyCalibPoint(key string) (x, y float64, axis string, ok bool) {
	p, ok := e.cal.Legacy(key)
	return p.X, p.Y, p.Axis, ok
}

// LegacyCalibPoints returns every absolute point calibration.json holds
// (name -> x, y, axis); empty when there is no file.
func (e *Engine) LegacyCalibPoints() map[string]calib.Point {
	return e.cal.Points()
}

// SetAppDeck installs (or, with nil, removes) the generic non-Resolve
// executor. While nil the engine behaves exactly as before.
func (e *Engine) SetAppDeck(x *appdeck.Executor) { e.hooks.appdeck.Store(x) }

// SetInputObserver registers fn to receive every handled press, release and
// turn. fn runs on its own goroutine behind a bounded queue: events are
// dropped when it is slow, and emitting never blocks MIDI handling. nil
// removes the observer. Safe from any goroutine.
func (e *Engine) SetInputObserver(fn func(InputEvent)) {
	h := &e.hooks
	if fn == nil {
		h.observer.Store(nil)
		return
	}
	h.obsMu.Lock()
	defer h.obsMu.Unlock()
	h.openObserverLocked()
	h.observer.Store(&fn)
}

// openObserverLocked starts the delivery goroutine and its queue when there is
// none. Caller holds obsMu (write).
func (h *appHooks) openObserverLocked() {
	if h.obsCh != nil {
		return
	}
	ch := make(chan InputEvent, inputObserverQueue)
	h.obsCh = ch
	go func() {
		for ev := range ch { // ends when closeObserver closes ch
			if f := h.observer.Load(); f != nil {
				(*f)(ev)
			}
		}
	}()
}

// closeObserver ends the delivery goroutine (F14). Events emitted afterwards
// are dropped until the engine starts again with an observer installed.
func (h *appHooks) closeObserver() {
	h.obsMu.Lock()
	if h.obsCh != nil {
		close(h.obsCh)
		h.obsCh = nil
	}
	h.obsMu.Unlock()
}

// reopenObserver restarts delivery after a Stop when an observer is installed.
func (h *appHooks) reopenObserver() {
	if h.observer.Load() == nil {
		return
	}
	h.obsMu.Lock()
	h.openObserverLocked()
	h.obsMu.Unlock()
}

func (e *Engine) observing() bool { return e.hooks.observer.Load() != nil }

// emitInput queues ev for the observer; drops it when the queue is full.
func (e *Engine) emitInput(ev InputEvent) {
	h := &e.hooks
	if h.observer.Load() == nil {
		return
	}
	if ev.T == 0 {
		ev.T = time.Now().UnixMilli()
	}
	if ev.Accel == 0 {
		ev.Accel = 1
	}
	h.obsMu.RLock()
	defer h.obsMu.RUnlock()
	if h.obsCh == nil { // stopped
		return
	}
	select {
	case h.obsCh <- ev:
	default: // slow observer: drop
	}
}

// frontmostFor returns the frontmost bundle id, reusing a lookup younger than
// maxAge. Goes through frontmostBundleIDFn so tests can fake it.
func (e *Engine) frontmostFor(maxAge time.Duration) string {
	h := &e.hooks
	h.fmMu.Lock()
	defer h.fmMu.Unlock()
	if !h.fmAt.IsZero() && time.Since(h.fmAt) < maxAge {
		return h.fmID
	}
	h.fmID = frontmostBundleIDFn()
	h.fmAt = time.Now()
	h.noteFrontLocked(h.fmID)
	return h.fmID
}

// isResolveFront treats an unknown frontmost ("") as Resolve, so a failed
// lookup keeps today's behaviour.
func isResolveFront(id string) bool { return id == "" || macapp.BundleIDIsResolve(id) }

// rateWin smooths the raw tick rate of one control over rateWindow.
type rateWin struct {
	at    []time.Time
	ticks []int
}

// ticksPerSec records |ticks| now and returns the rate over the last 250 ms.
func (e *Engine) ticksPerSec(control string, ticks int, now time.Time) float64 {
	h := &e.hooks
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	if h.rates == nil {
		h.rates = map[string]*rateWin{}
	}
	w := h.rates[control]
	if w == nil {
		w = &rateWin{}
		h.rates[control] = w
	}
	if ticks < 0 {
		ticks = -ticks
	}
	w.at = append(w.at, now)
	w.ticks = append(w.ticks, ticks)
	cut := 0
	for cut < len(w.at) && now.Sub(w.at[cut]) > rateWindow {
		cut++
	}
	w.at, w.ticks = w.at[cut:], w.ticks[cut:]
	sum := 0
	for _, t := range w.ticks {
		sum += t
	}
	return float64(sum) / rateWindow.Seconds()
}

var numRe = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)

// textValue extracts the first number from a dispatch result string.
func textValue(s string) float64 {
	if m := numRe.FindString(s); m != "" {
		if v, err := strconv.ParseFloat(m, 64); err == nil {
			return v
		}
	}
	return 0
}

// noteObs records what a Resolve-path note handler reported, so one deferred
// emit covers every return path of the note_on / release branch.
type noteObs struct {
	e     *Engine
	text  string
	fired bool
}

// set is e.setLastAction plus the record.
func (o *noteObs) set(s string) {
	o.e.setLastAction(s)
	o.text, o.fired = s, true
}

// resolveObs describes the binding a Resolve-path event ran under, best
// effort (direct intercepts report the note-bank binding for the control).
func resolveObs(cfg *config.Config, page string, layer int, bank, mod, control string) (b config.Binding, rbank string) {
	if mod != "" {
		if ob, bn, ok := resolveOverlayBinding(cfg, page, mod, control); ok {
			return ob, bn
		}
	}
	rbank = resolveNoteBank(cfg, page, layer, bank, control)
	b, _ = cfg.Binding(rbank, control)
	return b, rbank
}

func (e *Engine) emitResolveNote(kind string, o *noteObs, cfg *config.Config, page string, layer int, bank, mod, control string, fine bool) {
	b, rbank := resolveObs(cfg, page, layer, bank, mod, control)
	if mod == "" {
		if mb, mbank, ok := cfg.ModeBinding(page, e.getPMode(), control); ok {
			b, rbank = mb, mbank
		}
	}
	typ := b.Type
	if typ == "" {
		typ = b.Method
	}
	e.emitInput(InputEvent{Control: control, Kind: kind, Accel: 1,
		Out:      InputOut{Text: o.text, Value: textValue(o.text)},
		Fine:     fine,
		PresetID: preset.ResolveID, Source: "app", Bank: rbank, Action: typ})
}

// emitResolveTurn reports a turn handled by the Resolve cascade.
func (e *Engine) emitResolveTurn(control string, step int, text string, b config.Binding, bank string, fine bool, now time.Time) {
	typ := b.Type
	if typ == "" {
		typ = b.Method
	}
	e.emitInput(InputEvent{Control: control, Kind: "turn", RawTicks: step,
		TicksPerSec: e.ticksPerSec(control, step, now), Accel: 1,
		Out:  InputOut{Text: text, Value: textValue(text)},
		Fine: fine, PresetID: preset.ResolveID, Source: "app", Bank: bank, Action: typ})
}

// emitAppdeck converts a Result to an event and queues it.
func (e *Engine) emitAppdeck(control, kind string, step int, res appdeck.Result, fine bool, now time.Time) {
	if !res.Handled || !e.observing() {
		return
	}
	ev := InputEvent{Control: control, Kind: kind, RawTicks: step, Accel: 1,
		Out:  InputOut{Text: res.Text, Value: res.Value, Unit: res.Unit},
		Fine: fine, PresetID: res.PresetID, Source: res.Source, Bank: res.Bank, Action: res.Action, LatencyMs: res.LatencyMs}
	if kind == "turn" {
		ev.TicksPerSec = e.ticksPerSec(control, step, now)
	}
	if res.Accel > 0 {
		// the real velocity multiplier appdeck applied (wave C); 0 = none, event keeps 1
		ev.Accel = res.Accel
	}
	e.emitInput(ev)
}

// ObserveAppdeckTail reports one momentum-tail step of an appdeck turn to the
// input observer (wave C). The tail has no raw ticks of its own, so RawTicks
// and TicksPerSec are 0. No-op without an observer.
func (e *Engine) ObserveAppdeckTail(control string, res appdeck.Result) {
	if !res.Handled || !e.observing() {
		return
	}
	acc := res.Accel
	if acc <= 0 {
		acc = 1
	}
	e.emitInput(InputEvent{Control: control, Kind: "turn", Accel: acc,
		Out:      InputOut{Text: res.Text, Value: res.Value, Unit: res.Unit},
		PresetID: res.PresetID, Source: res.Source, Bank: res.Bank, Action: res.Action})
}

// ObserveAppdeckHold reports a hold-to-fire press that fired at its threshold
// (wave H): it runs on the executor's timer, not on the MIDI callback.
func (e *Engine) ObserveAppdeckHold(control string, res appdeck.Result) {
	if !res.Handled {
		return
	}
	if res.Fired && res.Text != "" {
		e.setLastAction(res.Text)
	}
	if !e.observing() {
		return
	}
	e.emitInput(InputEvent{Control: control, Kind: "press", Accel: 1,
		Out:      InputOut{Text: res.Text},
		PresetID: res.PresetID, Source: res.Source, Bank: res.Bank, Action: res.Action})
}

// finishAppdeckPress applies an appdeck press result: engine-side effects,
// last-action text, held-press bookkeeping for the matching release, and the
// observer event. res.Handled == false does nothing at all (the control is
// simply unassigned for this app and Global).
func (e *Engine) finishAppdeckPress(res appdeck.Result, control, bundle, mod string, layer int, setLayer func(int), fine bool) {
	if !res.Handled {
		return
	}
	holdMod := ""
	if res.Effect != nil {
		switch res.Effect.Kind {
		case "layer":
			setLayer(res.Effect.Layer)
		case "modifierHold":
			e.setHeldOverlay(res.Effect.Modifier)
			holdMod = res.Effect.Modifier
		case "toggleHud":
			if e.toggleHUD != nil {
				e.toggleHUD()
			}
		}
	}
	if res.Fired && res.Text != "" {
		e.setLastAction(res.Text)
	}
	e.hooks.appHeld.Store(control, heldPress{bundle: bundle, mod: mod, layer: layer, holdMod: holdMod})
	e.emitAppdeck(control, "press", 0, res, fine, time.Now())
}

// emitResolveRelease reports a Resolve-path release (fine mode / modifier).
func (e *Engine) emitResolveRelease(control, text string, b config.Binding, fine bool) {
	if !e.observing() {
		return
	}
	e.emitInput(InputEvent{Control: control, Kind: "release", Accel: 1,
		Out: InputOut{Text: text}, Fine: fine,
		PresetID: preset.ResolveID, Source: "app", Action: b.Type})
}

// turnSet runs set(text) and records text for the turn observer.
func turnSet(dst *string, set func(string), text string) {
	set(text)
	*dst = text
}
