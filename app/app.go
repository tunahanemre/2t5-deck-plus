package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"2t5-deck-plus/internal/engine"
	"path/filepath"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/editorapi"
	"2t5-deck-plus/internal/finder"
	"2t5-deck-plus/internal/hud"
	"2t5-deck-plus/internal/keys"
	"2t5-deck-plus/internal/macapp"
	"2t5-deck-plus/internal/mouse"
	"2t5-deck-plus/internal/preset"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// statusEventInterval is how often OnStartup's background ticker emits a
// "status" event to the frontend via runtime.EventsEmit. Cheap enough
// (Engine.Status() is a mutex-guarded snapshot copy plus two read-only CGO
// permission probes) to run unconditionally rather than only while the
// engine is running — the frontend decides what to render for a stopped
// engine from the Running field.
const statusEventInterval = 250 * time.Millisecond

// hudWatchInterval is how often the HUD gate samples whether DaVinci Resolve
// is running. Deliberately slow: this drives a human-visible window, not an
// input path, and each sample is a Launch Services round trip. It runs on its
// own goroutine and is never reached from the MIDI/input path -- blocking that
// path on a slow dependency is what produced the duplicate/lagging deck input
// the sidecar work of 2026-08 had to undo.
const hudWatchInterval = 3 * time.Second

// hudFlipSamples is the hysteresis depth: a committed state only changes after
// this many CONSECUTIVE samples disagree with it, so one missed or transient
// reading cannot flap the panel on and off. At hudWatchInterval this means the
// HUD reacts to Resolve launching or quitting within ~6s, which is well inside
// the time Resolve itself takes to become usable.
const hudFlipSamples = 2

// engineAPI is the subset of *engine.Engine's methods App depends on. Using
// an interface — rather than a concrete *engine.Engine field — lets
// app_test.go inject a fake engine and exercise StartEngine/StopEngine/
// GetStatus deterministically. A real *engine.Engine.Start(ctx) internally
// probes real sidecar/MIDI hardware via its own unexported package-level
// seams (launchSidecarFn/startMIDIFn), which are only overridable from
// within the engine package's own test files — calling the real Start from
// app_test.go would reintroduce the exact "test hermeticity trap" already
// documented from Task 2 (a unit test silently spawning real subprocesses /
// probing real hardware). *engine.Engine satisfies this interface
// structurally; no changes to the engine package are required.
type engineAPI interface {
	Start(ctx context.Context) error
	Stop() error
	Status() engine.Status
}

// Perms is the permission snapshot returned by RecheckPermissions. No json
// tags (matching engine.Status, which also has none) so the bound API's
// JSON shape is consistently PascalCase across both.
type Perms struct {
	Accessibility   bool
	ScreenRecording bool
}

// ErrAccessibilityRequired is returned by StartEngine when Accessibility has
// not been granted. It's a sentinel (typed/named) error rather than an
// ad-hoc fmt.Errorf so the rejected-promise message text on the frontend
// side is stable — the Status UI (Task 5) can special-case it (e.g. show the
// Accessibility permission banner) by checking the error string it receives.
var ErrAccessibilityRequired = errors.New("app: accessibility permission required — grant it in System Settings → Privacy & Security → Accessibility, then try again")

// --- Test seams -------------------------------------------------------
//
// Each var below wraps a call with a real side effect (TCC permission
// check, arming real CGEvent input, or shelling out to `open`). Production
// code always uses the real implementation; app_test.go overrides these so
// the whole test suite stays hermetic. In particular: arming mouse/keys for
// real inside a test would violate those packages' core safety invariant
// ("go test never arms real input" — see internal/mouse/mouse_darwin.go's
// arm-guard doc comment), and probing real TCC state would make the
// Accessibility-gate test's outcome depend on whatever permissions the
// machine running `go test` happens to have granted Terminal/the test
// binary.

// accessibilityTrustedFn checks whether this process has Accessibility
// access. Overridden in tests so StartEngine's refusal path is deterministic
// regardless of the real TCC state of the machine running `go test`.
var accessibilityTrustedFn = macapp.AccessibilityTrusted

