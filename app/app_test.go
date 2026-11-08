package main

import (
	"context"
	"errors"
	"testing"

	"2t5-deck-plus/internal/engine"
	"2t5-deck-plus/internal/finder"
)

// fakeEngine is a minimal engineAPI stub. It never touches CGO, real
// sidecar/MIDI hardware, or any package-level engine seams — it's plain
// in-memory state, so tests using it are fully hermetic regardless of what
// the engine package's own (unexported) production seams currently default
// to.
type fakeEngine struct {
	startCalled bool
	startCtx    context.Context
	startErr    error

	stopCalled bool
	stopErr    error

	status engine.Status
}

func (f *fakeEngine) Start(ctx context.Context) error {
	f.startCalled = true
	f.startCtx = ctx
	return f.startErr
}

func (f *fakeEngine) Stop() error {
	f.stopCalled = true
	return f.stopErr
}

func (f *fakeEngine) Status() engine.Status {
	return f.status
}

// --- seam stubs ---------------------------------------------------------

func stubAccessibility(t *testing.T, trusted bool) {
	t.Helper()
	orig := accessibilityTrustedFn
	accessibilityTrustedFn = func() bool { return trusted }
	t.Cleanup(func() { accessibilityTrustedFn = orig })
}

func stubScreenRecording(t *testing.T, trusted bool) {
	t.Helper()
	orig := screenRecordingTrustedFn
	screenRecordingTrustedFn = func() bool { return trusted }
	t.Cleanup(func() { screenRecordingTrustedFn = orig })
}

// stubArmFns replaces mouseArmFn/keysArmFn/finderArmFn AND finderProbeFn
// with no-op recorders — this is the ONLY safe way to exercise StartEngine's
// happy path in a test.
//
// All four matter, and finder's two matter most. finder.Arm() would flip a
// process-global that stays set for every later test in this binary, opening
// the door to a test writing REAL Finder tags to REAL files with no undo; and
// finder.Probe() sends a genuine Apple Event to Finder, which `go test` must
// never do. The real mouse.Arm/keys.Arm/finder.Arm/finder.Probe are NEVER
// called here.
func stubArmFns(t *testing.T) (mouseArmed, keysArmed *bool) {
	t.Helper()
	m, k := new(bool), new(bool)
	origMouse, origKeys := mouseArmFn, keysArmFn
	origFinderArm, origFinderProbe := finderArmFn, finderProbeFn
	mouseArmFn = func() { *m = true }
	keysArmFn = func() { *k = true }
	finderArmFn = func() {}
	finderProbeFn = func() error { return nil }
	t.Cleanup(func() {
		mouseArmFn = origMouse
		keysArmFn = origKeys
		finderArmFn = origFinderArm
		finderProbeFn = origFinderProbe
	})
	return m, k
}

// TestFinderIsNeverArmedDuringTests is a tripwire: internal/finder must still
// be unarmed after this whole package's tests have run, proving no test ever
// reached the real finder.Arm through StartEngine.
func TestFinderIsNeverArmedDuringTests(t *testing.T) {
	if finder.Armed() {
		t.Fatal("internal/finder is ARMED during `go test` — a test could write real Finder tags to real files, " +
			"and there is no undo. Check that every StartEngine test calls stubArmFns.")
	}
}

// stubHudVisible replaces hudSetVisibleFn with a recorder capturing every
// visibility decision the App applies, in order -- this is the ONLY safe way
// to exercise the HUD paths in a test. The real hud.Show/hud.Hide
// (internal/hud, CGO/NSPanel) are NEVER called here.
func stubHudVisible(t *testing.T) *[]bool {
	t.Helper()
	var applied []bool
	orig := hudSetVisibleFn
	hudSetVisibleFn = func(v bool) { applied = append(applied, v) }
	t.Cleanup(func() { hudSetVisibleFn = orig })
	return &applied
}

// stubResolveRunning replaces the process-presence seam with a value the test
// controls, so the HUD gate can be driven without a real DaVinci Resolve.
// Returns a pointer the test can flip mid-run to simulate Resolve
// launching/quitting.
func stubResolveRunning(t *testing.T, running bool) *bool {
	t.Helper()
	state := running
	orig := resolveRunningFn
	resolveRunningFn = func() bool { return state }
	t.Cleanup(func() { resolveRunningFn = orig })
	return &state
}

func stubOpenURL(t *testing.T) *[]string {
	t.Helper()
	calls := &[]string{}
	orig := openURLFn
	openURLFn = func(url string) error {
		*calls = append(*calls, url)
		return nil
	}
	t.Cleanup(func() { openURLFn = orig })
	return calls
}

