package engine

// Tests for the C1 "add marker at playhead" feature: adds a Resolve
// marker at the current playhead position on the Edit page via the
// sidecar's add_marker op. Mirrors the D1 selectClip-release feature's
// test shape (selectclip_test.go's TestIsSelectClipRelease/
// TestDispatchSelectClipRelease*/TestNoteOnD1Release*):
//   - isAddMarkerControl (pure predicate: page + binding type check)
//   - dispatchAddMarker (the actual action: default-filling + sidecar send)
//   - full Start()/MIDI drain-loop integration via note_on, including the
//     regression proving the Color page's col-bank C1 (Alt+S) is untouched.
//
// All hermetic: no live Resolve, no real CGEvent/keys, DECKPLUS_ARMED
// never set.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/ipc"
)

// ---------------------------------------------------------------------------
// isAddMarkerControl — pure-function unit tests (fast, no Start()/sidecar)
// ---------------------------------------------------------------------------

func TestIsAddMarkerControl(t *testing.T) {
	cfg := loadConfig(t, `{
		"col": {
			"C1": {"type": "key", "key": "Alt+S"}
		},
		"edit": {
			"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"},
			"Contrast": {"type": "property", "property": "Pan"}
		}
	}`)
	cases := []struct {
		name    string
		page    string
		control string
		want    bool
	}{
		{"edit page, C1 addMarker binding", "edit", "C1", true},
		{"edit page, property control is NOT addMarker", "edit", "Contrast", false},
		{"edit page, control absent from edit bank", "edit", "Copy", false},
		{"color page, C1 present in edit bank but wrong page", "color", "C1", false},
		{"cut page, C1", "cut", "C1", false},
		{"empty page, C1", "", "C1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isAddMarkerControl(cfg, c.page, 1, c.control, ""); got != c.want {
				t.Errorf("isAddMarkerControl(page=%q, control=%q) = %v, want %v", c.page, c.control, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// dispatchAddMarker — direct unit tests (fake sidecar, no Start())
// ---------------------------------------------------------------------------

// recByOp returns the first recorded sidecar call with the given op.
//
// dispatchAddMarker now probes marker_at_playhead BEFORE creating (the C1
// "adopt an existing marker" behaviour), so the add_marker record is no
// longer recs[0]. Selecting by op keeps these tests robust against further
// probes being added ahead of the create.
func recByOp(t *testing.T, recs []map[string]any, op string) map[string]any {
	t.Helper()
	for _, r := range recs {
		if r["op"] == op {
			return r
		}
	}
	t.Fatalf("no %q record found in %v", op, recs)
	return nil
}

func TestDispatchAddMarker_SendsAddMarkerWithConfiguredDurationAndColor(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "addMarker", MarkerDurationSeconds: 1, MarkerColor: "Blue"}
	dispatchAddMarker(client, "C1", b, 0, noopSetActiveMarker, func() int { return -1 }, func() {}, func(int, int) {})

	waitForRecs(t, received, 2)
	recs := []map[string]any{recByOp(t, received(), "add_marker")}
	if got := recs[0]["duration_seconds"].(float64); got != 1.0 {
		t.Errorf("duration_seconds = %v, want 1.0", got)
	}
	if recs[0]["color"] != "Blue" {
		t.Errorf("color = %v, want Blue", recs[0]["color"])
	}
	if _, hasName := recs[0]["name"]; hasName {
		t.Errorf("name key should be absent when MarkerName is empty, got %v", recs[0]["name"])
	}
}

func TestDispatchAddMarker_DefaultsWhenFieldsZero(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "addMarker"} // MarkerDurationSeconds/MarkerColor omitted (zero)
	dispatchAddMarker(client, "C1", b, 0, noopSetActiveMarker, func() int { return -1 }, func() {}, func(int, int) {})

	waitForRecs(t, received, 2)
	recs := []map[string]any{recByOp(t, received(), "add_marker")}
	if got := recs[0]["duration_seconds"].(float64); got != defaultMarkerDurationSeconds {
		t.Errorf("duration_seconds = %v, want default %v", got, defaultMarkerDurationSeconds)
	}
	if recs[0]["color"] != defaultMarkerColor {
		t.Errorf("color = %v, want default %v", recs[0]["color"], defaultMarkerColor)
	}
}

func TestDispatchAddMarker_PassesConfiguredMarkerName(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "addMarker", MarkerDurationSeconds: 2, MarkerColor: "Red", MarkerName: "Beat"}
	dispatchAddMarker(client, "C1", b, 0, noopSetActiveMarker, func() int { return -1 }, func() {}, func(int, int) {})

	waitForRecs(t, received, 2)
	recs := []map[string]any{recByOp(t, received(), "add_marker")}
	if recs[0]["name"] != "Beat" {
		t.Errorf("name = %v, want Beat", recs[0]["name"])
	}
	if got := recs[0]["duration_seconds"].(float64); got != 2.0 {
		t.Errorf("duration_seconds = %v, want 2.0", got)
	}
	if recs[0]["color"] != "Red" {
		t.Errorf("color = %v, want Red", recs[0]["color"])
	}
}

func TestDispatchAddMarker_NilClient_NoPanic(t *testing.T) {
	b := config.Binding{Type: "addMarker", MarkerDurationSeconds: 1, MarkerColor: "Blue"}
	// Must not panic with a nil client.
	dispatchAddMarker(nil, "C1", b, 0, noopSetActiveMarker, func() int { return -1 }, func() {}, func(int, int) {})
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration via note_on.
// ---------------------------------------------------------------------------

// TestNoteOnAddMarker_EditPage_SendsAddMarker proves the feature end to
// end: C1 (note 49, matching the real device-map.json) pressed on the
// Edit page sends the sidecar's add_marker op with the binding's
// configured duration/color.
func TestNoteOnAddMarker_EditPage_SendsAddMarker(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"49":"C1"}}`
	bindings := `{
		"col":  {"C1": {"type": "key", "key": "Alt+S"}},
		"edit": {"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"}}
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
	cb(midi.NoteOn(0, 49, 100), 0) // C1 press

	waitForRecs(t, received, 2)
	recs := []map[string]any{recByOp(t, received(), "add_marker")}
	if got := recs[0]["duration_seconds"].(float64); got != 1.0 {
		t.Errorf("duration_seconds = %v, want 1.0", got)
	}
	if recs[0]["color"] != "Blue" {
		t.Errorf("color = %v, want Blue", recs[0]["color"])
	}
}