// screenRecordingTrustedFn checks whether this process has Screen Recording
// access. Overridden in tests for the same determinism reason.
var screenRecordingTrustedFn = macapp.ScreenRecordingTrusted

// mouseArmFn/keysArmFn arm the real CGEvent-posting packages. NEVER call the
// real mouse.Arm/keys.Arm from a test. Overridden to no-op recorders in
// app_test.go.
var mouseArmFn = mouse.Arm
var keysArmFn = keys.Arm

// finderArmFn arms internal/finder's tag WRITER. Same placement and the same
// reasoning as mouseArmFn/keysArmFn above -- and if anything a stronger case:
// a colour tag is an irreversible edit to the user's own files, with four
// downstream consumers and no undo, where a stray CGEvent is merely a stray
// keystroke. NEVER call the real finder.Arm from a test.
var finderArmFn = finder.Arm

// finderProbeFn sends one cheap Apple Event to Finder at startup so a missing
// or revoked Automation grant surfaces at launch rather than in the middle of
// a review pass. Overridden in tests to a recorder -- `go test` must never
// send a real Apple Event.
var finderProbeFn = finder.Probe

// openURLFn shells out to macOS `open` to deep-link into System Settings.
// Overridden in tests to a recorder so tests never actually launch System
// Settings.
var openURLFn = defaultOpenURL

// hudSetVisibleFn applies a visibility DECISION to the native HUD panel. It
// replaced the old hudToggleFn/hudShowFn pair when the HUD gained a gate:
// visibility is no longer "whatever the last toggle did", it is a function of
// (user intent AND Resolve running), so every path now computes the desired
// state and asserts it rather than flipping whatever is there.
//
// Overridden in app_test.go to a recorder -- the real hud.Show/hud.Hide are
// NEVER called from a test. This is the App-side half of test hermeticity for
// the HUD feature (internal/hud's own arm-guard, armed only from app/main.go's
// func main() -- which `go test` never executes -- is the second, independent
// layer of the same guarantee; see hud_darwin.go).
var hudSetVisibleFn = defaultSetHUDVisible

func defaultSetHUDVisible(visible bool) {
	if visible {
		hud.Show()
		return
	}
	hud.Hide()
}

// resolveRunningFn reports whether DaVinci Resolve is running. A seam so the
// HUD gate's state machine can be driven deterministically in tests without a
// real Resolve (which cannot be assumed present on any machine, including this
// one). Production points it at macapp.IsResolveRunning.
//
// NOT engine Status().SidecarConnected, and that distinction is the whole
// point: the sidecar times out routinely while Resolve is merely BUSY, and
// this repo already established that a timeout must never be read as death
// (commit 4a1f0ba). Gating the panel on sidecar liveness would make it flap
// whenever the user does something slow in Resolve.
var resolveRunningFn = macapp.IsResolveRunning

func defaultOpenURL(url string) error {
	return exec.Command("open", url).Start()
}

// App is the Wails-bound application struct. Every exported method below is
// callable from the frontend as window.go.main.App.<MethodName>(...).
type App struct {
	ctx context.Context
	eng engineAPI

	// editor is the preset store + editor API wiring (nil when unavailable).
	editor *editorWiring

	tickerStop chan struct{}

	// --- HUD gate ---------------------------------------------------
	// The panel is visible iff the user has asked for it AND Resolve is
	// running. Splitting the two means an auto-hide (Resolve quit) never
	// destroys the user's intent, so the panel comes back by itself when
	// Resolve returns -- and equally, a manual hide while Resolve is up is
	// never undone by the watcher.
	hudMu sync.Mutex
	// hudUserWants is the user's last EXPLICIT intent (C6, the frontend
	// button, or the fixed-placement launch path).
	hudUserWants bool
	// hudResolveUp is the committed, debounced process-presence state.
	hudResolveUp bool
	// hudPending counts consecutive samples disagreeing with hudResolveUp.
	hudPending int
	// hudResolveRequired is true only when the user switched the setting
	// hudWithoutResolve OFF: the panel then follows Resolve (hidden while it
	// is closed). Zero value = the default (show without Resolve).
	hudResolveRequired bool
	// hudEnabledSet / hudEnabled: the persisted general HUD on/off (setting
	// hudEnabled). hudEnabledSet is false until the first value arrives.
	hudEnabledSet bool
	hudEnabled    bool
}