// --- GetStatus ------------------------------------------------------------

func TestGetStatus_NilEngine_ReturnsZeroStatus(t *testing.T) {
	a := &App{}
	got := a.GetStatus()
	if got != (engine.Status{}) {
		t.Fatalf("GetStatus() with nil engine = %+v, want zero value", got)
	}
}

func TestGetStatus_DelegatesToEngine(t *testing.T) {
	want := engine.Status{Running: true, Page: "color", ActiveWheel: "lift"}
	fe := &fakeEngine{status: want}
	a := &App{eng: fe}

	got := a.GetStatus()
	if got != want {
		t.Fatalf("GetStatus() = %+v, want %+v", got, want)
	}
}

// --- StartEngine ------------------------------------------------------------

func TestStartEngine_NilEngine_ReturnsError(t *testing.T) {
	stubAccessibility(t, true)
	a := &App{}

	err := a.StartEngine()
	if err == nil {
		t.Fatal("StartEngine() with nil engine = nil error, want non-nil")
	}
}

func TestStartEngine_AccessibilityUntrusted_ReturnsTypedErrorAndDoesNotArmOrStart(t *testing.T) {
	stubAccessibility(t, false)
	mouseArmed, keysArmed := stubArmFns(t)
	fe := &fakeEngine{}
	a := &App{eng: fe, ctx: context.Background()}

	err := a.StartEngine()
	if !errors.Is(err, ErrAccessibilityRequired) {
		t.Fatalf("StartEngine() error = %v, want ErrAccessibilityRequired", err)
	}
	if *mouseArmed || *keysArmed {
		t.Fatalf("StartEngine() armed input while Accessibility untrusted: mouseArmed=%v keysArmed=%v", *mouseArmed, *keysArmed)
	}
	if fe.startCalled {
		t.Fatal("StartEngine() called engine.Start while Accessibility untrusted")
	}
}

func TestStartEngine_AccessibilityTrusted_ArmsThenStartsEngine(t *testing.T) {
	stubAccessibility(t, true)
	mouseArmed, keysArmed := stubArmFns(t)
	fe := &fakeEngine{}
	ctx := context.Background()
	a := &App{eng: fe, ctx: ctx}

	if err := a.StartEngine(); err != nil {
		t.Fatalf("StartEngine() error = %v, want nil", err)
	}
	if !*mouseArmed {
		t.Error("StartEngine() did not arm mouse")
	}
	if !*keysArmed {
		t.Error("StartEngine() did not arm keys")
	}
	if !fe.startCalled {
		t.Error("StartEngine() did not call engine.Start")
	}
	if fe.startCtx != ctx {
		t.Error("StartEngine() did not pass App.ctx through to engine.Start")
	}
}

func TestStartEngine_PropagatesEngineStartError(t *testing.T) {
	stubAccessibility(t, true)
	stubArmFns(t)
	wantErr := errors.New("sidecar unreachable")
	fe := &fakeEngine{startErr: wantErr}
	a := &App{eng: fe, ctx: context.Background()}

	err := a.StartEngine()
	if !errors.Is(err, wantErr) {
		t.Fatalf("StartEngine() error = %v, want %v", err, wantErr)
	}
}

// TestStartEngine_AlreadyRunning_IsIdempotentSuccess is the 2026-07-08
// idempotency fix this needed once startup() started auto-calling
// StartEngine(): pressing the Start button (or auto-start firing) against
// an engine that's already running must be a harmless success, not a
// surfaced error — engine.ErrAlreadyRunning specifically must be swallowed,
// unlike any OTHER Start error (covered by
// TestStartEngine_PropagatesEngineStartError above, which still asserts a
// genuine failure propagates unchanged).
func TestStartEngine_AlreadyRunning_IsIdempotentSuccess(t *testing.T) {
	stubAccessibility(t, true)
	mouseArmed, keysArmed := stubArmFns(t)
	fe := &fakeEngine{startErr: engine.ErrAlreadyRunning}
	a := &App{eng: fe, ctx: context.Background()}

	err := a.StartEngine()
	if err != nil {
		t.Fatalf("StartEngine() error = %v, want nil (ErrAlreadyRunning must be swallowed)", err)
	}
	if !fe.startCalled {
		t.Error("StartEngine() did not call engine.Start")
	}
	// Arming is still expected even in the already-running case — both
	// mouse.Arm/keys.Arm are documented idempotent, so re-arming here is a
	// cheap no-op, not a second real side effect (see StartEngine's doc
	// comment).
	if !*mouseArmed || !*keysArmed {
		t.Errorf("StartEngine() did not (re-)arm input: mouseArmed=%v keysArmed=%v", *mouseArmed, *keysArmed)
	}
}