// TestNoteOnAddMarker_ColorPage_ColBankKeyUnchanged is the regression
// test: on the Color page, C1 must keep firing its existing col-bank
// binding (type "key", Alt+S) completely unaffected by the new Edit-page
// addMarker binding existing on the SAME control name -- no add_marker
// (or any other sidecar op) may ever be sent for a Color-page C1 press.
// The col-bank "key" binding's actual CGEvent (keys.Send) is itself
// gated behind macapp.BundleIDIsResolve(FrontmostBundleID()), which is
// false in this test process (Resolve is never frontmost under `go
// test`) -- so dispatchNoteOn's IsKey() case SKIPs before calling
// keys.Send, and the only externally observable assertion available
// here is "no sidecar command was ever produced", which is exactly the
// bug this feature could have introduced if isAddMarkerControl/
// dispatchAddMarker were wired incorrectly (e.g. checked against page
// alone, without the edit-bank-binding-type guard).
func TestNoteOnAddMarker_ColorPage_ColBankKeyUnchanged(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"49":"C1"}}`
	bindings := `{
		"col":  {"C1": {"type": "key", "key": "Alt+S"}},
		"edit": {"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"}}
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
	cb(midi.NoteOn(0, 49, 100), 0) // C1 press on Color page

	time.Sleep(150 * time.Millisecond) // give any (incorrect) send a chance to land
	for _, r := range received() {
		if r["op"] == "add_marker" {
			t.Errorf("add_marker must never be sent on the Color page, got %v", received())
		}
	}
}

// TestNoteOnAddMarker_EditPage_ButtonWithNoEditBinding_FallsBackToColBank
// mirrors TestNoteOnPageAware_EditPage_ButtonNotInEditBank_FallsBackToColBank
// -- proves a col-bank-only button (no edit-bank counterpart at all,
// unlike C1) still dispatches normally on the Edit page, unaffected by
// isAddMarkerControl's new intercept ahead of resolveNoteBank.
func TestNoteOnAddMarker_EditPage_ButtonWithNoEditBinding_FallsBackToColBank(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"92":"QuickBtn"}}`
	bindings := `{
		"col":  {"QuickBtn": {"type": "cdl", "param": "offset", "ch": "master"}},
		"edit": {"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"}}
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
		t.Fatalf("op = %v, want reset_param (col bank must still fire, unaffected by the addMarker intercept)", recs[0]["op"])
	}
}

// ---------------------------------------------------------------------------
// Feature 1: Hue-gated marker P-buttons -- markerAtPlayhead binding
// (isMarkerAtPlayheadControl predicate + dispatchMarkerAtPlayhead action).
// Reuses the three already-shipped, already-tested sidecar ops
// marker_at_playhead/set_marker_color/add_marker -- zero new Python. See
// the REV 2 design doc's Feature 1 section.
// ---------------------------------------------------------------------------