// NewApp constructs the production App. cfgDir/resourcesDir are resolved by
// resolveAppDirs (see paths.go): bundled into a .app, cfgDir is the seeded
// <data root>/config path (internal/datadir) and resourcesDir points at
// Contents/Resources (for sidecar.Launch); running from the repo in dev mode
// both fall back to the pre-Task-6 literals ("config", "."). If
// resolveAppDirs or engine.New fails, NewApp still returns a usable App with
// a.eng == nil: GetStatus then returns a zero Status and StartEngine returns
// a clear "not initialized" error instead of the whole app failing to
// launch.
func NewApp() *App {
	a := &App{}

	cfgDir, resourcesDir, err := resolveAppDirs()
	if err != nil {
		log.Printf("[app] resolveAppDirs failed: %v — falling back to dev-mode defaults", err)
		cfgDir, resourcesDir = "config", "."
	}

	e, err := engine.New(cfgDir)
	if err != nil {
		log.Printf("[app] engine.New(%q) failed: %v — Start/Stop unavailable until this is fixed", cfgDir, err)
		return a
	}
	e.SetResourcesDir(resourcesDir)
	// Wire the C6 button's HUD-toggle callback on the concrete *engine.Engine
	// -- BEFORE it's assigned to the engineAPI-typed a.eng field below --
	// mirroring SetResourcesDir immediately above. a.ToggleHUD is a method
	// value bound to `a` (already constructed at the top of this function);
	// it only reads/writes hudToggleFn when actually invoked later, so
	// capturing it now is safe regardless of what a.eng is set to next.
	// Needs zero changes to the engineAPI interface or app_test.go's
	// fakeEngine (fakeEngine never touches this callback at all).
	e.SetToggleHUDCallback(a.ToggleHUD)
	a.eng = e
	// Preset store, hot-reloaded config, appdeck and the editor API.
	a.editor = initEditor(e, cfgDir)
	if a.editor != nil {
		a.editor.setHUDHook(a.setHUDSettings)
	}
	return a
}

// EditorToken returns this launch's API token (wave H). The page, served by the
// same Wails asset server, calls window.go.main.App.EditorToken() once and sends
// the value as the X-LD-Token header on every mutating /api request. Memory
// only: a new token per launch. "" when the editor is unavailable.
func (a *App) EditorToken() string {
	if a.editor == nil {
		return ""
	}
	return a.editor.token
}

// StartupState is what StartupState() reports to the page.
type StartupState struct {
	// EditorAvailable is false when the presets could not be opened: there is no
	// editor API then and the deck is held disarmed (fail closed).
	EditorAvailable bool `json:"editorAvailable"`
	// Armed is the live arm state; Reason is why the app came up disarmed
	// ("env", "read-only", "disarmed-last-session", "first-run", "pre-update",
	// "store-unavailable") and "" when it came up armed.
	Armed  bool   `json:"armed"`
	Reason string `json:"reason"`
}

// StartupState lets the page show how the app came up even when there is no
// editor API to ask (the presets folder could not be opened): then
// {editorAvailable:false, armed:false, reason:"store-unavailable"}. With the
// editor it mirrors GET /api/live (armed, armedAtStart's reason).
func (a *App) StartupState() StartupState {
	if a.editor == nil {
		return StartupState{EditorAvailable: false, Armed: false, Reason: preset.StartReasonStoreDown}
	}
	return StartupState{EditorAvailable: true, Armed: a.editor.ad.Armed(), Reason: a.editor.armStart.Reason}
}