// --- autoStartEngine ---------------------------------------------------
//
// autoStartEngine (called from startup(), see app.go's doc comment) is
// tested directly rather than through startup() itself — startup() spawns
// runStatusTicker() as a real goroutine that would call
// wailsRuntime.EventsEmit against a non-Wails test context on its first
// tick, which calls log.Fatalf (os.Exit) and would kill the test binary.
// autoStartEngine has no such landmine: it only calls a.StartEngine(),
// exercised entirely through the same seams as the StartEngine tests above.

func TestAutoStartEngine_AccessibilityTrusted_StartsEngine(t *testing.T) {
	stubAccessibility(t, true)
	stubArmFns(t)
	fe := &fakeEngine{}
	a := &App{eng: fe, ctx: context.Background()}

	a.autoStartEngine()

	if !fe.startCalled {
		t.Error("autoStartEngine() did not call engine.Start")
	}
}

// TestAutoStartEngine_Failure_LogsAndDoesNotPanic covers the degrade-
// gracefully path (e.g. Accessibility not yet granted on a fresh install) —
// autoStartEngine must swallow the error (startup() has no error return to
// propagate to) rather than panic, leaving the engine simply not running so
// the user can still press Start manually later.
func TestAutoStartEngine_Failure_LogsAndDoesNotPanic(t *testing.T) {
	stubAccessibility(t, false)
	fe := &fakeEngine{}
	a := &App{eng: fe, ctx: context.Background()}

	a.autoStartEngine() // must not panic

	if fe.startCalled {
		t.Error("autoStartEngine() called engine.Start while Accessibility untrusted")
	}
}

// TestAutoStartEngine_AlreadyRunning_IsSilentNoOp: if something (a test
// harness, a future double-call) has already started the engine before
// autoStartEngine runs, it must stay a quiet no-op — same idempotency
// contract as TestStartEngine_AlreadyRunning_IsIdempotentSuccess, exercised
// through the auto-start entry point specifically.
func TestAutoStartEngine_AlreadyRunning_IsSilentNoOp(t *testing.T) {
	stubAccessibility(t, true)
	stubArmFns(t)
	fe := &fakeEngine{startErr: engine.ErrAlreadyRunning}
	a := &App{eng: fe, ctx: context.Background()}

	a.autoStartEngine() // must not panic, must not surface the sentinel anywhere

	if !fe.startCalled {
		t.Error("autoStartEngine() did not call engine.Start")
	}
}

// --- StopEngine ------------------------------------------------------------

func TestStopEngine_NilEngine_ReturnsNil(t *testing.T) {
	a := &App{}
	if err := a.StopEngine(); err != nil {
		t.Fatalf("StopEngine() with nil engine = %v, want nil", err)
	}
}

func TestStopEngine_DelegatesToEngine(t *testing.T) {
	fe := &fakeEngine{}
	a := &App{eng: fe}

	if err := a.StopEngine(); err != nil {
		t.Fatalf("StopEngine() error = %v, want nil", err)
	}
	if !fe.stopCalled {
		t.Error("StopEngine() did not call engine.Stop")
	}
}

func TestStopEngine_PropagatesEngineStopError(t *testing.T) {
	wantErr := errors.New("stop failed")
	fe := &fakeEngine{stopErr: wantErr}
	a := &App{eng: fe}

	err := a.StopEngine()
	if !errors.Is(err, wantErr) {
		t.Fatalf("StopEngine() error = %v, want %v", err, wantErr)
	}
}

// --- RecheckPermissions -----------------------------------------------------

func TestRecheckPermissions_ReturnsLiveSeamValues(t *testing.T) {
	cases := []struct {
		name            string
		accessibility   bool
		screenRecording bool
	}{
		{"both granted", true, true},
		{"both denied", false, false},
		{"accessibility only", true, false},
		{"screen recording only", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubAccessibility(t, tc.accessibility)
			stubScreenRecording(t, tc.screenRecording)
			a := &App{}

			got := a.RecheckPermissions()
			want := Perms{Accessibility: tc.accessibility, ScreenRecording: tc.screenRecording}
			if got != want {
				t.Fatalf("RecheckPermissions() = %+v, want %+v", got, want)
			}
		})
	}
}

// --- ToggleHUD -----------------------------------------------------------
//
// These tests exercise App.ToggleHUD entirely through the hudSetVisibleFn and
// resolveRunningFn seams -- internal/hud (a CGO/AppKit package) is never
// touched by this test file, and no test in this package calls the real
// hud.Show/hud.Hide.