// startMarkerAtPlayheadSidecar spins up a fake unix-socket sidecar for
// these tests, same shape as startDirectionSelectSidecar
// (direction_select_test.go): records every non-"status"/
// "restore_highlight" command, replies to "status" with {"ok":true,
// "page":page}, replies to "marker_at_playhead" with a settable canned
// response (default {"found": false} -- forces every test to explicitly
// script what it expects, same "not_configured"-style default rationale as
// startDirectionSelectSidecar), and replies {"ok": true} to everything
// else (add_marker, set_marker_color, clear_highlight, ...).
//
// setProbeResp is variadic: setProbeResp(one) behaves exactly as it always
// has (that single response answers EVERY "marker_at_playhead" call in the
// test, any number of times -- unchanged for the ~20 existing callers).
// setProbeResp(first, second, ...) additionally lets a test script a
// SEQUENCE, consumed in order across successive probe calls, sticking on
// the last element once exhausted -- needed for dispatchMarkerAtPlayhead's
// ADD branch (2026-08-25 fix), which now probes marker_at_playhead a
// SECOND time right after a successful add_marker.
func startMarkerAtPlayheadSidecar(t *testing.T, page string) (
	sockPath string,
	received func() []map[string]any,
	setProbeResp func(...map[string]any),
) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-map-*.sock")
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
	// probeResps is a QUEUE, consumed in order across successive
	// "marker_at_playhead" calls within a single test -- needed because
	// dispatchMarkerAtPlayhead's ADD branch (2026-08-25, the "marker-mode
	// trap" fix) now issues a SECOND marker_at_playhead probe right after a
	// successful add_marker, to learn the new marker's absolute range (see
	// that call site's doc comment). Once exhausted, the LAST element
	// repeats forever (sticky-last) -- this is what keeps every EXISTING
	// caller of setProbeResp(oneResponse) byte-identical to before: a
	// single-element queue behaves exactly like the old static probeResp
	// did, for any number of probe calls a test triggers.
	probeResps := []map[string]any{{"found": false}}
	probeIdx := 0

	setProbeResp = func(rs ...map[string]any) {
		mu.Lock()
		if len(rs) > 0 {
			probeResps = rs
			probeIdx = 0
		}
		mu.Unlock()
	}

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
					op, _ := m["op"].(string)

					if op != "status" && op != "restore_highlight" {
						mu.Lock()
						recs = append(recs, m)
						mu.Unlock()
					}

					var reply []byte
					switch op {
					case "status":
						reply, _ = json.Marshal(map[string]any{"ok": true, "page": page})
					case "marker_at_playhead":
						mu.Lock()
						idx := probeIdx
						if idx >= len(probeResps) {
							idx = len(probeResps) - 1 // sticky-last
						} else {
							probeIdx++
						}
						r := probeResps[idx]
						mu.Unlock()
						reply, _ = json.Marshal(r)
					default:
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
	}, setProbeResp
}

// stubStateFile redirects the package-level stateFilePath seam (see
// TestSaveSentinel in engine_test.go for the established pattern) to a
// t.TempDir() path for the duration of the test.
//
// MUST be called before New()/newEngineWithConfig() by any test whose
// assertions depend on the engine's INITIAL pmode/lastFolder state.
// Discovered live, 2026-08-12: stateFilePath defaults to the real
// $HOME/.2t5-deck-plus-state.json -- the SAME file the actually-running
// production 2t5-deck-plus app on this machine reads at its own startup and
// writes on every shutdown/Quick-Export-folder-pick. New() unconditionally
// reads it to seed savedPMode; Start()/Stop() unconditionally write it via
// saveSentinel. A test that does not call this stub is not just
// "hermetic in theory but coupled in practice" -- it is actively coupled
// to whatever the user's live app last persisted (observed here as a
// real, non-test failure: the live sentinel's pmode happened to be "sat"
// because the user's console currently has Sat mode selected, silently
// flipping this feature's whenPMode gate under test). Pre-existing
// condition of the whole test suite -- the vast majority of engine tests
// calling Start()/Stop() do NOT call this stub either -- not introduced
// by this feature; flagged rather than fixed suite-wide, out of scope for
// this task.
func stubStateFile(t *testing.T) {
	t.Helper()
	orig := stateFilePath
	stateFilePath = t.TempDir() + "/test-sentinel.json"
	t.Cleanup(func() { stateFilePath = orig })
}

// ---------------------------------------------------------------------------
// isMarkerAtPlayheadControl -- pure-function unit tests
// ---------------------------------------------------------------------------

