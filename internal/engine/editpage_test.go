package engine

// Tests for Phase 2 (Edit-page, page-aware) dispatch:
//   - processNonBalanceDrain's new "property" and "jog" branches
//   - page-aware bank resolution (col vs edit) in the drain loop
//   - the C5 Quick Export button: dispatchQuickExport + dispatchNoteOn's
//     async IsQuickExport() case + last-folder persistence
//
// All hermetic: no live Resolve, no real folder-picker dialog, no real
// CGEvent/keys, DECKPLUS_ARMED never set.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/ipc"
	"2t5-deck-plus/internal/mode"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newEngineWithConfig builds an *Engine from an explicit device-map.json +
// bindings.json pair (unlike newTestEngine, which is pinned to the shared
// internal/config/testdata fixtures) — needed here so tests can define
// both a "col" and an "edit" binding for the SAME control name to prove
// page-aware bank resolution actually picks the right one.
func newEngineWithConfig(t *testing.T, deviceMapJSON, bindingsJSON string) *Engine {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/device-map.json", []byte(deviceMapJSON), 0644); err != nil {
		t.Fatalf("write device-map.json: %v", err)
	}
	if err := os.WriteFile(dir+"/bindings.json", []byte(bindingsJSON), 0644); err != nil {
		t.Fatalf("write bindings.json: %v", err)
	}
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return eng
}

// startRecordingSidecar spins up a fake unix-socket sidecar that records
// every received command (decoded JSON) and replies {"page": page} to
// "status" ops and {"ok": true} to everything else. Lets tests both drive
// Start()'s page-aware bank resolution AND assert exactly which sidecar
// op a dispatch produced.
//
// "status" and "restore_highlight" are excluded from the recorded list --
// both are orthogonal engine-lifecycle housekeeping (status is a
// recurring background poll; restore_highlight is Start()'s unconditional
// D1-highlight crash-safety call, see engine.go) that would otherwise
// pollute index-0 assertions in tests that only care about the ONE
// command their own MIDI event produced.
func startRecordingSidecar(t *testing.T, page string) (sockPath string, received func() []map[string]any) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-rec-*.sock")
	if err != nil {
		t.Fatalf("tempname: %v", err)
	}
	sockPath = f.Name()
	f.Close()
	os.Remove(sockPath)
	t.Cleanup(func() { os.Remove(sockPath) })

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	var recs []map[string]any

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					body, err := ipc.ReadFrame(c)
					if err != nil {
						return
					}
					var m map[string]any
					_ = json.Unmarshal(body, &m)

					if op, _ := m["op"].(string); op != "status" && op != "restore_highlight" {
						mu.Lock()
						recs = append(recs, m)
						mu.Unlock()
					}

					var reply []byte
					if op, _ := m["op"].(string); op == "status" {
						reply, _ = json.Marshal(map[string]any{"ok": true, "page": page})
					} else {
						reply, _ = json.Marshal(map[string]any{"ok": true})
					}
					if err := ipc.WriteFrame(c, reply); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	return sockPath, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := make([]map[string]any, len(recs))
		copy(out, recs)
		return out
	}
}