// startup is called once by the Wails runtime when the window is created.
// The context is stored for later runtime.EventsEmit calls, the 250ms
// status-broadcast ticker is started, and the engine is auto-started (see
// autoStartEngine below).
//
// *** AUTO-START (2026-07-08) ***: the engine now starts itself here instead
// of waiting for the user to press the Start button — by the time this app
// is opened, Accessibility has already been granted (a one-time setup step),
// so requiring an extra manual Start click every launch was pure friction.
// autoStartEngine() calls the exact same a.StartEngine() the Start button
// calls — same Accessibility gate, same mouse/keys arming, same
// engine.Start — so there is no separate "auto" code path to drift out of
// sync with the manual one. A failure here (Accessibility revoked, config
// load failure, sidecar unreachable) is logged, not fatal: it degrades to
// exactly the pre-auto-start state (engine not running, Status UI shows why
// via GetStatus/SidecarError, user can still press Start manually after
// fixing whatever's wrong) rather than blocking the window from opening.
//
// NEVER call startup() itself from a test: go a.runStatusTicker() spawns a
// real goroutine whose FIRST tick calls wailsRuntime.EventsEmit(a.ctx, ...),
// and EventsEmit calls log.Fatalf (os.Exit) on any ctx that isn't a real
// Wails-constructed one (wails/v2/pkg/runtime/runtime.go's getEvents) — a
// plain context.Background() from a test would silently kill the `go test`
// process up to statusEventInterval later. This pre-existing hermeticity
// trap is exactly why runStatusTicker has never been called from a test
// either; autoStartEngine is split out below specifically so the auto-start
// behavior itself stays testable without going anywhere near the ticker.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.tickerStop = make(chan struct{})
	// Seed the gate SYNCHRONOUSLY before anything can consult it. The watcher
	// below needs hudFlipSamples ticks to commit a change, so without this
	// seed the first ~6s of a session would believe Resolve is absent -- and a
	// C6 press in that window would appear to do nothing on a machine where
	// Resolve is already open.
	a.seedResolveRunning(resolveRunningFn())
	go a.runStatusTicker()
	go a.runHUDWatcher()
	a.autoStartEngine()
}

// autoStartEngine calls StartEngine() once, logging (not propagating) any
// failure — see startup()'s doc comment for the full rationale and the
// test-hermeticity reason this is a separate method rather than inlined
// into startup() itself.
// configureHUDPlacement pushes the _config.hud block into the native HUD
// layer. Called before the engine starts so the panel's very first frame is
// already correct, rather than snapping into place a tick later.
func (a *App) configureHUDPlacement() {
	cfgDir, _, err := resolveAppDirs()
	if err != nil {
		return
	}
	cfg, err := config.Load(filepath.Join(cfgDir, "device-map.json"), filepath.Join(cfgDir, "bindings.json"))
	if err != nil {
		log.Printf("[app] HUD placement: config load failed, keeping default placement: %v", err)
		return
	}
	h := cfg.TopLevel.HUD
	if h.Display == "" || h.HeightPx <= 0 {
		return // not configured -- keep the existing placement behaviour
	}
	edge := h.Edge
	if edge == "" {
		edge = "bottom"
	}
	log.Printf("[app] HUD placement: display=%q edge=%q height=%d", h.Display, edge, h.HeightPx)
	hud.Configure(h.Display, edge, h.HeightPx)

	// A configured fixed placement means a dedicated always-on band on its own
	// monitor -- so ask for it at launch rather than making the user press C6
	// after every restart. C6 still toggles it. The corner-card path (no
	// placement configured) is unchanged and still starts hidden.
	//
	// This records INTENT and then lets the gate decide. With Resolve running
	// the observable behaviour is exactly as before; with Resolve closed the
	// band stays off screen, which is the whole point of the gate -- an empty
	// orange strip full of "--:--:--:--" is not something to show at launch.
	a.hudMu.Lock()
	a.hudUserWants = !a.hudEnabledSet || a.hudEnabled // the saved HUD on/off wins over the launch default
	a.hudMu.Unlock()
	if a.applyHUDVisibility() {
		log.Printf("[app] HUD shown on launch (fixed placement configured)")
	} else {
		log.Printf("[app] HUD placement configured, but DaVinci Resolve is not running — staying hidden until it launches")
	}
}