func TestIsMarkerAtPlayheadControl(t *testing.T) {
	cfg := loadConfig(t, `{
		"edit": {
			"P1btn": {"type": "markerAtPlayhead", "markerColor": "Blue", "whenPMode": "hue"},
			"Contrast": {"type": "property", "property": "Pan"}
		}
	}`)
	cases := []struct {
		name    string
		page    string
		control string
		want    bool
	}{
		{"edit page, P1btn markerAtPlayhead binding", "edit", "P1btn", true},
		{"edit page, property control is NOT markerAtPlayhead", "edit", "Contrast", false},
		{"edit page, control absent from edit bank", "edit", "P2btn", false},
		{"color page, P1btn present in edit bank but wrong page", "color", "P1btn", false},
		{"cut page, P1btn", "cut", "P1btn", false},
		{"empty page, P1btn", "", "P1btn", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMarkerAtPlayheadControl(cfg, c.page, 1, c.control, ""); got != c.want {
				t.Errorf("isMarkerAtPlayheadControl(page=%q, control=%q) = %v, want %v", c.page, c.control, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// dispatchMarkerAtPlayhead -- direct unit tests (fake sidecar, no Start())
// ---------------------------------------------------------------------------

// TestDispatchMarkerAtPlayhead_Found_RecoloursNoAdd proves the recolour
// branch: a marker found at the playhead is recoloured via
// set_marker_color (carrying the probe's frame + the binding's
// MarkerColor), add_marker is never sent, and the recoloured marker
// becomes ACTIVE (ControlDial resize targets it).
func TestDispatchMarkerAtPlayhead_Found_RecoloursNoAdd(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": true, "frame": 120.0, "duration": 25.0, "abs_start": 100.0, "abs_end": 125.0})

	var gotFrame int
	var gotColor string
	var gotDur float64
	setActiveMarker := func(frame int, color string, durationSeconds float64) {
		gotFrame, gotColor, gotDur = frame, color, durationSeconds
	}

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Red", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P5btn", b, 0, "hue", setActiveMarker, func(int, int) {}, func() {})

	waitForRecs(t, received, 2) // marker_at_playhead probe + set_marker_color
	for _, r := range received() {
		if r["op"] == "add_marker" {
			t.Fatalf("add_marker must not be sent when a marker was found, got %v", received())
		}
	}
	rec := recByOp(t, received(), "set_marker_color")
	if got := rec["frame"].(float64); got != 120.0 {
		t.Errorf("set_marker_color frame = %v, want 120", got)
	}
	if rec["color"] != "Red" {
		t.Errorf("set_marker_color color = %v, want Red", rec["color"])
	}
	if gotFrame != 120 || gotColor != "Red" || gotDur != 25.0 {
		t.Errorf("setActiveMarker called with (%d,%q,%v), want (120,\"Red\",25.0) -- recoloured marker must become active", gotFrame, gotColor, gotDur)
	}
}

// TestDispatchMarkerAtPlayhead_NotFound_AddsNoRecolour proves the add
// branch: no marker at the playhead sends exactly one add_marker op (the
// binding's colour), never set_marker_color.
func TestDispatchMarkerAtPlayhead_NotFound_AddsNoRecolour(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": false})

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Green", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P3btn", b, 0, "hue", noopSetActiveMarker, func(int, int) {}, func() {})

	waitForRecs(t, received, 2) // marker_at_playhead probe + add_marker
	for _, r := range received() {
		if r["op"] == "set_marker_color" {
			t.Fatalf("set_marker_color must not be sent when no marker was found, got %v", received())
		}
	}
	rec := recByOp(t, received(), "add_marker")
	if rec["color"] != "Green" {
		t.Errorf("add_marker color = %v, want Green", rec["color"])
	}
}

// ---------------------------------------------------------------------------
// THE "MARKER-MODE TRAP" FIX (2026-08-25): dispatchMarkerAtPlayhead's ADD
// branch must record the new marker's absolute range so maybeAutoExitMarker
// can actually end marker mode once the playhead leaves it. Before this fix,
// only the RECOLOUR branch (tested above) ever called recordMarkerRange --
// the far more common ADD path left activeMarkerAbsStart/End at whatever
// they were before the press, silently trapping the ControlDial in resize
// mode indefinitely (live symptom: dial still resizing a marker hours
// later, playhead nowhere near it).
// ---------------------------------------------------------------------------

// TestDispatchMarkerAtPlayhead_NotFound_Add_RecordsAbsRangeViaFollowupProbe
// proves the actual fix: after a successful add_marker, a SECOND
// marker_at_playhead probe is sent, and its abs_start/abs_end are passed to
// recordMarkerRange -- exactly the values maybeAutoExitMarker needs.
func TestDispatchMarkerAtPlayhead_NotFound_Add_RecordsAbsRangeViaFollowupProbe(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	// First probe (before add): not found, so dispatchMarkerAtPlayhead takes
	// the ADD branch. Second probe (the fix's follow-up, right after
	// add_marker succeeds): found, carrying the new marker's absolute range.
	setProbeResp(
		map[string]any{"found": false},
		map[string]any{"found": true, "frame": 21136.0, "color": "Blue", "duration": 3.0, "fps": 25.0, "abs_start": 111136.0, "abs_end": 111211.0},
	)

	var gotStart, gotEnd int
	var recordCalls int
	recordMarkerRange := func(start, end int) {
		recordCalls++
		gotStart, gotEnd = start, end
	}

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Blue", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P6btn", b, 0, "hue", noopSetActiveMarker, recordMarkerRange, func() {})

	waitForRecs(t, received, 3) // 1st probe + add_marker + 2nd (follow-up) probe
	probeCount := 0
	for _, r := range received() {
		if r["op"] == "marker_at_playhead" {
			probeCount++
		}
	}
	if probeCount != 2 {
		t.Fatalf("marker_at_playhead call count = %d, want 2 (initial + follow-up), recs=%v", probeCount, received())
	}
	if recordCalls != 1 {
		t.Fatalf("recordMarkerRange call count = %d, want 1", recordCalls)
	}
	if gotStart != 111136 || gotEnd != 111211 {
		t.Errorf("recordMarkerRange got (%d,%d), want (111136,111211)", gotStart, gotEnd)
	}
}

// TestDispatchMarkerAtPlayhead_NotFound_Add_FollowupProbeMisses_RangeNotRecorded
// proves graceful degradation: if the follow-up probe somehow doesn't find
// the marker it just created, recordMarkerRange is simply never called
// (no panic, no bogus zero-range write) -- no worse than this bug's
// pre-fix behavior, just not improved either.
func TestDispatchMarkerAtPlayhead_NotFound_Add_FollowupProbeMisses_RangeNotRecorded(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": false}) // every probe call (initial AND follow-up) misses

	recordCalls := 0
	recordMarkerRange := func(start, end int) { recordCalls++ }

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Blue", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P6btn", b, 0, "hue", noopSetActiveMarker, recordMarkerRange, func() {})

	waitForRecs(t, received, 3)
	if recordCalls != 0 {
		t.Errorf("recordMarkerRange call count = %d, want 0 (follow-up probe missed)", recordCalls)
	}
}

// TestDispatchMarkerAtPlayhead_NotFound_Add_NilRecordMarkerRange_NoFollowupProbe
// proves the follow-up probe is only sent when a caller actually supplied
// recordMarkerRange -- a nil callback (same defensive posture as every
// other nil-checked callback in this function) must not trigger the extra
// round trip at all.
func TestDispatchMarkerAtPlayhead_NotFound_Add_NilRecordMarkerRange_NoFollowupProbe(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": false})

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Blue", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P6btn", b, 0, "hue", noopSetActiveMarker, nil, func() {})

	waitForRecs(t, received, 2) // initial probe + add_marker only
	probeCount := 0
	for _, r := range received() {
		if r["op"] == "marker_at_playhead" {
			probeCount++
		}
	}
	if probeCount != 1 {
		t.Errorf("marker_at_playhead call count = %d, want 1 (no follow-up when recordMarkerRange is nil)", probeCount)
	}
}