func TestToggleHUD_ResolveRunning_ShowsThenHides(t *testing.T) {
	stubResolveRunning(t, true)
	applied := stubHudVisible(t)
	a := &App{}
	a.seedResolveRunning(true)

	if got := a.ToggleHUD(); !got {
		t.Fatalf("first ToggleHUD() = %v, want true", got)
	}
	if got := a.ToggleHUD(); got {
		t.Fatalf("second ToggleHUD() = %v, want false", got)
	}
	if want := []bool{true, false}; !equalBools(*applied, want) {
		t.Fatalf("applied visibility = %v, want %v", *applied, want)
	}
}

// The user's actual complaint: with Resolve closed the panel must not appear,
// even if he presses the toggle. Intent is still recorded (see
// TestHUDGate_IntentSurvivesResolveRestart) -- it just does not reach screen.
func TestToggleHUD_ResolveClosed_NeverShows(t *testing.T) {
	stubResolveRunning(t, false)
	applied := stubHudVisible(t)
	a := &App{}
	a.hudResolveRequired = true // hudWithoutResolve switched off
	a.seedResolveRunning(false)

	if got := a.ToggleHUD(); got {
		t.Fatalf("ToggleHUD() with Resolve closed = %v, want false", got)
	}
	for i, v := range *applied {
		if v {
			t.Fatalf("applied[%d] = true — the HUD was shown with Resolve closed", i)
		}
	}
}

// --- Open*Settings -----------------------------------------------------

func TestOpenAccessibilitySettings_OpensCorrectURL(t *testing.T) {
	calls := stubOpenURL(t)
	a := &App{}

	a.OpenAccessibilitySettings()

	if len(*calls) != 1 {
		t.Fatalf("openURLFn called %d times, want 1", len(*calls))
	}
	want := "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
	if (*calls)[0] != want {
		t.Fatalf("openURLFn called with %q, want %q", (*calls)[0], want)
	}
}

func TestOpenScreenRecordingSettings_OpensCorrectURL(t *testing.T) {
	calls := stubOpenURL(t)
	a := &App{}

	a.OpenScreenRecordingSettings()

	if len(*calls) != 1 {
		t.Fatalf("openURLFn called %d times, want 1", len(*calls))
	}
	want := "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
	if (*calls)[0] != want {
		t.Fatalf("openURLFn called with %q, want %q", (*calls)[0], want)
	}
}

// --- NewApp -----------------------------------------------------------

// TestNewApp_MissingConfigDir_ReturnsUsableAppWithNilEngine exercises the
// real engine.New("config") call (no seam needed — engine.New only reads
// JSON files off disk and does one read-only AXIsProcessTrusted probe, both
// safe in a test). Run from app/'s working directory, "config" does not
// exist relative to cwd, so engine.New must fail and NewApp must degrade
// gracefully instead of panicking.
func TestNewApp_MissingConfigDir_ReturnsUsableAppWithNilEngine(t *testing.T) {
	a := NewApp()
	if a == nil {
		t.Fatal("NewApp() = nil")
	}
	if a.eng != nil {
		t.Fatalf("NewApp() with missing config dir: a.eng = %#v, want nil (relative \"config\" does not exist under app/'s test working directory)", a.eng)
	}
	// Degraded-mode contract: GetStatus/StopEngine must not panic.
	if got := a.GetStatus(); got != (engine.Status{}) {
		t.Fatalf("GetStatus() on degraded App = %+v, want zero value", got)
	}
	if err := a.StopEngine(); err != nil {
		t.Fatalf("StopEngine() on degraded App = %v, want nil", err)
	}
}

// --- shutdown -----------------------------------------------------------

// TestShutdown_NilEngine_DoesNotPanic covers the degraded-mode case
// (config load failed, a.eng is nil) — shutdown must stay a safe no-op on
// the engine side, matching GetStatus/StopEngine's existing nil-engine
// posture.
func TestShutdown_NilEngine_DoesNotPanic(t *testing.T) {
	a := &App{}
	a.shutdown(context.Background()) // must not panic
}

// TestShutdown_ClosesTickerStop confirms shutdown() closes/nils tickerStop.
func TestShutdown_ClosesTickerStop(t *testing.T) {
	fe := &fakeEngine{}
	a := &App{eng: fe, tickerStop: make(chan struct{})}

	a.shutdown(context.Background())

	if a.tickerStop != nil {
		t.Error("shutdown() did not nil out tickerStop")
	}
}