func (a *App) autoStartEngine() {
	a.configureHUDPlacement()
	log.Printf("[app] auto-start: calling StartEngine()")
	if err := a.StartEngine(); err != nil {
		log.Printf("[app] auto-start on launch FAILED (user can still press Start manually): %v", err)
	} else {
		log.Printf("[app] auto-start: StartEngine() returned OK")
	}
}

// shutdown is called by the Wails runtime as the app is closing. It stops
// the status ticker. It deliberately does NOT call StopEngine — Task 7 (E2E)
// may want an explicit user-visible Stop before quit rather than a silent
// one triggered from here.
func (a *App) shutdown(ctx context.Context) {
	a.editor.Close()
	if a.tickerStop != nil {
		close(a.tickerStop)
		a.tickerStop = nil
	}
}

// runStatusTicker emits a "status" event every statusEventInterval for the
// lifetime of the window. It additionally pushes the same snapshot to the
// native HUD panel (internal/hud), but ONLY when the HUD is currently
// visible -- hud.Visible() is a cheap Go-side read (no CGO/AppKit call), so
// this costs nothing on every tick while the HUD is hidden (the common
// case). The HUD is a pure read-only consumer of the SAME snapshot already
// emitted to the frontend: no new engine state, no locks touched, no
// collision with GetStatus's existing mutex-guarded copy.
func (a *App) runStatusTicker() {
	ticker := time.NewTicker(statusEventInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.tickerStop:
			return
		case <-ticker.C:
			status := a.GetStatus()
			wailsRuntime.EventsEmit(a.ctx, "status", status)
			if hud.Visible() {
				hud.Update(formatHUD(status))
			}
		}
	}
}

// StartEngine arms the real mouse/keyboard input packages and starts the
// engine. It refuses (returning ErrAccessibilityRequired) if Accessibility
// has not been granted — arming without Accessibility would be a silent
// no-op for every CGEvent post anyway (see mouse/keys Armed() docs), so
// refusing up front gives the Status UI a clear, actionable error instead.
//
// Arming happens ONLY here — never at package init, never in NewApp/startup
// — so merely opening the window never arms real input. This mirrors
// cmd/2t5-deck-plus/main.go's own mouse.Arm()/keys.Arm() placement (immediately
// before engine.Start, never earlier) exactly.
//
// Idempotent (2026-07-08, needed once startup() started auto-calling this):
// if the engine is already running (engine.ErrAlreadyRunning), that is
// treated as a SUCCESS, not an error — pressing the Start button after
// auto-start already started the engine (or pressing it twice) must be a
// harmless no-op, not a surfaced error the Status UI would have to explain.
// mouseArmFn/keysArmFn are still called first in that case, which is safe:
// both are documented idempotent (sync.Once-guarded, see internal/mouse
// and internal/keys' Arm() docs), so re-arming an already-armed package is a
// cheap no-op, not a second real side effect. Any OTHER Start error (a
// genuine failure) still propagates unchanged.
func (a *App) StartEngine() error {
	if a.eng == nil {
		return fmt.Errorf("app: engine not initialized (config failed to load — see logs)")
	}
	if !accessibilityTrustedFn() {
		return ErrAccessibilityRequired
	}
	mouseArmFn()
	keysArmFn()
	finderArmFn()
	// Discover a missing/revoked Automation grant HERE, once, rather than on
	// the first P-press mid-review. Never fatal: the rest of the deck works
	// perfectly without Finder tagging, so this only warns.
	//
	// The grant survives rebuilds only because scripts/build-app.sh signs
	// with the persistent "2T5 LD-Resolve Dev" certificate and hard-fails if
	// the designated requirement comes out cdhash-anchored.
	probeErr := finderProbeFn()
	recordFinderProbe(probeErr)
	if err := probeErr; err != nil {
		log.Printf("[app] Finder automation unavailable — the deck's Finder tag profile will not work "+
			"until 2T5 DECK+ is allowed to control Finder in System Settings > Privacy & Security > Automation: %v", err)
	}
	if err := a.eng.Start(a.ctx); err != nil && !errors.Is(err, engine.ErrAlreadyRunning) {
		return err
	}
	return nil
}