// ---------------------------------------------------------------------------
// End-to-end proof the fix actually closes the trap: dispatch a real P-button
// ADD through dispatchMarkerAtPlayhead wired to a real *Engine's own
// setActiveMarker/setActiveMarkerAbsRange/clearActiveMarker, then run the
// SAME maybeAutoExitMarker the drain loop calls every tick and confirm it
// now fires once the playhead leaves the freshly-created marker's range --
// and does NOT fire while still inside it. This is the exact live scenario
// from the bug report (M21 at frame 21136, 75-frame clip, playhead miles
// away at 13569) reproduced as a hermetic test.
// ---------------------------------------------------------------------------

func TestMarkerAtPlayhead_Add_ThenAutoExit_FiresWhenPlayheadLeavesCreatedRange(t *testing.T) {
	sockPath, _, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(
		map[string]any{"found": false},
		map[string]any{"found": true, "frame": 21136.0, "color": "Blue", "duration": 3.0, "fps": 25.0, "abs_start": 21136.0, "abs_end": 21211.0},
	)

	e := &Engine{activeMarkerFrame: -1}
	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Blue", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P6btn", b, 0, "hue", e.setActiveMarker, e.setActiveMarkerAbsRange, e.clearActiveMarker)

	if got := e.getActiveMarkerFrame(); got < 0 {
		t.Fatalf("marker mode not entered after ADD, activeMarkerFrame = %d (test precondition)", got)
	}

	// Playhead far outside [21136, 21211) -- e.g. 13569 from the live report.
	maybeAutoExitMarker(e, true, true, 13569)

	if got := e.getActiveMarkerFrame(); got != -1 {
		t.Errorf("activeMarkerFrame after auto-exit check = %d, want -1 -- the trap: without this fix the range was never recorded, so auto-exit could never fire", got)
	}
}

func TestMarkerAtPlayhead_Add_ThenAutoExit_StaysActiveInsideCreatedRange(t *testing.T) {
	sockPath, _, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(
		map[string]any{"found": false},
		map[string]any{"found": true, "frame": 21136.0, "color": "Blue", "duration": 3.0, "fps": 25.0, "abs_start": 21136.0, "abs_end": 21211.0},
	)

	e := &Engine{activeMarkerFrame: -1}
	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Blue", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P6btn", b, 0, "hue", e.setActiveMarker, e.setActiveMarkerAbsRange, e.clearActiveMarker)

	before := e.getActiveMarkerFrame()
	if before < 0 {
		t.Fatalf("marker mode not entered after ADD, activeMarkerFrame = %d (test precondition)", before)
	}

	// Playhead still inside the recorded [21136, 21211) range.
	maybeAutoExitMarker(e, true, true, 21150)

	if got := e.getActiveMarkerFrame(); got != before {
		t.Errorf("activeMarkerFrame after in-range check = %d, want unchanged from %d (must survive)", got, before)
	}
}

