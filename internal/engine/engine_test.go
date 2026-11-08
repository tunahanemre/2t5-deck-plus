package engine

// Tests for the Engine lifecycle (New/Start/Stop/Status) plus the moved
// dispatchNoteOn/dispatchBalanceStep/executeResetWheel/DeriveRequiredCalibPoints
// helpers (relocated verbatim from cmd/2t5-deck-plus/main_test.go).
//
// All tests run without CGO access to a display and without Resolve running.
// CGO-backed functions (mouse.Click, mouse.DoubleClick, mouseScrubFn) are
// replaced by capturable vars before each test. Hardware-backed lifecycle
// pieces (Loupedeck MIDI port, the Python IPC sidecar) are replaced by the
// startMIDIFn/launchSidecarFn seams and a real-but-fake unix-socket sidecar
// respectively, so Start/Stop can be exercised in CI without any of that
// hardware present AND without ever discovering/spawning a real python3
// process (every test that calls Start must stub launchSidecarFn — a
// production sidecar.Launch call would otherwise probe real python3
// interpreters on the machine running `go test`, which is slow and
// non-hermetic, and — if the machine happens to have a working
// DaVinciResolveScript-importable python3 AND no live socket — would spawn a
// real sidecar.py subprocess from a unit test).

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/calib"
	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/ipc"
	"2t5-deck-plus/internal/mode"
	"2t5-deck-plus/internal/target"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

const minimalDeviceMap = `{"knobs":{"36":"Knob1"},"sliders":{},"buttons":{}}`