// StopEngine stops the engine. Safe to call when the engine was never
// started (Engine.Stop() is itself a no-op in that case) or when a.eng is
// nil (config failed to load).
func (a *App) StopEngine() error {
	if a.eng == nil {
		return nil
	}
	return a.eng.Stop()
}

// GetStatus returns a snapshot of the engine's current state. Safe to call
// before StartEngine (Running=false) and when a.eng is nil (zero Status).
func (a *App) GetStatus() engine.Status {
	if a.eng == nil {
		return engine.Status{}
	}
	return a.eng.Status()
}

// ToggleHUD flips the native always-on-top HUD status monitor's visibility
// and returns the new state. Bound for the frontend's "Show HUD" button; the
// Loupedeck C6 button reaches this exact same seam via a completely separate
// path -- the engine's injected toggle callback (see NewApp's
// e.SetToggleHUDCallback(a.ToggleHUD) wiring and dispatchNoteOn's
// IsToggleHud() case in internal/engine) -- so both toggle sources always
// stay in sync (same underlying hud.Toggle() state).
func (a *App) ToggleHUD() bool {
	// The toggle flips the PERSISTED hudEnabled setting (the same one the editor's
	// "Show the HUD" switch writes), so the deck button, the editor and the next
	// launch always agree. Fn+C6 (Global fn bank, Resolve col-fn) and the editor
	// button both land here.
	if w := a.editor; w != nil {
		if next, err := w.toggleHUDEnabled(); err == nil {
			a.hudMu.Lock()
			withoutResolve := !a.hudResolveRequired
			a.hudMu.Unlock()
			a.setHUDSettings(next, withoutResolve) // immediate; the recompose pushes the same value again, a no-op
			return next
		} else {
			log.Printf("[app] ToggleHUD: could not save hudEnabled (%v); toggling for this run only", err)
		}
	}
	return a.toggleHUDTransient()
}

// toggleHUDTransient is the old, unsaved toggle: the fallback when the preset
// store is unavailable or read-only.
func (a *App) toggleHUDTransient() bool {
	a.hudMu.Lock()
	a.hudUserWants = !a.hudUserWants
	want := a.hudUserWants
	up := a.hudResolveUp
	req := a.hudResolveRequired
	a.hudMu.Unlock()

	visible := want && (up || !req)
	if want && !up && req {
		log.Printf("[app] HUD requested, but DaVinci Resolve is not running — staying hidden until it launches")
	}
	hudSetVisibleFn(visible)
	return visible
}

// --- HUD gate -------------------------------------------------------------

// seedResolveRunning commits a process-presence reading immediately, bypassing
// the hysteresis counter. Used once at startup, where there is no previous
// state for a sample to be "consistent with" and waiting hudFlipSamples ticks
// would just mean starting from a known-wrong assumption.
func (a *App) seedResolveRunning(running bool) {
	a.hudMu.Lock()
	a.hudResolveUp = running
	a.hudPending = 0
	a.hudMu.Unlock()
}

// observeResolveRunning feeds one process-presence sample into the gate and
// applies the resulting visibility if the committed state changed.
//
// Hysteresis: a sample that agrees with the committed state resets the
// counter; hudFlipSamples CONSECUTIVE disagreeing samples are required to
// commit a flip. So an isolated bad reading -- Launch Services momentarily
// answering "no" while Resolve is mid-launch, say -- cannot blink the panel.
//
// Split out from runHUDWatcher so the state machine is testable without a
// clock, a goroutine, or a real Resolve.
func (a *App) observeResolveRunning(running bool) {
	a.hudMu.Lock()
	if running == a.hudResolveUp {
		a.hudPending = 0
		a.hudMu.Unlock()
		return
	}
	a.hudPending++
	if a.hudPending < hudFlipSamples {
		a.hudMu.Unlock()
		return
	}
	a.hudResolveUp = running
	a.hudPending = 0
	req := a.hudResolveRequired
	visible := a.hudUserWants && (running || !req)
	a.hudMu.Unlock()

	if running {
		log.Printf("[app] DaVinci Resolve started — HUD %s", visibleWord(visible))
	} else if req {
		log.Printf("[app] DaVinci Resolve is gone — hiding HUD")
	} else {
		log.Printf("[app] DaVinci Resolve is gone — HUD stays (hudWithoutResolve is on)")
	}
	hudSetVisibleFn(visible)
}