// TestDispatchMarkerAtPlayhead_NotFound_ActiveTrackSet_AddNeverSendsTrack
// proves CHANGE 2 (2026-08-12): a new marker's duration must always
// follow the sidecar's top-occupied-clip tier of add_marker's duration
// precedence, regardless of what the direction buttons (D1) have
// selected -- so activeTrack, even when nonzero (a real D1 selection),
// must NEVER be forwarded as add_marker's "track" argument. Forwarding it
// would force the sidecar's FIRST tier (explicit track) instead, letting
// clip selection override the marker's span -- the exact bug this change
// removes.
func TestDispatchMarkerAtPlayhead_NotFound_ActiveTrackSet_AddNeverSendsTrack(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": false})

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Green", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P3btn", b, 3, "hue", noopSetActiveMarker, func(int, int) {}, func() {}) // activeTrack=3, a real D1 selection

	waitForRecs(t, received, 2)
	rec := recByOp(t, received(), "add_marker")
	if _, hasTrack := rec["track"]; hasTrack {
		t.Errorf("add_marker must never send \"track\" (duration always follows the top-occupied clip, never D1's selection), got %v", rec)
	}
}

// TestDispatchMarkerAtPlayhead_Recolour_NoPlayheadMove proves a recolour
// sends no playhead-moving op -- only the probe + set_marker_color, never
// e.g. a jog/seek op.
func TestDispatchMarkerAtPlayhead_Recolour_NoPlayheadMove(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": true, "frame": 50.0, "duration": 10.0})

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Cyan", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P2btn", b, 0, "hue", noopSetActiveMarker, func(int, int) {}, func() {})

	waitForRecs(t, received, 2)
	for _, r := range received() {
		op, _ := r["op"].(string)
		if op != "marker_at_playhead" && op != "set_marker_color" {
			t.Errorf("unexpected op %q sent by a recolour, want only marker_at_playhead/set_marker_color: %v", op, received())
		}
	}
}

// ---------------------------------------------------------------------------
// Three-way toggle (2026-08-12): a marker found at the playhead whose
// colour case-insensitively MATCHES the pressed button's configured
// colour is DELETED (not recoloured-to-itself), and marker mode is
// exited. A different colour still recolours (proven above); no marker
// still adds (proven above).
// ---------------------------------------------------------------------------

// TestDispatchMarkerAtPlayhead_Found_SameColour_DeletesMarker proves the
// delete branch: probe finds a marker whose colour equals the pressed
// button's colour -> delete_marker is sent with the probe's frame, never
// set_marker_color/add_marker.
func TestDispatchMarkerAtPlayhead_Found_SameColour_DeletesMarker(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": true, "frame": 120.0, "color": "Red", "duration": 25.0})

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Red", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P1btn", b, 0, "hue", noopSetActiveMarker, func(int, int) {}, func() {})

	waitForRecs(t, received, 2) // marker_at_playhead probe + delete_marker
	for _, r := range received() {
		if r["op"] == "set_marker_color" || r["op"] == "add_marker" {
			t.Fatalf("same-colour press must delete, not recolour/add: got %v", received())
		}
	}
	rec := recByOp(t, received(), "delete_marker")
	if got := rec["frame"].(float64); got != 120.0 {
		t.Errorf("delete_marker frame = %v, want 120", got)
	}
}

// TestDispatchMarkerAtPlayhead_Found_SameColour_CaseInsensitive proves the
// colour comparison ignores case ("red" probe vs "Red" binding).
func TestDispatchMarkerAtPlayhead_Found_SameColour_CaseInsensitive(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": true, "frame": 10.0, "color": "red", "duration": 5.0})

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Red", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P1btn", b, 0, "hue", noopSetActiveMarker, func(int, int) {}, func() {})

	waitForRecs(t, received, 2)
	recByOp(t, received(), "delete_marker") // fails the test via t.Fatalf if absent
}

// TestDispatchMarkerAtPlayhead_Found_EmptyProbeColour_TreatedAsDifferent
// proves the tolerance requirement: an absent/empty "color" field in the
// probe response must never be treated as a match (which would risk an
// unintended delete) -- it degrades to the safer recolour path instead,
// same as today's existing recolour tests (whose canned probe responses
// already omit "color" entirely).
func TestDispatchMarkerAtPlayhead_Found_EmptyProbeColour_TreatedAsDifferent(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": true, "frame": 15.0, "color": "", "duration": 5.0})

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Red", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P1btn", b, 0, "hue", noopSetActiveMarker, func(int, int) {}, func() {})

	waitForRecs(t, received, 2)
	for _, r := range received() {
		if r["op"] == "delete_marker" {
			t.Fatalf("an empty probe colour must never trigger a delete, got %v", received())
		}
	}
	recByOp(t, received(), "set_marker_color")
}

// TestDispatchMarkerAtPlayhead_Delete_ClearsActiveMarker proves a
// successful delete exits marker mode -- otherwise the ControlDial would
// keep trying to resize a marker that no longer exists.
func TestDispatchMarkerAtPlayhead_Delete_ClearsActiveMarker(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": true, "frame": 30.0, "color": "Blue", "duration": 5.0})

	var cleared bool
	clearActiveMarker := func() { cleared = true }

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Blue", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P1btn", b, 0, "hue", noopSetActiveMarker, func(int, int) {}, clearActiveMarker)

	waitForRecs(t, received, 2)
	if !cleared {
		t.Errorf("expected clearActiveMarker to be called after a successful delete")
	}
}