func loadConfig(t *testing.T, bindingsJSON string) *config.Config {
	t.Helper()
	dm := writeTmp(t, minimalDeviceMap)
	bn := writeTmp(t, bindingsJSON)
	cfg, err := config.Load(dm, bn)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func minimalCalib(t *testing.T, pts map[string][2]float64) *calib.Calib {
	t.Helper()
	type point struct {
		X    float64 `json:"x"`
		Y    float64 `json:"y"`
		Axis string  `json:"axis"`
	}
	type calibFile struct {
		Points map[string]point `json:"points"`
	}
	cf := calibFile{Points: map[string]point{}}
	for name, xy := range pts {
		cf.Points[name] = point{X: xy[0], Y: xy[1], Axis: "h"}
	}
	b, _ := json.Marshal(cf)
	path := writeTmp(t, string(b))
	cal, err := calib.Load(path)
	if err != nil {
		t.Fatalf("calib.Load: %v", err)
	}
	return cal
}

func approxEqF64(a, b, eps float64) bool {
	return math.Abs(a-b) < eps
}

// startFakeSidecar spins up a real unix-domain-socket listener that speaks the
// ipc length-prefixed-JSON frame protocol well enough to satisfy ipc.Dial +
// the engine's "status" polling (it always replies {"page":"color"}). This
// lets Start()/Stop() be exercised without a live Python sidecar.
//
// The socket is created directly under os.TempDir() (NOT t.TempDir()) with a
// short random suffix — macOS's sun_path has a ~104-byte limit, and
// t.TempDir() nests under a path long enough (subtest name included) to blow
// past that limit and fail bind() with EINVAL.
func startFakeSidecar(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-sidecar-*.sock")
	if err != nil {
		t.Fatalf("fake sidecar tempname: %v", err)
	}
	sockPath := f.Name()
	f.Close()
	os.Remove(sockPath) // net.Listen requires the path not to exist yet
	t.Cleanup(func() { os.Remove(sockPath) })

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("fake sidecar listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					if _, err := ipc.ReadFrame(c); err != nil {
						return
					}
					if err := ipc.WriteFrame(c, []byte(`{"page":"color"}`)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return sockPath
}

// newTestEngine builds an Engine from the shared config-package testdata
// (internal/config/testdata/) without calling New's network/MIDI paths.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	eng, err := New("../config/testdata")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return eng
}

// stubSidecarLaunch replaces launchSidecarFn with a no-op success for the
// duration of the test, so Start() never discovers/spawns a real python3
// process. Every test that calls Start() must call this (mirrors the
// existing startMIDIFn stubbing pattern below). Tests that want to exercise
// the launch-failure path replace launchSidecarFn themselves instead.
func stubSidecarLaunch(t *testing.T) {
	t.Helper()
	orig := launchSidecarFn
	launchSidecarFn = func(string) (func(), string, error) {
		return func() {}, "", nil
	}
	t.Cleanup(func() { launchSidecarFn = orig })
}

// stubMIDI replaces startMIDIFn with a no-op for the duration of the test —
// bypasses real Loupedeck hardware. Returns the captured callback so tests
// can fire synthetic MIDI events through it.
func stubMIDI(t *testing.T) func(callback func(msg midi.Message, timestampms int32)) (func(), error) {
	t.Helper()
	orig := startMIDIFn
	t.Cleanup(func() { startMIDIFn = orig })
	noop := func(callback func(msg midi.Message, timestampms int32)) (func(), error) {
		return func() {}, nil
	}
	startMIDIFn = noop
	return noop
}

// stubMIDICapture replaces startMIDIFn so the registered callback is handed
// back to the caller (via the returned pointer) instead of being discarded —
// lets a test fire synthetic MIDI events as if real hardware produced them.
func stubMIDICapture(t *testing.T) *func(msg midi.Message, timestampms int32) {
	t.Helper()
	orig := startMIDIFn
	t.Cleanup(func() { startMIDIFn = orig })
	var captured func(msg midi.Message, timestampms int32)
	startMIDIFn = func(callback func(msg midi.Message, timestampms int32)) (func(), error) {
		captured = callback
		return func() {}, nil
	}
	return &captured
}

// ---------------------------------------------------------------------------
// pressResolution helpers (pure-Go; no CGO)
// ---------------------------------------------------------------------------

type pressKind int

const (
	pressKindDefaultWheelReset pressKind = iota
	pressKindDefaultCDLReset
	pressKindKey
	pressKindClick
	pressKindNone
	pressKindSelectWheel
	pressKindResetActiveWheel
)

func resolvePressKind(b config.Binding) pressKind {
	if b.Press != nil {
		switch b.Press.Type {
		case "key":
			return pressKindKey
		case "click":
			return pressKindClick
		case "none":
			return pressKindNone
		case "selectWheel":
			return pressKindSelectWheel
		}
	}
	switch {
	case b.IsWheel():
		return pressKindDefaultWheelReset
	case b.IsCDL():
		return pressKindDefaultCDLReset
	case b.IsResetActiveWheel():
		return pressKindResetActiveWheel
	}
	return pressKindNone
}

// ---------------------------------------------------------------------------
// TestPressDispatchResolution
// ---------------------------------------------------------------------------

func TestPressDispatchResolution(t *testing.T) {
	cases := []struct {
		name string
		b    config.Binding
		want pressKind
	}{
		{"wheel no press → default reset", config.Binding{Type: "wheel", Wheel: "lift_master"}, pressKindDefaultWheelReset},
		{"wheel press key", config.Binding{Type: "wheel", Wheel: "temp_slider",
			Press: &config.PressAction{Type: "key", Key: "Cmd+Z"}}, pressKindKey},
		{"wheel press none", config.Binding{Type: "wheel", Wheel: "tint_slider",
			Press: &config.PressAction{Type: "none"}}, pressKindNone},
		{"wheel press click", config.Binding{Type: "wheel", Wheel: "gain_master",
			Press: &config.PressAction{Type: "click", Point: "gain_reset"}}, pressKindClick},
		{"cdl no press → default cdl reset", config.Binding{Param: "slope", Ch: "master"}, pressKindDefaultCDLReset},
		{"cdl press key", config.Binding{Type: "cdl", Param: "offset", Ch: "r",
			Press: &config.PressAction{Type: "key", Key: "Ctrl+Z"}}, pressKindKey},
		{"cdl press none", config.Binding{Type: "cdl", Param: "power", Ch: "master",
			Press: &config.PressAction{Type: "none"}}, pressKindNone},
		{"method=wheel no press → default reset", config.Binding{Method: "wheel", Wheel: "gamma_master"}, pressKindDefaultWheelReset},
		{"wheel press selectWheel", config.Binding{Type: "wheel", Wheel: "lift_master",
			Press: &config.PressAction{Type: "selectWheel", Wheel: "lift"}}, pressKindSelectWheel},
		{"resetActiveWheel binding", config.Binding{Type: "resetActiveWheel"}, pressKindResetActiveWheel},
		{"balance binding → no press action", config.Binding{Type: "balance", Hue: 3}, pressKindNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePressKind(tc.b)
			if got != tc.want {
				t.Errorf("resolvePressKind(%+v) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestDeriveRequiredCalibPoints — v8: balance → 12 box positions
// ---------------------------------------------------------------------------

func TestDeriveRequiredCalibPoints_BalanceBindings_12Boxes(t *testing.T) {
	bindings := `{
		"col": {
			"P1": {"type": "balance", "hue": 0, "sens": 8},
			"P5": {"type": "balance", "hue": 4, "sens": 8},
			"Lift": {"type": "wheel", "wheel": "lift_master"}
		}
	}`
	cfg := loadConfig(t, bindings)
	points := DeriveRequiredCalibPoints(cfg)

	byKey := map[string]bool{}
	for _, p := range points {
		byKey[p.Key] = true
	}

	// Regular wheel calib still required.
	if !byKey["lift_master"] {
		t.Error("lift_master missing")
	}
	if !byKey["lift_reset"] {
		t.Error("lift_reset missing (default press)")
	}

	// All 12 balance box positions required.
	wheels := []string{"lift", "gamma", "gain", "offset"}
	channels := []string{"R", "G", "B"}
	for _, w := range wheels {
		for _, ch := range channels {
			k := w + "_" + ch
			if !byKey[k] {
				t.Errorf("expected balance box calib point %q in derived points", k)
			}
		}
	}

	// Old v7 balance points must NOT appear.
	for _, old := range []string{"balance_red", "lift_center", "gamma_center", "gain_center", "offset_center"} {
		if byKey[old] {
			t.Errorf("v7 calib point %q must NOT be derived in v8", old)
		}
	}
}

func TestDeriveRequiredCalibPoints_PressAware(t *testing.T) {
	bindings := `{
		"col": {
			"Lift": {"type": "wheel", "wheel": "lift_master"},
			"Temp": {"type": "wheel", "wheel": "temp_slider", "press": {"type": "key", "key": "Cmd+Z"}},
			"Tint": {"type": "wheel", "wheel": "tint_slider", "press": {"type": "none"}},
			"Gain": {"type": "wheel", "wheel": "gain_master", "press": {"type": "click", "point": "gain_reset"}}
		}
	}`
	cfg := loadConfig(t, bindings)
	points := DeriveRequiredCalibPoints(cfg)

	byKey := map[string]string{}
	for _, p := range points {
		byKey[p.Key] = p.Axis
	}

	if _, ok := byKey["lift_master"]; !ok {
		t.Error("lift_master missing")
	}
	if _, ok := byKey["lift_reset"]; !ok {
		t.Error("lift_reset missing (Lift has no press override)")
	}
	if _, ok := byKey["temp_slider"]; !ok {
		t.Error("temp_slider missing")
	}
	if _, ok := byKey["temp_slider_reset"]; ok {
		t.Error("temp_slider_reset should NOT be derived (Temp has press→key)")
	}
	if _, ok := byKey["tint_slider"]; !ok {
		t.Error("tint_slider missing")
	}
	if _, ok := byKey["tint_slider_reset"]; ok {
		t.Error("tint_slider_reset should NOT be derived (Tint has press→none)")
	}
	if _, ok := byKey["gain_master"]; !ok {
		t.Error("gain_master missing")
	}
	if _, ok := byKey["gain_reset"]; ok {
		t.Error("gain_reset should NOT be auto-derived (Gain has press→click)")
	}
}

func TestDeriveRequiredCalibPoints_SelectWheelNoNewPoints(t *testing.T) {
	bindings := `{
		"col": {
			"Exposure": {"type": "wheel", "wheel": "lift_master", "sens": 15,
				"press": {"type": "selectWheel", "wheel": "lift"}},
			"L1": {"type": "resetActiveWheel"}
		}
	}`
	cfg := loadConfig(t, bindings)
	points := DeriveRequiredCalibPoints(cfg)

	byKey := map[string]string{}
	for _, p := range points {
		byKey[p.Key] = p.Axis
	}

	if _, ok := byKey["lift_master"]; !ok {
		t.Error("lift_master missing")
	}
	if _, ok := byKey["lift_reset"]; ok {
		t.Error("lift_reset should not be derived (press→selectWheel overrides default)")
	}
	// No balance points.
	for _, k := range []string{"lift_R", "lift_G", "lift_B"} {
		if _, ok := byKey[k]; ok {
			t.Errorf("balance box %q should NOT be derived (no balance bindings)", k)
		}
	}
}

func TestDeriveRequiredCalibPoints_CDLNoPoints(t *testing.T) {
	bindings := `{
		"col": {
			"Sat":    {"param": "sat", "ch": "master"},
			"Slope":  {"type": "cdl", "param": "slope", "ch": "r", "press": {"type": "key", "key": "Cmd+Z"}},
			"Offset": {"type": "cdl", "param": "offset", "ch": "master", "press": {"type": "none"}}
		}
	}`
	cfg := loadConfig(t, bindings)
	points := DeriveRequiredCalibPoints(cfg)
	if len(points) != 0 {
		t.Errorf("CDL bindings should derive 0 calib points, got %d: %v", len(points), points)
	}
}

func TestDeriveRequiredCalibPoints_BackwardCompat(t *testing.T) {
	bindings := `{
		"col": {
			"Lift":  {"method": "wheel", "wheel": "lift_master"},
			"Gamma": {"type":   "wheel", "wheel": "gamma_master"}
		}
	}`
	cfg := loadConfig(t, bindings)
	points := DeriveRequiredCalibPoints(cfg)

	byKey := map[string]bool{}
	for _, p := range points {
		byKey[p.Key] = true
	}
	for _, k := range []string{"lift_master", "lift_reset", "gamma_master", "gamma_reset"} {
		if !byKey[k] {
			t.Errorf("expected %q in derived points (backward compat)", k)
		}
	}
}

// ---------------------------------------------------------------------------
// TestActiveWheelStateUpdate
// ---------------------------------------------------------------------------

func TestActiveWheelStateUpdate(t *testing.T) {
	aw := target.New()
	if got := aw.Get(); got != "lift" {
		t.Fatalf("initial wheel = %q, want lift", got)
	}
	for _, wheel := range []string{"gamma", "gain", "offset", "lift"} {
		aw.Set(wheel)
		if got := aw.Get(); got != wheel {
			t.Errorf("after Set(%q), Get() = %q", wheel, got)
		}
	}
	// Arbitrary target names are now ACCEPTED. internal/target was
	// generalised from activewheel when Col became a universal reset: the
	// package cannot know which targets _config.resetTargets defines, so an
	// unrecognised name is rejected at dispatch (logged SKIP) rather than
	// silently dropped here. Only the empty string is refused.
	aw.Set("gamma")
	aw.Set("crop")
	if got := aw.Get(); got != "crop" {
		t.Errorf("after Set(crop), Get() = %q, want crop", got)
	}
	aw.Set("")
	if got := aw.Get(); got != "crop" {
		t.Errorf("Set(\"\") must be ignored, Get() = %q, want crop", got)
	}
}

// ---------------------------------------------------------------------------
// Compile-time sanity: dispatchNoteOn signature
// ---------------------------------------------------------------------------
var _ = func() {
	var (
		b           config.Binding
		modeTracker *mode.Tracker
		cal         *calib.Calib
		client      *ipc.Client
		aw          *target.State
		cfg         *config.Config
	)
	_ = resolvePressKind(b)
	_ = modeTracker
	_ = cal
	_ = client
	_ = aw
	_ = cfg
}

// ---------------------------------------------------------------------------
// TestDodeeMostRegex
// ---------------------------------------------------------------------------

func TestDodeeMostRegex(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"2026-MOST-PRODUCTION-DODEE", true},
		{"DODEE EP02", true},
		{"dodee test", true},
		{"MOST PRODUCTION 2026", true},
		{"most production", true},
		{"Normal Project", false},
		{"My Color Grade", false},
		{"", false},
	}
	for _, tc := range cases {
		got := dodeeMostRe.MatchString(tc.title)
		if got != tc.want {
			t.Errorf("dodeeMostRe.MatchString(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestSaveSentinel — verify saveSentinel writes a JSON with lastShutdown
// ---------------------------------------------------------------------------

func TestSaveSentinel(t *testing.T) {
	tmp := t.TempDir() + "/test-sentinel.json"
	origPath := stateFilePath
	stateFilePath = tmp
	defer func() { stateFilePath = origPath }()

	saveSentinel("", "")

	b, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("sentinel file not written: %v", err)
	}
	var s sentinelState
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("sentinel JSON invalid: %v", err)
	}
	if s.LastShutdown == "" {
		t.Error("lastShutdown must not be empty")
	}
}

func TestSaveSentinel_PreservesLastExportFolder(t *testing.T) {
	tmp := t.TempDir() + "/test-sentinel.json"
	origPath := stateFilePath
	stateFilePath = tmp
	defer func() { stateFilePath = origPath }()

	saveSentinel("/some/folder", "")

	b, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("sentinel file not written: %v", err)
	}
	var s sentinelState
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("sentinel JSON invalid: %v", err)
	}
	if s.LastExportFolder != "/some/folder" {
		t.Errorf("LastExportFolder = %q, want /some/folder", s.LastExportFolder)
	}
}

// ---------------------------------------------------------------------------
// TestConfigDefaults_v8 — verify new config defaults are correct
// ---------------------------------------------------------------------------

func TestConfigDefaults_v8(t *testing.T) {
	bindings := `{"col": {}}`
	cfg := loadConfig(t, bindings)
	tl := cfg.TopLevel

	if tl.CoalesceWindowMs != 120 {
		t.Errorf("CoalesceWindowMs = %d, want 120", tl.CoalesceWindowMs)
	}
	if tl.FineFactor != 0.5 {
		t.Errorf("FineFactor = %.2f, want 0.5", tl.FineFactor)
	}
	if tl.StartupReset != "never" {
		t.Errorf("StartupReset = %q, want never", tl.StartupReset)
	}
	if tl.DodeeMostGuard == nil || !*tl.DodeeMostGuard {
		t.Error("DodeeMostGuard default should be true")
	}
	if tl.PxPerUnit != 10.0 {
		t.Errorf("PxPerUnit = %v, want 10.0", tl.PxPerUnit)
	}
	if tl.MaxScrubPx != 80.0 {
		t.Errorf("MaxScrubPx = %v, want 80.0", tl.MaxScrubPx)
	}
	if tl.MinScrubPx != 1.0 {
		t.Errorf("MinScrubPx = %v, want 1.0", tl.MinScrubPx)
	}

	want := map[string]float64{"lift": 1.0, "gamma": 1.0, "gain": 0.7, "offset": 0.5}
	for w, wantV := range want {
		if got := tl.WheelSensMultipliers[w]; math.Abs(got-wantV) > 1e-9 {
			t.Errorf("WheelSensMultipliers[%q] = %.2f, want %.2f", w, got, wantV)
		}
	}
}

func TestConfigDefaults_v8_Override(t *testing.T) {
	bindings := `{
		"_config": {
			"coalesceWindowMs": 80,
			"fineFactor": 0.25,
			"startupReset": "always",
			"dodeeMostGuard": false,
			"pxPerUnit": 15.0,
			"maxScrubPx": 60.0,
			"minScrubPx": 2.0,
			"wheelSensMultipliers": {"lift": 2.0, "gamma": 1.5, "gain": 1.0, "offset": 0.8}
		},
		"col": {}
	}`
	cfg := loadConfig(t, bindings)
	tl := cfg.TopLevel

	if tl.CoalesceWindowMs != 80 {
		t.Errorf("CoalesceWindowMs = %d, want 80", tl.CoalesceWindowMs)
	}
	if tl.FineFactor != 0.25 {
		t.Errorf("FineFactor = %.2f, want 0.25", tl.FineFactor)
	}
	if tl.StartupReset != "always" {
		t.Errorf("StartupReset = %q, want always", tl.StartupReset)
	}
	if tl.DodeeMostGuard == nil || *tl.DodeeMostGuard {
		t.Error("DodeeMostGuard should be false")
	}
	if tl.PxPerUnit != 15.0 {
		t.Errorf("PxPerUnit = %v, want 15.0", tl.PxPerUnit)
	}
	if tl.MaxScrubPx != 60.0 {
		t.Errorf("MaxScrubPx = %v, want 60.0", tl.MaxScrubPx)
	}
	if tl.MinScrubPx != 2.0 {
		t.Errorf("MinScrubPx = %v, want 2.0", tl.MinScrubPx)
	}
	if tl.WheelSensMultipliers["lift"] != 2.0 {
		t.Errorf("WheelSensMultipliers[lift] = %.2f, want 2.0", tl.WheelSensMultipliers["lift"])
	}
}

// ---------------------------------------------------------------------------
// TestIsNewBindingTypes
// ---------------------------------------------------------------------------

func TestIsNewBindingTypes(t *testing.T) {
	fmh := config.Binding{Type: "fineModeHold"}
	if !fmh.IsFineModeHold() {
		t.Error("IsFineModeHold() false for fineModeHold binding")
	}
	rsa := config.Binding{Type: "resyncAll"}
	if !rsa.IsResyncAll() {
		t.Error("IsResyncAll() false for resyncAll binding")
	}
	bal := config.Binding{Type: "balance"}
	if bal.IsFineModeHold() {
		t.Error("IsFineModeHold() true for balance binding")
	}
	raw := config.Binding{Type: "resetActiveWheel"}
	if raw.IsResyncAll() {
		t.Error("IsResyncAll() true for resetActiveWheel binding")
	}
}

// ---------------------------------------------------------------------------
// TestDispatchBalanceStep_RGBSequence
// Verifies R/G/B scrub sequence, correct magnitudes, per-wheel sens, and fine mode.
// ---------------------------------------------------------------------------

// scrubCall records one invocation of mouseScrubFn.
type scrubCall struct {
	x, y, deltaPx float64
}

func TestDispatchBalanceStep_RGBSequence(t *testing.T) {
	// Build a calibration with lift_R, lift_G, lift_B boxes.
	cal := minimalCalib(t, map[string][2]float64{
		"lift_R": {100, 200},
		"lift_G": {200, 200},
		"lift_B": {300, 200},
	})

	// hue 0° (red): R=+1, G=-0.5, B=-0.5
	b := config.Binding{Type: "balance", Hue: 0, Sens: 1.0}
	tl := config.TopLevel{
		HueOffsetsDeg:        [8]float64{0, 45, 105, 165, 225, 270, 300, 330},
		WheelSensMultipliers: map[string]float64{"lift": 1.0, "gamma": 1.0, "gain": 0.7, "offset": 0.5},
		FineFactor:           0.5,
		PxPerUnit:            10.0,
		MaxScrubPx:           80.0,
		MinScrubPx:           1.0,
	}
	aw := target.New() // defaults to "lift"

	var calls []scrubCall
	origScrub := mouseScrubFn
	defer func() { mouseScrubFn = origScrub }()
	mouseScrubFn = func(x, y, delta float64) {
		calls = append(calls, scrubCall{x, y, delta})
	}

	// step=10, sens=1.0, wheelMult=1.0 → perTick=10
	// R: 10 * 1.0 * 10 = 100 → clamped to 80
	// G: 10 * (-0.5) * 10 = -50 → |-50| > minScrubPx → scrub
	// B: same as G → -50
	dispatchBalanceStep("P1", 10, b, true, true, cal, aw, tl, false)

	if len(calls) != 3 {
		t.Fatalf("expected 3 scrub calls (R, G, B), got %d: %v", len(calls), calls)
	}

	const eps = 0.01
	// R: clamped to 80
	if !approxEqF64(calls[0].x, 100, eps) || !approxEqF64(calls[0].y, 200, eps) {
		t.Errorf("R box: got (%.1f,%.1f) want (100,200)", calls[0].x, calls[0].y)
	}
	if !approxEqF64(calls[0].deltaPx, 80.0, eps) {
		t.Errorf("R delta_px = %.2f, want 80.0 (clamped)", calls[0].deltaPx)
	}

	// G: 10 * -0.5 * 10 = -50
	if !approxEqF64(calls[1].x, 200, eps) || !approxEqF64(calls[1].y, 200, eps) {
		t.Errorf("G box: got (%.1f,%.1f) want (200,200)", calls[1].x, calls[1].y)
	}
	if !approxEqF64(calls[1].deltaPx, -50.0, eps) {
		t.Errorf("G delta_px = %.2f, want -50.0", calls[1].deltaPx)
	}

	// B: same magnitude as G
	if !approxEqF64(calls[2].x, 300, eps) || !approxEqF64(calls[2].y, 200, eps) {
		t.Errorf("B box: got (%.1f,%.1f) want (300,200)", calls[2].x, calls[2].y)
	}
	if !approxEqF64(calls[2].deltaPx, -50.0, eps) {
		t.Errorf("B delta_px = %.2f, want -50.0", calls[2].deltaPx)
	}
}

func TestDispatchBalanceStep_FineMode(t *testing.T) {
	cal := minimalCalib(t, map[string][2]float64{
		"lift_R": {100, 200},
		"lift_G": {200, 200},
		"lift_B": {300, 200},
	})
	b := config.Binding{Type: "balance", Hue: 0, Sens: 1.0}
	tl := config.TopLevel{
		HueOffsetsDeg:        [8]float64{0},
		WheelSensMultipliers: map[string]float64{"lift": 1.0, "gamma": 1.0, "gain": 0.7, "offset": 0.5},
		FineFactor:           0.5,
		PxPerUnit:            10.0,
		MaxScrubPx:           80.0,
		MinScrubPx:           1.0,
	}
	aw := target.New()

	var normalCalls, fineCalls []scrubCall
	origScrub := mouseScrubFn
	defer func() { mouseScrubFn = origScrub }()

	mouseScrubFn = func(x, y, delta float64) {
		normalCalls = append(normalCalls, scrubCall{x, y, delta})
	}
	dispatchBalanceStep("P1", 5, b, true, true, cal, aw, tl, false)

	mouseScrubFn = func(x, y, delta float64) {
		fineCalls = append(fineCalls, scrubCall{x, y, delta})
	}
	dispatchBalanceStep("P1", 5, b, true, true, cal, aw, tl, true)

	if len(normalCalls) == 0 || len(fineCalls) == 0 {
		t.Fatal("expected scrub calls in both modes")
	}
	// Fine mode applies FineFactor=0.5, so R channel delta should be half.
	if normalCalls[0].deltaPx == 0 {
		t.Fatal("normal R delta is zero — degenerate test")
	}
	ratio := fineCalls[0].deltaPx / normalCalls[0].deltaPx
	if math.Abs(ratio-0.5) > 0.05 {
		t.Errorf("fine mode ratio = %.3f, want ~0.5 (FineFactor=0.5)", ratio)
	}
}

func TestDispatchBalanceStep_PerWheelMultiplier(t *testing.T) {
	// lift (mult=1.0) vs offset (mult=0.5) should produce half the delta.
	cal := minimalCalib(t, map[string][2]float64{
		"lift_R": {100, 200}, "lift_G": {200, 200}, "lift_B": {300, 200},
		"offset_R": {100, 300}, "offset_G": {200, 300}, "offset_B": {300, 300},
	})
	b := config.Binding{Type: "balance", Hue: 0, Sens: 1.0}
	tl := config.TopLevel{
		HueOffsetsDeg:        [8]float64{0},
		WheelSensMultipliers: map[string]float64{"lift": 1.0, "gamma": 1.0, "gain": 0.7, "offset": 0.5},
		FineFactor:           0.5,
		PxPerUnit:            10.0,
		MaxScrubPx:           80.0,
		MinScrubPx:           1.0,
	}

	awLift := target.New()
	awOffset := target.New()
	awOffset.Set("offset")

	var liftCalls, offsetCalls []scrubCall
	origScrub := mouseScrubFn
	defer func() { mouseScrubFn = origScrub }()

	mouseScrubFn = func(x, y, delta float64) {
		liftCalls = append(liftCalls, scrubCall{x, y, delta})
	}
	dispatchBalanceStep("P1", 5, b, true, true, cal, awLift, tl, false)

	mouseScrubFn = func(x, y, delta float64) {
		offsetCalls = append(offsetCalls, scrubCall{x, y, delta})
	}
	dispatchBalanceStep("P1", 5, b, true, true, cal, awOffset, tl, false)

	if len(liftCalls) == 0 || len(offsetCalls) == 0 {
		t.Fatal("expected scrub calls for both wheels")
	}
	if liftCalls[0].deltaPx == 0 {
		t.Fatal("lift R delta is zero — degenerate test")
	}
	ratio := offsetCalls[0].deltaPx / liftCalls[0].deltaPx
	if math.Abs(ratio-0.5) > 0.05 {
		t.Errorf("offset/lift ratio = %.3f, want ~0.5 (multiplier=0.5)", ratio)
	}
}

func TestDispatchBalanceStep_GateSkip(t *testing.T) {
	cal := minimalCalib(t, map[string][2]float64{
		"lift_R": {100, 200}, "lift_G": {200, 200}, "lift_B": {300, 200},
	})
	b := config.Binding{Type: "balance", Hue: 0, Sens: 1.0}
	tl := config.TopLevel{
		HueOffsetsDeg:        [8]float64{0},
		WheelSensMultipliers: map[string]float64{"lift": 1.0},
		PxPerUnit:            10.0, MaxScrubPx: 80.0, MinScrubPx: 1.0,
	}
	aw := target.New()

	var calls []scrubCall
	origScrub := mouseScrubFn
	defer func() { mouseScrubFn = origScrub }()
	mouseScrubFn = func(x, y, delta float64) {
		calls = append(calls, scrubCall{x, y, delta})
	}

	// allowed=false → no scrubs
	dispatchBalanceStep("P1", 5, b, false, true, cal, aw, tl, false)
	if len(calls) != 0 {
		t.Errorf("gate=false: expected 0 scrubs, got %d", len(calls))
	}

	// modeOK=false → no scrubs
	dispatchBalanceStep("P1", 5, b, true, false, cal, aw, tl, false)
	if len(calls) != 0 {
		t.Errorf("mode=false: expected 0 scrubs, got %d", len(calls))
	}
}

func TestDispatchBalanceStep_MinScrubSkip(t *testing.T) {
	// At hue=0°: R=+1, G=-0.5, B=-0.5.
	// With tiny step, G and B may be below minScrubPx; if so they are skipped.
	// We use a minScrubPx that is larger than |G component| * small perTick.
	cal := minimalCalib(t, map[string][2]float64{
		"lift_R": {100, 200}, "lift_G": {200, 200}, "lift_B": {300, 200},
	})
	b := config.Binding{Type: "balance", Hue: 0, Sens: 0.1} // very low sens
	tl := config.TopLevel{
		HueOffsetsDeg:        [8]float64{0},
		WheelSensMultipliers: map[string]float64{"lift": 1.0},
		PxPerUnit:            1.0,
		MaxScrubPx:           80.0,
		MinScrubPx:           1.0, // R component (0.1*1*1=0.1) < 1.0 → all skipped
	}
	aw := target.New()

	var calls []scrubCall
	origScrub := mouseScrubFn
	defer func() { mouseScrubFn = origScrub }()
	mouseScrubFn = func(x, y, delta float64) {
		calls = append(calls, scrubCall{x, y, delta})
	}

	// step=1, sens=0.1, wheelMult=1.0, pxPerUnit=1.0 → perTick=0.1
	// R: 0.1 * 1.0 * 1.0 = 0.1 < minScrubPx=1.0 → skip
	// G/B: smaller → skip
	dispatchBalanceStep("P1", 1, b, true, true, cal, aw, tl, false)
	if len(calls) != 0 {
		t.Errorf("all channels below minScrubPx: expected 0 scrubs, got %d", len(calls))
	}
}

// ---------------------------------------------------------------------------
// TestExecuteResetWheel_DoubleClicksRGB
// Verifies that executeResetWheel issues 3 sequential double-clicks on R/G/B boxes.
// ---------------------------------------------------------------------------

func TestExecuteResetWheel_DoubleClicksRGB(t *testing.T) {
	cal := minimalCalib(t, map[string][2]float64{
		"lift_R": {10, 20},
		"lift_G": {30, 20},
		"lift_B": {50, 20},
	})

	var doubleClicks [][2]float64
	origDC := mouseDoubleClickFn
	defer func() { mouseDoubleClickFn = origDC }()
	mouseDoubleClickFn = func(x, y float64) {
		doubleClicks = append(doubleClicks, [2]float64{x, y})
	}

	ok := executeResetWheel("lift", cal)
	if !ok {
		t.Error("executeResetWheel returned false, want true")
	}
	if len(doubleClicks) != 3 {
		t.Fatalf("expected 3 double-clicks, got %d: %v", len(doubleClicks), doubleClicks)
	}
	// Order: R, G, B.
	wantClicks := [][2]float64{{10, 20}, {30, 20}, {50, 20}}
	for i, want := range wantClicks {
		if doubleClicks[i][0] != want[0] || doubleClicks[i][1] != want[1] {
			t.Errorf("double-click[%d] = (%.1f,%.1f), want (%.1f,%.1f)",
				i, doubleClicks[i][0], doubleClicks[i][1], want[0], want[1])
		}
	}
}

func TestExecuteResetWheel_MissingCalib(t *testing.T) {
	cal := minimalCalib(t, map[string][2]float64{}) // empty
	ok := executeResetWheel("lift", cal)
	if ok {
		t.Error("executeResetWheel should return false for missing calib point")
	}
}

func TestExecuteResetWheel_NilCalib(t *testing.T) {
	ok := executeResetWheel("lift", nil)
	if ok {
		t.Error("executeResetWheel should return false for nil calib")
	}
}

// ---------------------------------------------------------------------------
// TestResyncAll_12DoubleClicks
// Verifies that IsResyncAll triggers 12 double-clicks (4 wheels × 3 channels).
// We invoke executeResetWheel for each wheel to mirror dispatchNoteOn's resyncAll branch.
// ---------------------------------------------------------------------------

func TestResyncAll_12DoubleClicks(t *testing.T) {
	pts := map[string][2]float64{}
	wheels := []string{"lift", "gamma", "gain", "offset"}
	for i, w := range wheels {
		for j, ch := range []string{"R", "G", "B"} {
			pts[w+"_"+ch] = [2]float64{float64(i * 10), float64(j * 10)}
		}
	}
	cal := minimalCalib(t, pts)

	var doubleClicks [][2]float64
	origDC := mouseDoubleClickFn
	defer func() { mouseDoubleClickFn = origDC }()
	mouseDoubleClickFn = func(x, y float64) {
		doubleClicks = append(doubleClicks, [2]float64{x, y})
	}

	for i, wheel := range wheels {
		if i > 0 {
			time.Sleep(1 * time.Millisecond) // accelerate for test
		}
		executeResetWheel(wheel, cal)
	}

	if len(doubleClicks) != 12 {
		t.Errorf("resyncAll: expected 12 double-clicks, got %d", len(doubleClicks))
	}
}

// ---------------------------------------------------------------------------
// Engine lifecycle: New
// ---------------------------------------------------------------------------

func TestNew_LoadsTestdataConfigs(t *testing.T) {
	// Reuses the shared internal/config/testdata/ fixtures (device-map.json +
	// bindings.json). No calibration.json exists there, so cal must come back
	// nil — matching the pre-refactor CLI's "suppressed until --calibrate" path.
	eng := newTestEngine(t)
	if eng.cfg.Load() == nil {
		t.Fatal("New: cfg not loaded")
	}
	if eng.cal != nil {
		t.Error("New: expected cal to be nil (no calibration.json in testdata)")
	}
	if eng.modeTracker == nil {
		t.Error("New: modeTracker not initialized")
	}
	if eng.aw == nil {
		t.Error("New: aw (active wheel) not initialized")
	}
	if eng.aw.Get() != "lift" {
		t.Errorf("New: aw default = %q, want lift", eng.aw.Get())
	}
	if eng.sockPath == "" {
		t.Error("New: sockPath not defaulted")
	}
}

func TestNew_MissingConfig_ReturnsError(t *testing.T) {
	_, err := New(t.TempDir()) // empty dir: no device-map.json / bindings.json
	if err == nil {
		t.Fatal("New: expected error for missing config files, got nil")
	}
}

// ---------------------------------------------------------------------------
// Engine lifecycle: Start / Stop
// ---------------------------------------------------------------------------

func TestStartStop_NoGoroutineLeak(t *testing.T) {
	eng := newTestEngine(t)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	stubMIDI(t) // bypass real Loupedeck hardware

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !eng.Status().Running {
		t.Error("expected Status().Running=true after Start")
	}
	if !eng.Status().SidecarConnected {
		t.Error("expected Status().SidecarConnected=true after Start (fake sidecar dialed)")
	}

	if err := eng.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if eng.Status().Running {
		t.Error("expected Status().Running=false after Stop")
	}
	if eng.Status().SidecarConnected {
		t.Error("expected Status().SidecarConnected=false after Stop")
	}

	// Stop() joins on e.done before returning, so by now the drain-loop
	// goroutine must have exited and closed the channel — no leak.
	select {
	case <-eng.done:
		// closed — goroutine exited cleanly.
	default:
		t.Error("engine drain-loop goroutine did not exit (done channel not closed)")
	}
}

func TestStart_AlreadyRunning_ReturnsError(t *testing.T) {
	eng := newTestEngine(t)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	stubMIDI(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	if err := eng.Start(context.Background()); err == nil {
		t.Error("expected error calling Start twice while running, got nil")
	}
}

func TestStop_NeverStarted_NoOp(t *testing.T) {
	eng := newTestEngine(t)
	if err := eng.Stop(); err != nil {
		t.Errorf("Stop on never-started engine: expected nil, got %v", err)
	}
}

// TestStart_NoSidecar_DegradesGracefully replaces the old
// TestStart_NoSidecar_ReturnsError: Task 2 changed the contract so a
// sidecar launch/dial failure no longer aborts Start() (the Wails UI needs
// to show a live Status window with a banner, not have Start() itself
// fail). The engine still starts — Running=true, SidecarConnected=false,
// SidecarError populated — and the MIDI drain loop still runs (grading is
// suppressed via the client==nil guards, same posture as a missing
// calibration.json).
func TestStart_NoSidecar_DegradesGracefully(t *testing.T) {
	eng := newTestEngine(t)
	eng.sockPath = t.TempDir() + "/no-such-sidecar.sock" // nothing listening
	stubSidecarLaunch(t)                                 // launch itself "succeeds" (no-op reuse)…
	stubMIDI(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v, want nil (sidecar failure must not abort Start)", err)
	}
	defer eng.Stop()

	got := eng.Status()
	if !got.Running {
		t.Error("Status().Running = false, want true (engine keeps running without a sidecar)")
	}
	if got.SidecarConnected {
		t.Error("Status().SidecarConnected = true, want false (dial to a dead socket)")
	}
	if got.SidecarError == "" {
		t.Error("Status().SidecarError is empty, want a human-readable message")
	}
}

// TestStart_SidecarLaunchFails_DegradesGracefully covers the other half:
// launchSidecarFn itself returns an error (e.g. DiscoverPython3 failure) —
// same graceful-degradation contract, and the message must surface the
// launch error specifically (not just a generic dial failure).
func TestStart_SidecarLaunchFails_DegradesGracefully(t *testing.T) {
	eng := newTestEngine(t)
	eng.sockPath = t.TempDir() + "/no-such-sidecar.sock"

	wantErr := "python3 not found (simulated)"
	orig := launchSidecarFn
	launchSidecarFn = func(string) (func(), string, error) {
		return nil, "", errors.New(wantErr)
	}
	t.Cleanup(func() { launchSidecarFn = orig })
	stubMIDI(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v, want nil", err)
	}
	defer eng.Stop()

	got := eng.Status()
	if got.SidecarConnected {
		t.Error("Status().SidecarConnected = true, want false")
	}
	if !strings.Contains(got.SidecarError, wantErr) {
		t.Errorf("Status().SidecarError = %q, want it to contain %q", got.SidecarError, wantErr)
	}
}

func TestStart_ParentContextCancel_StopsEngine(t *testing.T) {
	eng := newTestEngine(t)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	stubMIDI(t)

	ctx, cancel := context.WithCancel(context.Background())
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cancel() // cancel the PARENT context passed into Start

	select {
	case <-eng.done:
		// drain-loop exited because runCtx (derived from ctx) was cancelled.
	case <-time.After(2 * time.Second):
		t.Fatal("drain-loop goroutine did not exit after parent context cancel")
	}
}

// ---------------------------------------------------------------------------
// Engine lifecycle: Status reflects injected state
// ---------------------------------------------------------------------------

func TestStatus_ReflectsInjectedState(t *testing.T) {
	eng := newTestEngine(t)

	eng.mu.Lock()
	eng.status.Running = true
	eng.status.SidecarConnected = true
	eng.status.ResolveFrontmost = true
	eng.status.Page = "color"
	eng.status.GateAllow = true
	eng.status.ActiveWheel = "gamma"
	eng.status.Mode = "log"
	eng.status.Project = "2026-TEST-PROJECT"
	eng.status.LastMIDI = `cc=36 val=1 control="Knob1" step=1`
	eng.mu.Unlock()

	got := eng.Status()

	if !got.Running {
		t.Error("Status().Running = false, want true")
	}
	if !got.SidecarConnected {
		t.Error("Status().SidecarConnected = false, want true")
	}
	if !got.ResolveFrontmost {
		t.Error("Status().ResolveFrontmost = false, want true")
	}
	if got.Page != "color" {
		t.Errorf("Status().Page = %q, want color", got.Page)
	}
	if !got.GateAllow {
		t.Error("Status().GateAllow = false, want true")
	}
	if got.ActiveWheel != "gamma" {
		t.Errorf("Status().ActiveWheel = %q, want gamma", got.ActiveWheel)
	}
	if got.Mode != "log" {
		t.Errorf("Status().Mode = %q, want log", got.Mode)
	}
	if got.Project != "2026-TEST-PROJECT" {
		t.Errorf("Status().Project = %q, want 2026-TEST-PROJECT", got.Project)
	}
	if got.LastMIDI == "" {
		t.Error("Status().LastMIDI is empty, want injected value")
	}
	// AccessibilityOK is probed live from macapp.AccessibilityTrusted() — just
	// confirm Status() doesn't panic touching it; value is environment-dependent.
	_ = got.AccessibilityOK
	// Phase 1: ScreenRecordingOK has no CGO check yet — must default false.
	if got.ScreenRecordingOK {
		t.Error("Status().ScreenRecordingOK = true, want false (Phase 1 stub)")
	}
}

func TestStatus_SetLastMIDI(t *testing.T) {
	eng := newTestEngine(t)
	eng.setLastMIDI("cc=36 val=1")
	if got := eng.Status().LastMIDI; got != "cc=36 val=1" {
		t.Errorf("Status().LastMIDI = %q, want %q", got, "cc=36 val=1")
	}
}

// ---------------------------------------------------------------------------
// Part B: data-race regression coverage (statusCache, bank, router.node)
// ---------------------------------------------------------------------------

// TestConcurrentEvents_StatusPolling_Race is the -race regression test for
// the two data races Task 1 flagged and Task 2 fixed:
//   - the statusCache closure var in queryStatus() (read/written from both
//     the MIDI-callback goroutine, via dispatchNoteOn -> dispatchPressAction
//     -> queryPage, and the ticker/drain goroutine's per-tick snapshot).
//   - bank (fixed here with bankMu) and router.Router.node (fixed in
//     internal/router with its own mutex) — bank is written by the "Col"
//     hardwired nav press and read by the ticker/drain goroutine's binding
//     resolution; node is written by the "Right" hardwired nav press
//     (r.SetNode) and read by router.Route from the drain goroutine's CDL
//     dispatch.
//
// It fires synthetic MIDI events (knob turns + Col/Right nav presses)
// through the real callback registered via startMIDIFn, exactly as a real
// Loupedeck's single delivery goroutine would, while concurrently polling
// Status() from a second goroutine — the shape a future Wails UI's
// status-polling goroutine will create — with the engine's own real 50ms
// ticker goroutine running throughout. Must pass under `go test -race`.
func TestConcurrentEvents_StatusPolling_Race(t *testing.T) {
	eng := newTestEngine(t)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	if cb == nil {
		t.Fatal("MIDI callback was not captured")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	done := make(chan struct{})

	// Goroutine 1: synthetic MIDI events, delivered sequentially as a real
	// Loupedeck driver goroutine would — knob turns (CC 36 = "Saturation",
	// a CDL binding: drives the coalesce accumulator, and on the drain tick
	// touches bank + router.Route -> node) interleaved with hardwired nav
	// presses ("Col" note 65 writes bank; "Right" note 79 writes node via
	// r.SetNode and calls client.Send directly). Events are spread out with
	// a small per-iteration sleep so firing genuinely OVERLAPS several
	// 50ms ticker cycles — a burst-then-wait pattern would let the ticker
	// drain everything only after this goroutine already exited, which
	// never actually races (no temporal overlap) even without the fix.
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < 200; i++ {
			cb(midi.ControlChange(0, 36, 1), 0)
			if i%5 == 0 {
				cb(midi.NoteOn(0, 65, 100), 0) // Col: writes bank
			}
			if i%3 == 0 {
				cb(midi.NoteOn(0, 79, 100), 0) // Right: writes node via r.SetNode
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Goroutine 2: concurrent Status() reads for the same duration — mirrors
	// a future Wails status-polling goroutine hitting Status() while events
	// are live.
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				_ = eng.Status()
				time.Sleep(time.Millisecond)
			}
		}
	}()

	wg.Wait()

	if !eng.Status().Running {
		t.Error("expected engine still running after concurrent event storm")
	}
}