// applyHUDVisibility asserts the currently-correct visibility and reports it.
func (a *App) applyHUDVisibility() bool {
	a.hudMu.Lock()
	visible := a.hudUserWants && (a.hudResolveUp || !a.hudResolveRequired)
	a.hudMu.Unlock()
	hudSetVisibleFn(visible)
	return visible
}

// setHUDSettings applies the hudEnabled (general on/off) and hudWithoutResolve
// (on = the HUD may show while Resolve is closed) settings and re-asserts
// visibility when either changed. hudEnabled acts only on a CHANGE (or on its
// first value, which just records it for the launch decision): the editor
// pushes both on every config recompose, and a deck-button toggle (C6) must not
// be undone by an unrelated edit.
func (a *App) setHUDSettings(enabled, withoutResolve bool) {
	a.hudMu.Lock()
	changed := a.hudResolveRequired == withoutResolve // required = !on, so equal means it flips
	a.hudResolveRequired = !withoutResolve
	if !a.hudEnabledSet {
		a.hudEnabledSet, a.hudEnabled = true, enabled
	} else if a.hudEnabled != enabled {
		a.hudEnabled = enabled
		a.hudUserWants = enabled
		changed = true
	}
	a.hudMu.Unlock()
	if changed {
		a.applyHUDVisibility()
	}
}

func visibleWord(v bool) string {
	if v {
		return "shown (user had it on)"
	}
	return "left hidden (user has it off)"
}

// runHUDWatcher polls Resolve's process presence for the lifetime of the
// window. Its own goroutine, deliberately: the MIDI/input path must never
// block on anything slow, and this makes a Launch Services call per tick.
func (a *App) runHUDWatcher() {
	ticker := time.NewTicker(hudWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.tickerStop:
			return
		case <-ticker.C:
			a.observeResolveRunning(resolveRunningFn())
		}
	}
}

// RecheckPermissions re-probes both required TCC permissions live. Both
// underlying checks are read-only (neither
// CGPreflightScreenCaptureAccess nor AXIsProcessTrusted triggers the system
// permission prompt), so this is safe to call as often as the UI wants —
// typically after the user returns from System Settings via a "Recheck"
// button.
func (a *App) RecheckPermissions() Perms {
	return Perms{
		Accessibility:   accessibilityTrustedFn(),
		ScreenRecording: screenRecordingTrustedFn(),
	}
}

// OpenAccessibilitySettings deep-links into System Settings' Accessibility
// pane via the x-apple.systempreferences URL scheme.
func (a *App) OpenAccessibilitySettings() {
	if err := openURLFn("x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"); err != nil {
		log.Printf("[app] OpenAccessibilitySettings: %v", err)
	}
}

// OpenScreenRecordingSettings deep-links into System Settings' Screen
// Recording pane via the x-apple.systempreferences URL scheme.
func (a *App) OpenScreenRecordingSettings() {
	if err := openURLFn("x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"); err != nil {
		log.Printf("[app] OpenScreenRecordingSettings: %v", err)
	}
}

// recordFinderProbe remembers what the startup Finder Apple Event said, for
// GET /api/live permissions.automationFinder: success = granted; a refusal
// (error -1743, "not allowed / not permitted") = denied; any other failure
// (Finder not running ...) says nothing, so the state stays unknown.
func recordFinderProbe(err error) {
	switch {
	case err == nil:
		macapp.SetFinderAutomation(macapp.FinderAutomationGranted)
	case strings.Contains(err.Error(), "-1743") || strings.Contains(strings.ToLower(err.Error()), "not allowed") ||
		strings.Contains(strings.ToLower(err.Error()), "not permitted"):
		macapp.SetFinderAutomation(macapp.FinderAutomationDenied)
	}
}