// TestDispatchMarkerAtPlayhead_Recolour_DoesNotClearActiveMarker proves
// the mirror: a recolour (different colour) must NOT exit marker mode --
// only setActiveMarker (re-pinning to the recoloured marker) is expected,
// per the existing Found_RecoloursNoAdd test.
func TestDispatchMarkerAtPlayhead_Recolour_DoesNotClearActiveMarker(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": true, "frame": 40.0, "color": "Green", "duration": 5.0})

	var cleared bool
	clearActiveMarker := func() { cleared = true }

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Red", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P1btn", b, 0, "hue", noopSetActiveMarker, func(int, int) {}, clearActiveMarker)

	waitForRecs(t, received, 2)
	if cleared {
		t.Errorf("a recolour (different colour) must not call clearActiveMarker")
	}
	recByOp(t, received(), "set_marker_color")
}

// TestDispatchMarkerAtPlayhead_Delete_FailedResponse_DoesNotClear proves
// a refused delete_marker (ok:false) leaves marker mode untouched --
// exiting on a failed delete would strand the ControlDial on a marker
// that Resolve says still exists.
func TestDispatchMarkerAtPlayhead_Delete_FailedResponse_DoesNotClear(t *testing.T) {
	sockPath, received := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"marker_at_playhead": {"found": true, "frame": 50.0, "color": "Blue", "duration": 5.0},
		"delete_marker":      {"ok": false, "err": "marker_not_found"},
	})
	client := dialRecordingClient(t, sockPath)

	var cleared bool
	clearActiveMarker := func() { cleared = true }

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Blue", WhenPMode: "hue"}
	got := dispatchMarkerAtPlayhead(client, "P1btn", b, 0, "hue", noopSetActiveMarker, func(int, int) {}, clearActiveMarker)

	waitForRecs(t, received, 2)
	if cleared {
		t.Errorf("a failed delete_marker response must not call clearActiveMarker")
	}
	if got != "" {
		t.Errorf("LastAction = %q, want empty string on a failed delete", got)
	}
}

// TestDispatchMarkerAtPlayhead_WhenPMode_Mismatch_SuppressesAllOps proves
// the whenPMode gate: a binding requiring "hue" while the live pmode is
// "sat" sends NO ops at all (not even the marker_at_playhead probe).
func TestDispatchMarkerAtPlayhead_WhenPMode_Mismatch_SuppressesAllOps(t *testing.T) {
	sockPath, received, _ := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Blue", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P1btn", b, 0, "sat", noopSetActiveMarker, func(int, int) {}, func() {})

	time.Sleep(150 * time.Millisecond) // give any (incorrect) send a chance to land
	if recs := received(); len(recs) != 0 {
		t.Errorf("expected zero ops sent when whenPMode mismatches live pmode, got %v", recs)
	}
}

// TestDispatchMarkerAtPlayhead_WhenPMode_Match_SendsOps proves the
// mirror: a binding requiring "hue" while the live pmode IS "hue" fires
// normally.
func TestDispatchMarkerAtPlayhead_WhenPMode_Match_SendsOps(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": false})

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Yellow", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(client, "P4btn", b, 0, "hue", noopSetActiveMarker, func(int, int) {}, func() {})

	waitForRecs(t, received, 2)
	rec := recByOp(t, received(), "add_marker")
	if rec["color"] != "Yellow" {
		t.Errorf("add_marker color = %v, want Yellow", rec["color"])
	}
}

// TestDispatchMarkerAtPlayhead_WhenPModeEmpty_AlwaysFires proves an
// ungated binding (WhenPMode=="") fires regardless of live pmode.
func TestDispatchMarkerAtPlayhead_WhenPModeEmpty_AlwaysFires(t *testing.T) {
	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)
	setProbeResp(map[string]any{"found": false})

	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Pink"} // WhenPMode omitted
	dispatchMarkerAtPlayhead(client, "P6btn", b, 0, "lum", noopSetActiveMarker, func(int, int) {}, func() {})

	waitForRecs(t, received, 2)
	rec := recByOp(t, received(), "add_marker")
	if rec["color"] != "Pink" {
		t.Errorf("add_marker color = %v, want Pink", rec["color"])
	}
}