// dialRecordingClient connects a real *ipc.Client to a startRecordingSidecar
// socket, for direct (non-Start()) unit tests of processNonBalanceDrain.
func dialRecordingClient(t *testing.T, sockPath string) *ipc.Client {
	t.Helper()
	c, err := ipc.Dial(sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func floatPtr(f float64) *float64 { return &f }

// ---------------------------------------------------------------------------
// processNonBalanceDrain — "property" branch
// ---------------------------------------------------------------------------

func TestProcessNonBalanceDrain_Property_Single(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "property", Property: "Pan", Sens: 2.0}
	processNonBalanceDrain("Contrast", 3, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "bump_property" {
		t.Fatalf("op = %v, want bump_property", recs[0]["op"])
	}
	if recs[0]["property"] != "Pan" {
		t.Errorf("property = %v, want Pan", recs[0]["property"])
	}
	if _, hasGang := recs[0]["gang"]; hasGang {
		t.Errorf("gang key should be absent for single-property binding, got %v", recs[0]["gang"])
	}
	if got := recs[0]["delta"].(float64); got != 6.0 {
		t.Errorf("delta = %v, want 6.0 (step=3 * sens=2.0)", got)
	}
}

func TestProcessNonBalanceDrain_Property_Gang(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "property", Gang: []string{"ZoomX", "ZoomY"}, Sens: 0.01}
	processNonBalanceDrain("Exposure", 4, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "bump_property" {
		t.Fatalf("op = %v, want bump_property", recs[0]["op"])
	}
	if _, hasProp := recs[0]["property"]; hasProp {
		t.Errorf("property key should be absent for gang binding, got %v", recs[0]["property"])
	}
	gang, ok := recs[0]["gang"].([]any)
	if !ok || len(gang) != 2 || gang[0] != "ZoomX" || gang[1] != "ZoomY" {
		t.Errorf("gang = %v, want [ZoomX ZoomY]", recs[0]["gang"])
	}
	const eps = 1e-9
	if got := recs[0]["delta"].(float64); got < 0.04-eps || got > 0.04+eps {
		t.Errorf("delta = %v, want 0.04 (step=4 * sens=0.01)", got)
	}
}

func TestProcessNonBalanceDrain_Property_MinMaxPassthrough(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "property", Property: "CropTop", Sens: 1.0, Min: floatPtr(0), Max: floatPtr(100)}
	processNonBalanceDrain("Temperature", 1, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["min"].(float64); got != 0 {
		t.Errorf("min = %v, want 0", got)
	}
	if got := recs[0]["max"].(float64); got != 100 {
		t.Errorf("max = %v, want 100", got)
	}
}

func TestProcessNonBalanceDrain_Property_NoMinMax_KeysAbsent(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "property", Property: "Tilt", Sens: 1.0}
	processNonBalanceDrain("Clarity", 1, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if _, ok := recs[0]["min"]; ok {
		t.Errorf("min key should be absent, got %v", recs[0]["min"])
	}
	if _, ok := recs[0]["max"]; ok {
		t.Errorf("max key should be absent, got %v", recs[0]["max"])
	}
}

func TestProcessNonBalanceDrain_Property_MaxDeltaClamp(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "property", Property: "Pan", Sens: 100.0, MaxDelta: 40.0}
	processNonBalanceDrain("Contrast", 5, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	// rawDelta = 5*100 = 500, clamped to 40.
	if got := recs[0]["delta"].(float64); got != 40.0 {
		t.Errorf("delta = %v, want 40.0 (clamped)", got)
	}
}

func TestProcessNonBalanceDrain_Property_DefaultSensWhenZero(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "property", Property: "Pan"} // Sens omitted (zero)
	processNonBalanceDrain("Contrast", 3, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["delta"].(float64); got != 3.0*defaultPropertySens {
		t.Errorf("delta = %v, want %v (default sens)", got, 3.0*defaultPropertySens)
	}
}

func TestProcessNonBalanceDrain_Property_NilClient_NoPanic(t *testing.T) {
	b := config.Binding{Type: "property", Property: "Pan", Sens: 1.0}
	// Must not panic with a nil client.
	processNonBalanceDrain("Contrast", 3, b, mode.New(), nil, nil, nil, "edit", nil, func() string { return "edit" }, 0, 0)
}

// ---------------------------------------------------------------------------
// processNonBalanceDrain — "jog" branch
// ---------------------------------------------------------------------------

func TestProcessNonBalanceDrain_Jog_SendsAcceleratedFrames(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"}
	processNonBalanceDrain("ControlDial", 1, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog" {
		t.Fatalf("op = %v, want jog", recs[0]["op"])
	}
	if got := recs[0]["frames"].(float64); got != 1 {
		t.Errorf("frames = %v, want 1 (slow single-step turn)", got)
	}
}

func TestProcessNonBalanceDrain_Jog_MaxDeltaClamp(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog", MaxDelta: 5}
	// step=3 stays in the FINE tier (below the default snap5 threshold of
	// 4 -- see TestJogTier_DefaultThresholds in internal/tc) and
	// accelerates to well over 5 frames; must clamp to 5. (Formerly
	// step=20, moved here to stay in the fine tier now that fast spins
	// snap instead of producing a large frame jump -- see the Snap*Tier
	// tests below for that behavior.)
	processNonBalanceDrain("ControlDial", 3, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog" {
		t.Fatalf("op = %v, want jog", recs[0]["op"])
	}
	if got := recs[0]["frames"].(float64); got != 5 {
		t.Errorf("frames = %v, want 5 (clamped)", got)
	}
	if _, has := recs[0]["snap_seconds"]; has {
		t.Errorf("fine tier must not send snap_seconds, got %v", recs[0])
	}
}

func TestProcessNonBalanceDrain_Jog_NegativeStep_NegativeFrames(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"}
	processNonBalanceDrain("ControlDial", -1, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["frames"].(float64); got != -1 {
		t.Errorf("frames = %v, want -1", got)
	}
}

func TestProcessNonBalanceDrain_Jog_NilClient_NoPanic(t *testing.T) {
	b := config.Binding{Type: "jog"}
	processNonBalanceDrain("ControlDial", 5, b, mode.New(), nil, nil, nil, "edit", nil, func() string { return "edit" }, 0, 0)
}

// ---------------------------------------------------------------------------
// processNonBalanceDrain — "jog" branch, speed-tiered snap (fast dial)
// ---------------------------------------------------------------------------

func TestProcessNonBalanceDrain_Jog_Snap5Tier_SendsSnapSecondsAndDir(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"} // default thresholds: t5Min=4
	processNonBalanceDrain("ControlDial", 4, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog" {
		t.Fatalf("op = %v, want jog", recs[0]["op"])
	}
	if got := recs[0]["snap_seconds"].(float64); got != 5 {
		t.Errorf("snap_seconds = %v, want 5", got)
	}
	if got := recs[0]["dir"].(float64); got != 1 {
		t.Errorf("dir = %v, want 1 (forward)", got)
	}
}

func TestProcessNonBalanceDrain_Jog_Snap10Tier(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"} // default thresholds: t10Min=8
	processNonBalanceDrain("ControlDial", 9, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["snap_seconds"].(float64); got != 10 {
		t.Errorf("snap_seconds = %v, want 10", got)
	}
	if got := recs[0]["dir"].(float64); got != 1 {
		t.Errorf("dir = %v, want 1", got)
	}
}

func TestProcessNonBalanceDrain_Jog_Snap30Tier(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"} // default thresholds: t30Min=13
	processNonBalanceDrain("ControlDial", 15, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["snap_seconds"].(float64); got != 30 {
		t.Errorf("snap_seconds = %v, want 30", got)
	}
	if got := recs[0]["dir"].(float64); got != 1 {
		t.Errorf("dir = %v, want 1", got)
	}
}

func TestProcessNonBalanceDrain_Jog_Snap_NegativeStep_DirNegative(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"}
	processNonBalanceDrain("ControlDial", -20, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["snap_seconds"].(float64); got != 30 {
		t.Errorf("snap_seconds = %v, want 30 (tier decided by |step|)", got)
	}
	if got := recs[0]["dir"].(float64); got != -1 {
		t.Errorf("dir = %v, want -1 (backward)", got)
	}
}

// TestProcessNonBalanceDrain_Jog_Snap_IncludesFallbackFrames proves the
// snap-tier command ALSO carries a "frames" value (computed the same way
// as the fine tier, via tc.AccelerateWith) alongside snap_seconds/dir --
// the sidecar's jog_snap falls back to this "frames" delta if the live
// timeline turns out to be drop-frame or non-integer-fps (unknowable from
// the Go side, which never touches the live Resolve Timeline). See
// resolve_driver.py's jog_snap()/handle()'s "jog" dispatch.
func TestProcessNonBalanceDrain_Jog_Snap_IncludesFallbackFrames(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"}
	processNonBalanceDrain("ControlDial", 4, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if _, has := recs[0]["frames"]; !has {
		t.Errorf("snap-tier jog command should still include a fallback frames value, got %v", recs[0])
	}
}

func TestProcessNonBalanceDrain_Jog_Snap_CustomThresholdsFromBinding(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	// Custom thresholds: t5Min=2 -- step=2 would be FINE under defaults
	// (t5Min=4) but snaps to 5s here because the binding overrides it.
	b := config.Binding{Type: "jog", JogTier5Min: 2, JogTier10Min: 5, JogTier30Min: 10}
	processNonBalanceDrain("ControlDial", 2, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["snap_seconds"].(float64); got != 5 {
		t.Errorf("snap_seconds = %v, want 5 (custom JogTier5Min=2 threshold)", got)
	}
}

func TestProcessNonBalanceDrain_Jog_FineTier_JustBelowSnapThreshold(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"} // default t5Min=4
	processNonBalanceDrain("ControlDial", 3, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if _, has := recs[0]["snap_seconds"]; has {
		t.Errorf("step=3 should stay in the fine tier (default t5Min=4), got snap_seconds=%v", recs[0]["snap_seconds"])
	}
	if _, has := recs[0]["frames"]; !has {
		t.Errorf("fine tier must send a frames value, got %v", recs[0])
	}
}

// ---------------------------------------------------------------------------
// processNonBalanceDrain — "jog" branch, sustained-spin escalation
// (sustainSnapSeconds param, computed by the caller from
// internal/tc.JogStreak/SustainSnapSeconds -- direct-call tests here only
// exercise the OVERRIDE wiring itself, not the streak/duration math, which
// is covered exhaustively by internal/tc's own pure tests).
// ---------------------------------------------------------------------------

// TestProcessNonBalanceDrain_Jog_SustainOverride_ForcesConfiguredTier proves
// a nonzero sustainSnapSeconds wins over whatever tc.JogTier would have
// picked from the tick's own |step| speed -- step=4 alone would normally
// land in the default 5s tier, but a sustained-spin override of 60 must
// take priority.
func TestProcessNonBalanceDrain_Jog_SustainOverride_ForcesConfiguredTier(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"} // default thresholds: t5Min=4 -> step=4 alone would be the 5s tier
	processNonBalanceDrain("ControlDial", 4, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 60)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog" {
		t.Fatalf("op = %v, want jog", recs[0]["op"])
	}
	if got := recs[0]["snap_seconds"].(float64); got != 60 {
		t.Errorf("snap_seconds = %v, want 60 (sustain override must win over the 5s speed tier)", got)
	}
}

// TestProcessNonBalanceDrain_Jog_SustainOverride_EscalatesEvenFineTierSpeed
// proves the escalation applies even when the tick's OWN speed would have
// stayed in the fine (frame-by-frame) tier -- a slow, steady sustained
// dial must still snap to the sustain tier, not silently stay fine-tier
// forever just because each individual tick is slow.
func TestProcessNonBalanceDrain_Jog_SustainOverride_EscalatesEvenFineTierSpeed(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"} // default t5Min=4; step=1 alone is deep in the FINE tier
	processNonBalanceDrain("ControlDial", 1, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 60)

	waitForRecs(t, received, 1)
	recs := received()
	if got, has := recs[0]["snap_seconds"]; !has || got.(float64) != 60 {
		t.Errorf("snap_seconds = %v (present=%v), want 60 (sustain override escalates even a fine-tier-speed tick)", got, has)
	}
}

// TestProcessNonBalanceDrain_Jog_SustainZero_UsesNormalSpeedTier_ByteIdentical
// proves sustainSnapSeconds==0 (the value every pre-existing test in this
// file now passes) is a complete no-op -- byte-identical to the behavior
// before this parameter existed.
func TestProcessNonBalanceDrain_Jog_SustainZero_UsesNormalSpeedTier_ByteIdentical(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"}
	processNonBalanceDrain("ControlDial", 4, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["snap_seconds"].(float64); got != 5 {
		t.Errorf("snap_seconds = %v, want 5 (sustainSnapSeconds=0 must defer entirely to JogTier)", got)
	}
}

// TestProcessNonBalanceDrain_Jog_SustainOverride_CustomTier proves the
// override value itself is just forwarded verbatim (not hardcoded to 60)
// -- the caller (Start()'s drain loop) is what resolves the configured
// jogSustainSeconds via tc.SustainSnapSeconds before calling in.
func TestProcessNonBalanceDrain_Jog_SustainOverride_CustomTier(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"}
	processNonBalanceDrain("ControlDial", 4, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 45)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["snap_seconds"].(float64); got != 45 {
		t.Errorf("snap_seconds = %v, want 45 (verbatim override value)", got)
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration -- sustained-spin escalation.
// The unit tests above only prove processNonBalanceDrain honors an
// ALREADY-RESOLVED override value; these prove the drain loop actually
// tracks the streak across real ticks and resolves that value itself.
// ---------------------------------------------------------------------------

// TestDrainLoop_ControlDial_SustainedSpin_EscalatesToConfiguredTier fires a
// small step (deep in the default fine tier on its own) once per real
// drain tick, repeatedly, for longer than a shortened jogSustainMs --
// proving the continuous streak escalates to the configured
// jogSustainSeconds tier even though no single tick's own speed would
// ever have crossed a JogTier threshold.
func TestDrainLoop_ControlDial_SustainedSpin_EscalatesToConfiguredTier(t *testing.T) {
	deviceMap := `{"knobs":{"48":"ControlDial"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"ControlDial": {"type": "jog", "maxDelta": 90, "jogSustainMs": 150, "jogSustainSeconds": 77}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	// Fire a step every 10ms -- much FASTER than the 50ms drain tick, so
	// (unlike a real Loupedeck's physical dial, which emits many raw CC
	// ticks per rotation) every single 50ms tick sees at least one
	// coalesced step, with no "empty" tick in between to reset the
	// streak. This is what "dialled continuously" means for the streak
	// detector: a tick with NO step at all is a pause (see
	// internal/tc.JogStreak's doc comment) -- a synthetic test that fired
	// events SLOWER than one per tick would spuriously reset the streak
	// on every intervening empty tick and never escalate.
	deadline := time.Now().Add(3 * time.Second)
	var sawSustainTier bool
	for time.Now().Before(deadline) {
		cb(midi.ControlChange(0, 48, 1), 0) // step=+1, deep in the fine tier alone
		time.Sleep(10 * time.Millisecond)
		for _, r := range received() {
			if r["op"] != "jog" {
				continue
			}
			if ss, ok := r["snap_seconds"]; ok && int(ss.(float64)) == 77 {
				sawSustainTier = true
			}
		}
		if sawSustainTier {
			break
		}
	}
	if !sawSustainTier {
		t.Fatalf("never saw a jog op with the configured sustain tier (snap_seconds=77) after sustained spin, received=%v", received())
	}
}

// TestDrainLoop_ControlDial_SingleSpin_StaysOnSpeedTier_NotSustain proves a
// single one-off fast spin (well below jogSustainMs, this binding's is
// 1000ms) resolves via the normal JogTier speed tier, NOT the sustain
// override -- guards against an over-eager streak implementation that
// escalates on the very first tick.
func TestDrainLoop_ControlDial_SingleSpin_StaysOnSpeedTier_NotSustain(t *testing.T) {
	deviceMap := `{"knobs":{"48":"ControlDial"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"ControlDial": {"type": "jog", "maxDelta": 90, "jogSustainMs": 1000, "jogSustainSeconds": 77}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 48, 15), 0) // one big step (>= default t30Min=13) -> 30s speed tier

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog" {
		t.Fatalf("op = %v, want jog", recs[0]["op"])
	}
	ss, hasSnap := recs[0]["snap_seconds"]
	if !hasSnap {
		t.Fatalf("expected a snap_seconds tier for a single fast spin, got %v", recs[0])
	}
	if got := int(ss.(float64)); got != 30 {
		t.Errorf("snap_seconds = %d, want 30 (default speed tier for |step|=15) -- must NOT be 77 (the sustain override, unreachable on tick 1)", got)
	}
}

// waitForRecs polls received() until it has at least n entries or times out.
func waitForRecs(t *testing.T, received func() []map[string]any, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(received()) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d recorded command(s), got %d: %v", n, len(received()), received())
}

// ---------------------------------------------------------------------------
// Page-aware bank resolution (col vs edit) — full Start() integration
// ---------------------------------------------------------------------------

func TestPageAwareBankResolution_EditPageUsesEditBank(t *testing.T) {
	deviceMap := `{"knobs":{"36":"Contrast"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col":  {"Contrast": {"type": "cdl", "param": "offset", "ch": "master"}},
		"edit": {"Contrast": {"type": "property", "property": "Pan", "sens": 2.0}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 36, 3), 0) // Contrast, step +3

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "bump_property" {
		t.Fatalf("page=edit dispatched op=%v, want bump_property (edit bank should win over col)", recs[0]["op"])
	}
	if recs[0]["property"] != "Pan" {
		t.Errorf("property = %v, want Pan", recs[0]["property"])
	}
}

func TestPageAwareBankResolution_ColorPageUsesColBank_ByteIdentical(t *testing.T) {
	deviceMap := `{"knobs":{"36":"Contrast"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col":  {"Contrast": {"type": "cdl", "param": "offset", "ch": "master"}},
		"edit": {"Contrast": {"type": "property", "property": "Pan", "sens": 2.0}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "color")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 36, 3), 0)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "delta" {
		t.Fatalf("page=color dispatched op=%v, want delta (col/CDL bank unaffected by edit-page addition)", recs[0]["op"])
	}
}

// ---------------------------------------------------------------------------
// resolveNoteBank — pure-function unit tests (fast, no Start()/sidecar)
// ---------------------------------------------------------------------------

func TestResolveNoteBank(t *testing.T) {
	bindings := `{
		"col": {
			"Copy":     {"type": "key", "key": "Cmd+C"},
			"Contrast": {"type": "cdl", "param": "offset", "ch": "master"},
			"C2":       {"type": "key", "key": "Alt+P"}
		},
		"edit": {
			"Contrast":    {"type": "property", "property": "Pan", "sens": 2.0},
			"ControlDial": {"type": "jog", "maxDelta": 30},
			"C2":          {"type": "key", "key": "Cmd+B"}
		}
	}`
	cfg := loadConfig(t, bindings)

	cases := []struct {
		name    string
		page    string
		control string
		want    string
	}{
		{"edit page, property control shadows to edit bank", "edit", "Contrast", "edit"},
		{"edit page, jog control shadows to edit bank", "edit", "ControlDial", "edit"},
		{"edit page, key control shadows to edit bank", "edit", "C2", "edit"},
		{"edit page, button absent from edit bank falls back to col", "edit", "Copy", "col"},
		{"edit page, control absent from both banks falls back to col", "edit", "DoesNotExist", "col"},
		{"color page, control with an edit-bank binding still resolves col", "color", "Contrast", "col"},
		{"color page, key control with an edit-bank binding still resolves col", "color", "C2", "col"},
		{"cut page, button resolves col", "cut", "Copy", "col"},
		{"empty page, button resolves col", "", "Copy", "col"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveNoteBank(cfg, tc.page, 1, "col", tc.control); got != tc.want {
				t.Errorf("resolveNoteBank(page=%q, control=%q) = %q, want %q", tc.page, tc.control, got, tc.want)
			}
		})
	}
}

// TestResolveNoteBankHonoursLayer is resolveNoteBank's own pure-function
// layer-awareness guard, complementing TestNoteOnHonoursLiveLayer's
// end-to-end Start() coverage above resolveNoteBank's doc comment.
func TestResolveNoteBankHonoursLayer(t *testing.T) {
	bindings := `{
		"col":   {"C1": {"type":"key","key":"Alt+S"}},
		"col-2": {"C1": {"type":"key","key":"Alt+X"}}
	}`
	cfg := loadConfig(t, bindings)

	if got := resolveNoteBank(cfg, "color", 1, "col", "C1"); got != "col" {
		t.Errorf("layer 1: resolveNoteBank = %q, want col", got)
	}
	if got := resolveNoteBank(cfg, "color", 2, "col", "C1"); got != "col-2" {
		t.Errorf("layer 2: resolveNoteBank = %q, want col-2", got)
	}
}

// ---------------------------------------------------------------------------
// Page-aware bank resolution — note_on (knob-click / button-press) —
// full Start() integration, mirroring the drain-path tests above.
// ---------------------------------------------------------------------------

// TestNoteOnPageAware_EditPage_PropertyKnobClick_SendsResetProperty proves
// the FIX-1 bug report: on the Edit page, clicking a property-bound knob
// must reset that property via the sidecar, not fall through to whatever
// col-bank press action (e.g. a Cmd+Z key) the SAME control name has on
// the Color page.
func TestNoteOnPageAware_EditPage_PropertyKnobClick_SendsResetProperty(t *testing.T) {
	deviceMap := `{"knobs":{"38":"Temperature"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col":  {"Temperature": {"type": "wheel", "wheel": "temp_slider", "press": {"type": "key", "key": "Cmd+Z"}}},
		"edit": {"Temperature": {"type": "property", "property": "CropTop", "sens": 2.0}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 38, 100), 0) // Temperature knob click

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "reset_property" {
		t.Fatalf("op = %v, want reset_property", recs[0]["op"])
	}
	if recs[0]["property"] != "CropTop" {
		t.Errorf("property = %v, want CropTop", recs[0]["property"])
	}
}

func TestNoteOnPageAware_EditPage_PropertyGangKnobClick_SendsResetPropertyGang(t *testing.T) {
	deviceMap := `{"knobs":{"33":"Exposure"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col":  {"Exposure": {"type": "wheel", "wheel": "lift_master", "sens": 15}},
		"edit": {"Exposure": {"type": "property", "gang": ["ZoomX", "ZoomY"], "sens": 0.01}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 33, 100), 0)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "reset_property" {
		t.Fatalf("op = %v, want reset_property", recs[0]["op"])
	}
	if _, hasProp := recs[0]["property"]; hasProp {
		t.Errorf("property key should be absent for gang binding, got %v", recs[0]["property"])
	}
	gang, ok := recs[0]["gang"].([]any)
	if !ok || len(gang) != 2 || gang[0] != "ZoomX" || gang[1] != "ZoomY" {
		t.Errorf("gang = %v, want [ZoomX ZoomY]", recs[0]["gang"])
	}
}

// TestNoteOnPageAware_EditPage_JogKnobClick_NoSidecarOp proves the
// ControlDial (jog) knob press is a harmless no-op — dispatchNoteOn's
// switch has no case for IsJog(), so the press is simply not dispatched
// (no CGEvent, no sidecar call, no panic).
func TestNoteOnPageAware_EditPage_JogKnobClick_NoSidecarOp(t *testing.T) {
	deviceMap := `{"knobs":{"48":"ControlDial"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col":  {},
		"edit": {"ControlDial": {"type": "jog", "maxDelta": 30}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 48, 100), 0) // ControlDial press

	time.Sleep(150 * time.Millisecond) // give any (incorrect) send a chance to land
	if len(received()) != 0 {
		t.Errorf("jog knob click must be a no-op, got sidecar commands: %v", received())
	}
}

// TestNoteOnPageAware_EditPage_ButtonNotInEditBank_FallsBackToColBank is
// the regression test for the exact bug found in live testing: a blind
// "page==edit => bank=edit" shadow (rather than resolveNoteBank's
// property/jog-gated shadow) makes cfg.Binding("edit", control) miss for
// every button that only exists in the col bank (Copy/Paste/Undo/Redo/
// C5/C6/...) and silently drops the press. This proves such a button
// still dispatches its normal col-bank action while the Edit page shows.
func TestNoteOnPageAware_EditPage_ButtonNotInEditBank_FallsBackToColBank(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"92":"QuickBtn"}}`
	bindings := `{
		"col":  {"QuickBtn": {"type": "cdl", "param": "offset", "ch": "master"}},
		"edit": {"ControlDial": {"type": "jog", "maxDelta": 30}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 92, 100), 0) // QuickBtn press, col-bank-only control

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "reset_param" {
		t.Fatalf("page=edit, control absent from edit bank: op = %v, want reset_param (col bank must still fire)", recs[0]["op"])
	}
}

// TestNoteOnPageAware_ColorPage_UsesColBank_ByteIdentical proves the
// non-edit-page path is completely unaffected by the edit bank's
// existence — same control name bound differently in each bank, Color
// page must resolve the col (CDL) binding exactly as before Phase 2.
func TestNoteOnPageAware_ColorPage_UsesColBank_ByteIdentical(t *testing.T) {
	deviceMap := `{"knobs":{"36":"Contrast"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col":  {"Contrast": {"type": "cdl", "param": "offset", "ch": "master"}},
		"edit": {"Contrast": {"type": "property", "property": "Pan", "sens": 2.0}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "color")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 36, 100), 0)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "reset_param" {
		t.Fatalf("page=color dispatched op=%v, want reset_param (col/CDL bank unaffected by edit-page addition)", recs[0]["op"])
	}
}

// TestNoteOnPageAware_EditPage_KeyKnobClick_ShadowsToEditBank proves the
// Batch 1 fix (resolveNoteBank widened to also shadow IsKey() bindings):
// a control bound as "key" in the edit bank must shadow a DIFFERENT
// col-bank binding for the same control name on the Edit page (C2:
// col=Alt+P add-parallel-node, edit=Cmd+B Blade/Razor, in the real
// config). C2's "key" dispatch itself is unobservable under `go test`
// (IsKey() SKIPs before keys.Send() because Resolve is never frontmost
// in a test process -- see TestNoteOnAddMarker_ColorPage_ColBankKeyUnchanged's
// doc comment in marker_test.go for the same constraint), so this test
// uses a col-bank binding type (cdl) that DOES send an observable
// sidecar op as the distinguishing signal: if resolveNoteBank
// incorrectly fell back to col (the pre-fix bug this widen fixes), the
// cdl reset_param op would be sent even on the Edit page -- this test
// fails loudly in that case instead of silently proving nothing.
func TestNoteOnPageAware_EditPage_KeyKnobClick_ShadowsToEditBank(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"52":"C2"}}`
	bindings := `{
		"col":  {"C2": {"type": "cdl", "param": "offset", "ch": "master"}},
		"edit": {"C2": {"type": "key", "key": "Cmd+B"}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 52, 100), 0) // C2 press on the Edit page

	time.Sleep(150 * time.Millisecond) // give any (incorrect) col-bank send a chance to land
	for _, r := range received() {
		if r["op"] == "reset_param" {
			t.Errorf("edit-bank key binding must shadow the col-bank cdl binding on the Edit page (resolveNoteBank must route to edit, not fall back to col); got sidecar ops: %v", received())
		}
	}
}

// TestNoteOnPageAware_ColorPage_KeyEditBinding_ColBankCdlStillFires is the
// counterpart, same control/bindings pair on the Color page: proves the
// col-bank cdl binding still fires completely unaffected by C2 also
// having an edit-bank key binding, mirroring
// TestNoteOnPageAware_ColorPage_UsesColBank_ByteIdentical's shape above.
func TestNoteOnPageAware_ColorPage_KeyEditBinding_ColBankCdlStillFires(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"52":"C2"}}`
	bindings := `{
		"col":  {"C2": {"type": "cdl", "param": "offset", "ch": "master"}},
		"edit": {"C2": {"type": "key", "key": "Cmd+B"}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "color")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 52, 100), 0) // C2 press on the Color page

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "reset_param" {
		t.Fatalf("page=color dispatched op=%v, want reset_param (col/CDL bank unaffected by C2's edit-page key binding)", recs[0]["op"])
	}
}

// ---------------------------------------------------------------------------
// dispatchQuickExport
// ---------------------------------------------------------------------------

func TestDispatchQuickExport_UsesRememberedFolder_NoPicker(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	pickerCalled := false
	chooseFolder := func() (string, error) {
		pickerCalled = true
		return "/should/not/be/used", nil
	}
	var remembered string
	setLastFolder := func(f string) { remembered = f }
	getLastFolder := func() string { return "/remembered/folder" }

	dispatchQuickExport(client, chooseFolder, getLastFolder, setLastFolder)

	if pickerCalled {
		t.Error("picker should NOT be called when a folder is already remembered")
	}
	if remembered != "" {
		t.Error("setLastFolder should not be called when using the already-remembered folder")
	}
	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "render" {
		t.Fatalf("op = %v, want render", recs[0]["op"])
	}
	if recs[0]["target_dir"] != "/remembered/folder" {
		t.Errorf("target_dir = %v, want /remembered/folder", recs[0]["target_dir"])
	}
}

func TestDispatchQuickExport_PicksAndRemembersOnFirstUse(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	chooseFolder := func() (string, error) { return "/picked/folder", nil }
	var remembered string
	setLastFolder := func(f string) { remembered = f }
	getLastFolder := func() string { return "" } // unset

	dispatchQuickExport(client, chooseFolder, getLastFolder, setLastFolder)

	if remembered != "/picked/folder" {
		t.Errorf("remembered = %q, want /picked/folder", remembered)
	}
	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["target_dir"] != "/picked/folder" {
		t.Errorf("target_dir = %v, want /picked/folder", recs[0]["target_dir"])
	}
}

func TestDispatchQuickExport_PickerCancelled_NoRenderNoRemember(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	chooseFolder := func() (string, error) { return "", nil } // cancelled: empty, no error
	remembered := false
	setLastFolder := func(f string) { remembered = true }
	getLastFolder := func() string { return "" }

	dispatchQuickExport(client, chooseFolder, getLastFolder, setLastFolder)

	if remembered {
		t.Error("setLastFolder must not be called when the picker is cancelled")
	}
	time.Sleep(100 * time.Millisecond) // give any (incorrect) send a chance to land
	if len(received()) != 0 {
		t.Errorf("expected no render sent when picker cancelled, got %v", received())
	}
}

func TestDispatchQuickExport_PickerError_NoRenderNoPanic(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	chooseFolder := func() (string, error) { return "", errors.New("picker failed") }
	setLastFolder := func(f string) { t.Error("setLastFolder must not be called on picker error") }
	getLastFolder := func() string { return "" }

	dispatchQuickExport(client, chooseFolder, getLastFolder, setLastFolder)

	time.Sleep(100 * time.Millisecond)
	if len(received()) != 0 {
		t.Errorf("expected no render sent on picker error, got %v", received())
	}
}

func TestDispatchQuickExport_NilClient_NoPanic(t *testing.T) {
	getLastFolder := func() string { return "/some/folder" }
	setLastFolder := func(f string) {}
	chooseFolder := func() (string, error) { return "", nil }
	// Must not panic with a nil client, even though a folder is remembered.
	dispatchQuickExport(nil, chooseFolder, getLastFolder, setLastFolder)
}

// ---------------------------------------------------------------------------
// dispatchNoteOn's IsQuickExport() case — must dispatch ASYNCHRONOUSLY
// (never block the calling goroutine, which in production is the
// MIDI-callback goroutine).
// ---------------------------------------------------------------------------

func TestDispatchNoteOn_QuickExport_RunsAsync_DoesNotBlock(t *testing.T) {
	pickerStarted := make(chan struct{})
	pickerRelease := make(chan struct{})
	chooseFolder := func() (string, error) {
		close(pickerStarted)
		<-pickerRelease // blocks until the test releases it
		return "/picked", nil
	}
	getLastFolder := func() string { return "" } // unset -> triggers picker
	setLastFolder := func(string) {}

	b := config.Binding{Type: "quickexport"}

	done := make(chan struct{})
	go func() {
		dispatchNoteOn("C5", b, mode.New(), nil, nil, 1, 0, func() string { return "edit" }, nil, nil,
			chooseFolder, getLastFolder, setLastFolder, nil)
		close(done)
	}()

	select {
	case <-done:
		// Good: dispatchNoteOn returned without waiting for the picker.
	case <-time.After(1 * time.Second):
		t.Fatal("dispatchNoteOn(quickexport) blocked on the folder picker — must run async")
	}

	select {
	case <-pickerStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("picker was never invoked")
	}
	close(pickerRelease) // let the background goroutine finish, avoid leak
}

// ---------------------------------------------------------------------------
// Last-folder persistence (sentinelState)
// ---------------------------------------------------------------------------

func TestEngine_LastFolder_DefaultEmpty(t *testing.T) {
	tmp := t.TempDir() + "/no-such-sentinel.json"
	orig := stateFilePath
	stateFilePath = tmp
	defer func() { stateFilePath = orig }()

	eng := newTestEngine(t)
	if got := eng.getLastFolder(); got != "" {
		t.Errorf("getLastFolder() = %q, want empty when no sentinel exists", got)
	}
}

func TestEngine_SetLastFolder_PersistsAcrossNew(t *testing.T) {
	tmp := t.TempDir() + "/sentinel.json"
	orig := stateFilePath
	stateFilePath = tmp
	defer func() { stateFilePath = orig }()

	eng1 := newTestEngine(t)
	eng1.setLastFolder("/export/folder")

	if got := eng1.getLastFolder(); got != "/export/folder" {
		t.Errorf("getLastFolder() = %q, want /export/folder", got)
	}

	// A freshly-constructed Engine must reload the persisted folder.
	eng2 := newTestEngine(t)
	if got := eng2.getLastFolder(); got != "/export/folder" {
		t.Errorf("second Engine getLastFolder() = %q, want /export/folder (persisted)", got)
	}
}

func TestEngine_SetLastFolder_WritesSentinelJSON(t *testing.T) {
	tmp := t.TempDir() + "/sentinel.json"
	orig := stateFilePath
	stateFilePath = tmp
	defer func() { stateFilePath = orig }()

	eng := newTestEngine(t)
	eng.setLastFolder("/export/folder")

	b, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("sentinel file not written: %v", err)
	}
	var s sentinelState
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("sentinel JSON invalid: %v", err)
	}
	if s.LastExportFolder != "/export/folder" {
		t.Errorf("sentinel LastExportFolder = %q, want /export/folder", s.LastExportFolder)
	}
}