// --- native export / import (WKWebView cannot download a blob) ------------
//
// The frontend calls these as window.go.main.App.ExportPresets(appId),
// .ImportPresetsFromDialog() and .ApplyImport(token) when present, and keeps
// GET /api/export and POST /api/import for the browser. They share the
// Store.Export / Store.Import code of the HTTP endpoints (editorapi.NativeIO).

// saveFileDialogFn / openFileDialogFn show the native dialogs. Seams: tests
// replace them, a test never opens a real dialog.
var saveFileDialogFn = wailsRuntime.SaveFileDialog
var openFileDialogFn = wailsRuntime.OpenFileDialog

var errEditorUnavailable = errors.New("app: the preset editor is not available (the presets could not be opened, see the log)")

var zipFilter = []wailsRuntime.FileFilter{{DisplayName: "2T5 DECK+ presets (*.zip)", Pattern: "*.zip"}}

// ExportPresets asks where to save, then writes the same zip GET /api/export
// sends. appID is "all" (or "") or a preset id. It returns the saved path, ""
// when the user cancelled.
func (a *App) ExportPresets(appID string) (string, error) {
	if a.editor == nil || a.editor.native == nil {
		return "", errEditorUnavailable
	}
	data, name, err := a.editor.native.ExportBytes(appID) // before the dialog: an unknown app asks nothing
	if err != nil {
		return "", err
	}
	path, err := saveFileDialogFn(a.ctx, wailsRuntime.SaveDialogOptions{
		Title: "Export 2T5 DECK+ presets", DefaultFilename: name, Filters: zipFilter, CanCreateDirectories: true,
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	return a.editor.native.WriteExport(path, data)
}

// ImportPresetsFromDialog asks for a presets zip and returns the dry-run diff
// with a one-use token; nothing is imported until ApplyImport(token). A
// cancelled dialog returns an empty preview (Token "").
func (a *App) ImportPresetsFromDialog() (editorapi.ImportPreview, error) {
	if a.editor == nil || a.editor.native == nil {
		return editorapi.ImportPreview{}, errEditorUnavailable
	}
	path, err := openFileDialogFn(a.ctx, wailsRuntime.OpenDialogOptions{Title: "Import 2T5 DECK+ presets", Filters: zipFilter})
	if err != nil {
		return editorapi.ImportPreview{}, err
	}
	if path == "" {
		return editorapi.ImportPreview{Files: []preset.ImportFile{}}, nil
	}
	return a.editor.native.PreviewImport(path)
}

// ApplyImport applies the import ImportPresetsFromDialog previewed (one undo
// step, preceded by a backup). An import that installs shell or destructive
// actions is refused here (the error says so; the token stays valid): the UI
// shows the preview's risk and calls ApplyImportConfirmed once the user agreed.
func (a *App) ApplyImport(token string) (editorapi.ImportResult, error) {
	if a.editor == nil || a.editor.native == nil {
		return editorapi.ImportResult{}, errEditorUnavailable
	}
	return a.editor.native.ApplyImport(token, false)
}

// ApplyImportConfirmed is ApplyImport after the user confirmed the risk
// (ImportPreview.needsConfirm, files[].runsProgram).
func (a *App) ApplyImportConfirmed(token string) (editorapi.ImportResult, error) {
	if a.editor == nil || a.editor.native == nil {
		return editorapi.ImportResult{}, errEditorUnavailable
	}
	return a.editor.native.ApplyImport(token, true)
}

// ApplyImportSkippingRisky applies the import ImportPresetsFromDialog previewed
// but leaves out every shell and destructive assignment it would add or change
// (result.dropped lists them); it needs no risk confirmation. One undo step.
func (a *App) ApplyImportSkippingRisky(token string) (editorapi.ImportResult, error) {
	if a.editor == nil || a.editor.native == nil {
		return editorapi.ImportResult{}, errEditorUnavailable
	}
	return a.editor.native.ApplyImportOpts(token, preset.ImportOptions{SkipRisky: true})
}