// TestDispatchMarkerAtPlayhead_NilClient_NoPanic mirrors
// TestDispatchAddMarker_NilClient_NoPanic.
func TestDispatchMarkerAtPlayhead_NilClient_NoPanic(t *testing.T) {
	b := config.Binding{Type: "markerAtPlayhead", MarkerColor: "Blue", WhenPMode: "hue"}
	dispatchMarkerAtPlayhead(nil, "P1btn", b, 0, "hue", noopSetActiveMarker, func(int, int) {}, func() {})
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration via note_on.
// ---------------------------------------------------------------------------

// TestNoteOnMarkerAtPlayhead_EditPage_Found_SendsSetMarkerColor proves the
// feature end to end through Start(): P1btn pressed on the Edit page,
// pmode hue, marker found at playhead -> set_marker_color sent, no
// add_marker.
func TestNoteOnMarkerAtPlayhead_EditPage_Found_SendsSetMarkerColor(t *testing.T) {
	stubStateFile(t) // isolate from the real ~/.2t5-deck-plus-state.json -- see its doc comment
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"82":"P1btn"}}`
	bindings := `{
		"col":  {},
		"edit": {"P1btn": {"type": "markerAtPlayhead", "markerColor": "Blue", "whenPMode": "hue"}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received, setProbeResp := startMarkerAtPlayheadSidecar(t, "edit")
	setProbeResp(map[string]any{"found": true, "frame": 30.0, "duration": 5.0})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 82, 100), 0) // P1btn press

	waitForRecs(t, received, 2)
	rec := recByOp(t, received(), "set_marker_color")
	if rec["color"] != "Blue" {
		t.Errorf("set_marker_color color = %v, want Blue", rec["color"])
	}
	for _, r := range received() {
		if r["op"] == "add_marker" {
			t.Errorf("add_marker must not be sent when a marker was found, got %v", received())
		}
	}
}

// TestNoteOnMarkerAtPlayhead_EditPage_PModeSat_Suppressed proves the page
// C2 (P-mode) is checked through the FULL Start() path too: with pmode
// switched to "sat" via a real note_on press on the engraved Sat button,
// a subsequent P1btn press sends no ops at all.
func TestNoteOnMarkerAtPlayhead_EditPage_PModeSat_Suppressed(t *testing.T) {
	stubStateFile(t) // isolate from the real ~/.2t5-deck-plus-state.json -- see its doc comment
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"82":"P1btn","70":"Sat"}}`
	bindings := `{
		"col":  {"Sat": {"type": "pmode", "pmode": "sat"}},
		"edit": {"P1btn": {"type": "markerAtPlayhead", "markerColor": "Blue", "whenPMode": "hue"}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received, _ := startMarkerAtPlayheadSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 70, 100), 0) // Sat press: pmode -> "sat"
	cb(midi.NoteOn(0, 82, 100), 0) // P1btn press: should be suppressed

	time.Sleep(150 * time.Millisecond)
	if recs := received(); len(recs) != 0 {
		t.Errorf("expected zero ops sent for P1btn while pmode=sat, got %v", recs)
	}
}

// TestNoteOnMarkerAtPlayhead_ColorPage_Inert proves page-gating through
// Start(): P1btn on the Color page must never dispatch markerAtPlayhead
// (P-buttons have no col-bank binding at all, so this also proves no
// crash/fallthrough).
func TestNoteOnMarkerAtPlayhead_ColorPage_Inert(t *testing.T) {
	stubStateFile(t) // isolate from the real ~/.2t5-deck-plus-state.json -- see its doc comment
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"82":"P1btn"}}`
	bindings := `{
		"col":  {},
		"edit": {"P1btn": {"type": "markerAtPlayhead", "markerColor": "Blue", "whenPMode": "hue"}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received, _ := startMarkerAtPlayheadSidecar(t, "color")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 82, 100), 0) // P1btn press on the Color page

	time.Sleep(150 * time.Millisecond)
	if recs := received(); len(recs) != 0 {
		t.Errorf("expected zero ops sent for P1btn on the Color page, got %v", recs)
	}
}

// TestNoteOnD1ReleaseAndC1AddMarker_Coexist proves the new C1 addMarker
// intercept is inserted alongside the existing D1 selectClip-release
// intercept without either one shadowing or interfering with the other
// -- both fire correctly for their own distinct controls in the same
// Start() session.
func TestNoteOnD1ReleaseAndC1AddMarker_Coexist(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{"49":"C1"}}`
	bindings := `{
		"col": {"C1": {"type": "key", "key": "Alt+S"}},
		"edit": {
			"D1": {"type": "selectClip"},
			"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"}
		}
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
	cb(midi.NoteOn(0, 41, 100), 0) // D1 press: release/unselect (no prior turn -> activeTrack already 0)
	cb(midi.NoteOn(0, 49, 100), 0) // C1 press: add marker

	deadline := time.Now().Add(2 * time.Second)
	var sawSetClipsColor, sawAddMarker bool
	for time.Now().Before(deadline) {
		for _, r := range received() {
			if r["op"] == "set_clips_color" {
				sawSetClipsColor = true
			}
			if r["op"] == "add_marker" {
				sawAddMarker = true
			}
		}
		if sawSetClipsColor && sawAddMarker {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sawSetClipsColor {
		t.Errorf("expected a set_clips_color op from D1's release intercept, received=%v", received())
	}
	if !sawAddMarker {
		t.Errorf("expected an add_marker op from C1's addMarker intercept, received=%v", received())
	}
}
