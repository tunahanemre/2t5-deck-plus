package engine

// Tests for the C1 "marker mode" feature (resize + recolor), built on top
// of the already-shipped C1 "add marker at playhead" (marker_test.go):
//   - C1 press ALSO enters marker mode: activeMarkerFrame is set to the
//     sidecar's returned frame (dispatchAddMarker's widened signature).
//   - ControlDial rotation, while marker mode is active, resizes the
//     active marker's duration (resize_marker) INSTEAD OF jogging the
//     playhead -- the drain loop's b.IsJog() branch.
//   - ControlDial PRESS (note_on note 48) exits marker mode.
//   - P1-8btn, while marker mode is active, sets the active marker's
//     color (set_marker_color) -- a no-op everywhere else.
//   - Status.MarkerMode reflects the current marker-mode state, updated
//     on every C1/resize/recolor/exit event (no per-tick sidecar poll).
//
// All hermetic: no live Resolve, no real CGEvent/keys, DECKPLUS_ARMED
// never set. AddMarker/GetMarkers/DeleteMarkerAtFrame are never called --
// only the Go engine <-> fake-sidecar IPC boundary is exercised here (the
// Python side is covered by sidecar/test_marker_mode_ops.py).

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
// helpers
// ---------------------------------------------------------------------------

// noopSetActiveMarker is a do-nothing setActiveMarker callback for
// dispatchAddMarker tests (marker_test.go) that only care about the
// SEND shape, not the marker-mode state transition.
func noopSetActiveMarker(frame int, color string, durationSeconds float64) {}

// startFixedOpReplySidecar behaves like startRecordingSidecar (excludes
// "status"/"restore_highlight" from the recorded list, so recs[0]-style
// assertions in a fresh Start() session stay meaningful) but replies to
// specific ops with a caller-provided payload instead of the generic
// {"ok": true} -- lets marker-mode tests exercise realistic sidecar
// responses (frame/duration/fps) that startRecordingSidecar's blanket
// {"ok": true} can't produce.
func startFixedOpReplySidecar(t *testing.T, page string, opReplies map[string]map[string]any) (sockPath string, received func() []map[string]any) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-fixedreply-*.sock")
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

					op, _ := m["op"].(string)
					if op != "status" && op != "restore_highlight" {
						mu.Lock()
						recs = append(recs, m)
						mu.Unlock()
					}

					var reply []byte
					switch {
					case op == "status":
						reply, _ = json.Marshal(map[string]any{"ok": true, "page": page})
					case opReplies[op] != nil:
						reply, _ = json.Marshal(opReplies[op])
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
	}
}

// ---------------------------------------------------------------------------
// isJogPressControl / isMarkerColorControl -- pure predicates
// ---------------------------------------------------------------------------

func TestIsJogPressControl(t *testing.T) {
	cfg := loadConfig(t, `{
		"col": {},
		"edit": {
			"ControlDial": {"type": "jog", "maxDelta": 90},
			"Contrast": {"type": "property", "property": "Pan"}
		}
	}`)
	cases := []struct {
		name    string
		page    string
		control string
		want    bool
	}{
		{"edit page, ControlDial jog binding", "edit", "ControlDial", true},
		{"edit page, property control is NOT jog", "edit", "Contrast", false},
		{"edit page, control absent from edit bank", "edit", "Copy", false},
		{"color page, ControlDial present in edit bank but wrong page", "color", "ControlDial", false},
		{"cut page, ControlDial", "cut", "ControlDial", false},
		{"empty page, ControlDial", "", "ControlDial", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isJogPressControl(cfg, c.page, 1, c.control, ""); got != c.want {
				t.Errorf("isJogPressControl(page=%q, control=%q) = %v, want %v", c.page, c.control, got, c.want)
			}
		})
	}
}

func TestIsMarkerColorControl(t *testing.T) {
	cfg := loadConfig(t, `{
		"col": {},
		"edit": {
			"P1btn": {"type": "markerColor", "markerColor": "Blue"},
			"Contrast": {"type": "property", "property": "Pan"}
		}
	}`)
	cases := []struct {
		name    string
		page    string
		control string
		want    bool
	}{
		{"edit page, P1btn markerColor binding", "edit", "P1btn", true},
		{"edit page, property control is NOT markerColor", "edit", "Contrast", false},
		{"edit page, control absent from edit bank", "edit", "Copy", false},
		{"color page, P1btn present in edit bank but wrong page", "color", "P1btn", false},
		{"cut page, P1btn", "cut", "P1btn", false},
		{"empty page, P1btn", "", "P1btn", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMarkerColorControl(cfg, c.page, 1, c.control, ""); got != c.want {
				t.Errorf("isMarkerColorControl(page=%q, control=%q) = %v, want %v", c.page, c.control, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// dispatchControlDialPress -- direct unit tests
// ---------------------------------------------------------------------------

func TestDispatchControlDialPress_ActiveMode_ClearsMarker(t *testing.T) {
	cleared := false
	getActive := func() int { return 25 }
	clear := func() { cleared = true }

	dispatchControlDialPress(getActive, clear)

	if !cleared {
		t.Error("expected clearActiveMarker to be called when marker mode is active")
	}
}

func TestDispatchControlDialPress_InactiveMode_NoOp(t *testing.T) {
	cleared := false
	getActive := func() int { return -1 }
	clear := func() { cleared = true }

	dispatchControlDialPress(getActive, clear)

	if cleared {
		t.Error("clearActiveMarker must NOT be called when marker mode is already inactive")
	}
}

// ---------------------------------------------------------------------------
// dispatchSetMarkerColor -- direct unit tests
// ---------------------------------------------------------------------------

func TestDispatchSetMarkerColor_ActiveMode_SendsSetMarkerColor(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	var gotColor string
	updateColor := func(c string) { gotColor = c }
	getActive := func() int { return 25 }

	b := config.Binding{Type: "markerColor", MarkerColor: "Red"}
	dispatchSetMarkerColor(client, "P5btn", b, getActive, updateColor)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "set_marker_color" {
		t.Fatalf("op = %v, want set_marker_color", recs[0]["op"])
	}
	if got := recs[0]["frame"].(float64); got != 25 {
		t.Errorf("frame = %v, want 25", got)
	}
	if recs[0]["color"] != "Red" {
		t.Errorf("color = %v, want Red", recs[0]["color"])
	}
	if gotColor != "Red" {
		t.Errorf("updateActiveMarkerColor called with %q, want Red", gotColor)
	}
}

func TestDispatchSetMarkerColor_InactiveMode_NoOp(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	updateCalled := false
	updateColor := func(string) { updateCalled = true }
	getActive := func() int { return -1 }

	b := config.Binding{Type: "markerColor", MarkerColor: "Red"}
	dispatchSetMarkerColor(client, "P5btn", b, getActive, updateColor)

	time.Sleep(150 * time.Millisecond)
	if len(received()) != 0 {
		t.Errorf("expected no sidecar op when marker mode inactive, got %v", received())
	}
	if updateCalled {
		t.Error("updateActiveMarkerColor must not be called when marker mode inactive")
	}
}

func TestDispatchSetMarkerColor_NilClient_NoPanic(t *testing.T) {
	getActive := func() int { return 25 }
	dispatchSetMarkerColor(nil, "P5btn", config.Binding{Type: "markerColor", MarkerColor: "Red"}, getActive, func(string) {})
}

func TestDispatchSetMarkerColor_SidecarFailure_DoesNotUpdateColor(t *testing.T) {
	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"set_marker_color": {"ok": false, "err": "marker_not_found"},
	})
	client := dialRecordingClient(t, sockPath)

	updateCalled := false
	updateColor := func(string) { updateCalled = true }
	getActive := func() int { return 25 }

	dispatchSetMarkerColor(client, "P5btn", config.Binding{Type: "markerColor", MarkerColor: "Red"}, getActive, updateColor)

	if updateCalled {
		t.Error("updateActiveMarkerColor must not be called on a sidecar failure response")
	}
}

// ---------------------------------------------------------------------------
// dispatchMarkerResize -- direct unit tests
// ---------------------------------------------------------------------------

func TestDispatchMarkerResize_PositiveStep_SendsPositiveDelta(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchMarkerResize(client, 25, 3, 1, func(float64) {})

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "resize_marker" {
		t.Fatalf("op = %v, want resize_marker", recs[0]["op"])
	}
	if got := recs[0]["frame"].(float64); got != 25 {
		t.Errorf("frame = %v, want 25", got)
	}
	if got := recs[0]["delta_frames"].(float64); got != 3 {
		t.Errorf("delta_frames = %v, want 3 (step=3 * framesPerStep=1)", got)
	}
}

func TestDispatchMarkerResize_NegativeStep_SendsNegativeDelta(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchMarkerResize(client, 25, -2, 4, func(float64) {})

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["delta_frames"].(float64); got != -8 {
		t.Errorf("delta_frames = %v, want -8 (step=-2 * framesPerStep=4)", got)
	}
}

func TestDispatchMarkerResize_ZeroStep_NoOp(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchMarkerResize(client, 25, 0, 1, func(float64) {})

	time.Sleep(150 * time.Millisecond)
	if len(received()) != 0 {
		t.Errorf("expected no sidecar op for step=0, got %v", received())
	}
}

func TestDispatchMarkerResize_NilClient_NoPanic(t *testing.T) {
	dispatchMarkerResize(nil, 25, 3, 1, func(float64) {})
}

func TestDispatchMarkerResize_SuccessResponse_UpdatesDurationInSeconds(t *testing.T) {
	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"resize_marker": {"ok": true, "frame": 25, "duration": 30, "fps": 25.0},
	})
	client := dialRecordingClient(t, sockPath)

	var gotSeconds float64
	var called bool
	dispatchMarkerResize(client, 25, 5, 1, func(s float64) { gotSeconds = s; called = true })

	if !called {
		t.Fatal("updateDuration was never called")
	}
	if gotSeconds != 1.2 {
		t.Errorf("updateDuration called with %v seconds, want 1.2 (30 frames / 25fps)", gotSeconds)
	}
}

func TestDispatchMarkerResize_ResponseMissingFps_DoesNotCallUpdateDuration(t *testing.T) {
	// Mirrors startRecordingSidecar's generic {"ok": true} reply (no
	// "duration"/"fps" keys) -- must not panic or call updateDuration
	// with garbage when fps can't be determined.
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	called := false
	dispatchMarkerResize(client, 25, 5, 1, func(float64) { called = true })

	waitForRecs(t, received, 1)
	if called {
		t.Error("updateDuration must not be called when the response has no usable fps")
	}
}

func TestDispatchMarkerResize_FailureResponse_DoesNotCallUpdateDuration(t *testing.T) {
	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"resize_marker": {"ok": false, "err": "marker_not_found"},
	})
	client := dialRecordingClient(t, sockPath)

	called := false
	dispatchMarkerResize(client, 25, 5, 1, func(float64) { called = true })

	if called {
		t.Error("updateDuration must not be called on a sidecar failure response")
	}
}

// ---------------------------------------------------------------------------
// dispatchAddMarker -- marker-mode entry (widened signature)
// ---------------------------------------------------------------------------

// TestDispatchAddMarker_Success_CallsSetActiveMarkerWithReturnedFrame also
// doubles as the response-missing-duration/fps fallback test: the fixed
// reply below carries only "frame" (no "duration"/"fps"), so
// dispatchAddMarker must fall back to the LOCALLY configured
// MarkerDurationSeconds (2) for gotDuration rather than computing
// garbage from an absent duration/fps pair.
func TestDispatchAddMarker_Success_CallsSetActiveMarkerWithReturnedFrame(t *testing.T) {
	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": true, "frame": 77},
	})
	client := dialRecordingClient(t, sockPath)

	var gotFrame int
	var gotColor string
	var gotDuration float64
	var called bool
	setActiveMarker := func(frame int, color string, durationSeconds float64) {
		gotFrame, gotColor, gotDuration, called = frame, color, durationSeconds, true
	}

	b := config.Binding{Type: "addMarker", MarkerDurationSeconds: 2, MarkerColor: "Red"}
	dispatchAddMarker(client, "C1", b, 0, setActiveMarker, func() int { return -1 }, func() {}, func(int, int) {})

	if !called {
		t.Fatal("setActiveMarker was never called")
	}
	if gotFrame != 77 {
		t.Errorf("frame = %d, want 77", gotFrame)
	}
	if gotColor != "Red" {
		t.Errorf("color = %q, want Red", gotColor)
	}
	if gotDuration != 2 {
		t.Errorf("durationSeconds = %v, want 2 (fallback to configured value: response had no duration/fps)", gotDuration)
	}
}

func TestDispatchAddMarker_Failure_DoesNotCallSetActiveMarker(t *testing.T) {
	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": false, "err": "marker_exists", "frame": 42},
	})
	client := dialRecordingClient(t, sockPath)

	called := false
	setActiveMarker := func(int, string, float64) { called = true }

	b := config.Binding{Type: "addMarker", MarkerDurationSeconds: 1, MarkerColor: "Blue"}
	dispatchAddMarker(client, "C1", b, 0, setActiveMarker, func() int { return -1 }, func() {}, func(int, int) {})

	if called {
		t.Error("setActiveMarker must not be called when add_marker fails")
	}
}

// ---------------------------------------------------------------------------
// dispatchAddMarker -- activeTrack passthrough ("track" field) and
// response duration/fps preference (Batch 1, clip-length marker feature)
// ---------------------------------------------------------------------------

// TestDispatchAddMarker_ActiveTrackZero_OmitsTrackKey mirrors the
// established bump_property/reset_property "omit when zero" convention
// (selectclip_test.go's TestDispatchNoteOn_Property_ActiveTrackZero_OmitsTrackKey):
// activeTrack==0 means "no D1 selection", so the add_marker request must
// not carry a "track" key at all.
func TestDispatchAddMarker_ActiveTrackZero_OmitsTrackKey(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "addMarker", MarkerDurationSeconds: 1, MarkerColor: "Blue"}
	dispatchAddMarker(client, "C1", b, 0, noopSetActiveMarker, func() int { return -1 }, func() {}, func(int, int) {})

	waitForRecs(t, received, 2)
	recs := []map[string]any{recByOp(t, received(), "add_marker")}
	if _, has := recs[0]["track"]; has {
		t.Errorf("track key should be absent when activeTrack=0, got %v", recs[0]["track"])
	}
}

// TestDispatchAddMarker_ActiveTrackSet_IncludesTrackKey is the
// counterpart: a nonzero D1-selected track must be forwarded as "track"
// so the sidecar can target that track's clip.
func TestDispatchAddMarker_ActiveTrackSet_IncludesTrackKey(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "addMarker", MarkerDurationSeconds: 1, MarkerColor: "Blue"}
	dispatchAddMarker(client, "C1", b, 3, noopSetActiveMarker, func() int { return -1 }, func() {}, func(int, int) {})

	waitForRecs(t, received, 2)
	recs := []map[string]any{recByOp(t, received(), "add_marker")}
	if got := recs[0]["track"].(float64); got != 3 {
		t.Errorf("track = %v, want 3", got)
	}
}

// TestDispatchAddMarker_ActiveTrack_UsesClipDurationFromResponse proves
// the clip-length marker feature's engine-side half: when the sidecar
// resolves a track to an actual clip, it echoes the clip's real
// duration/fps back in the response, and dispatchAddMarker must PREFER
// that over the button's configured MarkerDurationSeconds when calling
// setActiveMarker (so the Marker status row shows the clip's real
// length, e.g. 200 frames / 25fps = 8s here, not the configured 1s).
func TestDispatchAddMarker_ActiveTrack_UsesClipDurationFromResponse(t *testing.T) {
	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": true, "frame": 77, "duration": 200, "fps": 25.0},
	})
	client := dialRecordingClient(t, sockPath)

	var gotDuration float64
	var called bool
	setActiveMarker := func(frame int, color string, durationSeconds float64) {
		gotDuration, called = durationSeconds, true
	}

	b := config.Binding{Type: "addMarker", MarkerDurationSeconds: 1, MarkerColor: "Blue"}
	dispatchAddMarker(client, "C1", b, 3, setActiveMarker, func() int { return -1 }, func() {}, func(int, int) {})

	if !called {
		t.Fatal("setActiveMarker was never called")
	}
	if gotDuration != 8.0 {
		t.Errorf("durationSeconds = %v, want 8.0 (200 frames / 25fps from the response, not the configured 1s)", gotDuration)
	}
}

// TestDispatchAddMarker_ResponseHasFpsButZero_FallsBackToConfiguredDuration
// exercises the fps<=0 branch of the reused dispatchMarkerResize guard
// shape -- a response with a present-but-nonsensical fps must not divide
// by zero/negative, and must fall back to the configured duration
// exactly like a response missing duration/fps entirely.
func TestDispatchAddMarker_ResponseHasFpsButZero_FallsBackToConfiguredDuration(t *testing.T) {
	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": true, "frame": 10, "duration": 200, "fps": 0.0},
	})
	client := dialRecordingClient(t, sockPath)

	var gotDuration float64
	setActiveMarker := func(frame int, color string, durationSeconds float64) {
		gotDuration = durationSeconds
	}

	b := config.Binding{Type: "addMarker", MarkerDurationSeconds: 4, MarkerColor: "Blue"}
	dispatchAddMarker(client, "C1", b, 3, setActiveMarker, func() int { return -1 }, func() {}, func(int, int) {})

	if gotDuration != 4 {
		t.Errorf("durationSeconds = %v, want 4 (fallback: response fps<=0 is unusable)", gotDuration)
	}
}

// ---------------------------------------------------------------------------
// Engine marker-mode state: getActiveMarkerFrame / setActiveMarker /
// clearActiveMarker / updateActiveMarkerDuration / updateActiveMarkerColor
// / markerModeStatus
// ---------------------------------------------------------------------------

func TestEngine_ActiveMarkerFrame_DefaultInactive(t *testing.T) {
	eng := newTestEngine(t)
	if got := eng.getActiveMarkerFrame(); got != -1 {
		t.Errorf("default activeMarkerFrame = %d, want -1 (inactive)", got)
	}
}

func TestEngine_SetActiveMarker_ThenGetActiveMarkerFrame(t *testing.T) {
	eng := newTestEngine(t)
	eng.setActiveMarker(25, "Blue", 1.0)
	if got := eng.getActiveMarkerFrame(); got != 25 {
		t.Errorf("activeMarkerFrame = %d, want 25", got)
	}
}

// TestEngine_SetActiveMarker_SecondCallOverwritesFirst proves the
// "each C1 press creates a fresh marker + makes it active" design rule
// at the state layer.
func TestEngine_SetActiveMarker_SecondCallOverwritesFirst(t *testing.T) {
	eng := newTestEngine(t)
	eng.setActiveMarker(10, "Blue", 1.0)
	eng.setActiveMarker(20, "Red", 2.0)
	if got := eng.getActiveMarkerFrame(); got != 20 {
		t.Errorf("activeMarkerFrame = %d, want 20 (second C1 press wins)", got)
	}
	want := "active @2.0s Red"
	if got := eng.markerModeStatus(); got != want {
		t.Errorf("markerModeStatus() = %q, want %q", got, want)
	}
}

func TestEngine_ClearActiveMarker(t *testing.T) {
	eng := newTestEngine(t)
	eng.setActiveMarker(25, "Blue", 1.0)
	eng.clearActiveMarker()
	if got := eng.getActiveMarkerFrame(); got != -1 {
		t.Errorf("activeMarkerFrame after clear = %d, want -1", got)
	}
}

func TestEngine_MarkerModeStatus_EmptyWhenInactive(t *testing.T) {
	eng := newTestEngine(t)
	if got := eng.markerModeStatus(); got != "" {
		t.Errorf("markerModeStatus() = %q, want empty when inactive", got)
	}
}

func TestEngine_MarkerModeStatus_FormatsActiveMarker(t *testing.T) {
	eng := newTestEngine(t)
	eng.setActiveMarker(25, "Blue", 1.5)
	want := "active @1.5s Blue"
	if got := eng.markerModeStatus(); got != want {
		t.Errorf("markerModeStatus() = %q, want %q", got, want)
	}
}

func TestEngine_UpdateActiveMarkerDuration_NoOpWhenInactive(t *testing.T) {
	eng := newTestEngine(t)
	eng.updateActiveMarkerDuration(9.9) // no active marker -- must be a no-op
	if got := eng.markerModeStatus(); got != "" {
		t.Errorf("markerModeStatus() = %q, want empty (update must not resurrect an inactive marker)", got)
	}
}

func TestEngine_UpdateActiveMarkerDuration_UpdatesWhenActive(t *testing.T) {
	eng := newTestEngine(t)
	eng.setActiveMarker(25, "Blue", 1.0)
	eng.updateActiveMarkerDuration(2.4)
	want := "active @2.4s Blue"
	if got := eng.markerModeStatus(); got != want {
		t.Errorf("markerModeStatus() = %q, want %q", got, want)
	}
}

func TestEngine_UpdateActiveMarkerColor_NoOpWhenInactive(t *testing.T) {
	eng := newTestEngine(t)
	eng.updateActiveMarkerColor("Red")
	if got := eng.markerModeStatus(); got != "" {
		t.Errorf("markerModeStatus() = %q, want empty", got)
	}
}

func TestEngine_UpdateActiveMarkerColor_UpdatesWhenActive(t *testing.T) {
	eng := newTestEngine(t)
	eng.setActiveMarker(25, "Blue", 1.0)
	eng.updateActiveMarkerColor("Red")
	want := "active @1.0s Red"
	if got := eng.markerModeStatus(); got != want {
		t.Errorf("markerModeStatus() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration: the whole marker-mode state
// machine (enter -> resize -> color -> exit), plus regressions proving
// marker mode ON/OFF states don't bleed into each other.
// ---------------------------------------------------------------------------

func TestMarkerMode_C1Press_EntersModeWithReturnedFrame(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"49":"C1"}}`
	bindings := `{
		"col":  {"C1": {"type": "key", "key": "Alt+S"}},
		"edit": {"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": true, "frame": 42},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 49, 100), 0) // C1 press

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveMarkerFrame() < 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveMarkerFrame(); got != 42 {
		t.Fatalf("activeMarkerFrame after C1 press = %d, want 42", got)
	}
	// Status.MarkerMode is only refreshed once per 50ms drain tick (see
	// Start()'s per-tick status snapshot) -- poll rather than assert
	// immediately, same pattern as TestEngine_Status_ActiveClip_PopulatedAfterD1Rotation.
	want := "active @1.0s Blue"
	var gotStatus string
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gotStatus = eng.Status().MarkerMode
		if gotStatus == want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if gotStatus != want {
		t.Errorf("Status.MarkerMode = %q, want %q", gotStatus, want)
	}
}

func TestMarkerMode_C1Press_AddMarkerFails_DoesNotEnterMode(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"49":"C1"}}`
	bindings := `{
		"col":  {"C1": {"type": "key", "key": "Alt+S"}},
		"edit": {"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": false, "err": "marker_exists", "frame": 42},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 49, 100), 0) // C1 press: add_marker fails

	waitForRecs(t, received, 1)
	time.Sleep(100 * time.Millisecond)
	if got := eng.getActiveMarkerFrame(); got != -1 {
		t.Errorf("activeMarkerFrame = %d, want -1 (marker creation failed, must not enter mode)", got)
	}
}

func TestMarkerMode_ControlDial_WhenActive_SendsResizeMarker_NotJog(t *testing.T) {
	deviceMap := `{"knobs":{"48":"ControlDial"},"sliders":{},"buttons":{"49":"C1"}}`
	bindings := `{
		"col":  {"C1": {"type": "key", "key": "Alt+S"}},
		"edit": {
			"ControlDial": {"type": "jog", "maxDelta": 90},
			"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"}
		}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": true, "frame": 42},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 49, 100), 0) // C1: enter marker mode

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveMarkerFrame() < 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveMarkerFrame(); got != 42 {
		t.Fatalf("activeMarkerFrame = %d, want 42 (test precondition)", got)
	}

	cb(midi.ControlChange(0, 48, 3), 0) // ControlDial turn, CW

	deadline = time.Now().Add(2 * time.Second)
	var sawResize bool
	for time.Now().Before(deadline) {
		for _, r := range received() {
			if r["op"] == "resize_marker" {
				sawResize = true
			}
			if r["op"] == "jog" {
				t.Fatalf("jog op sent while marker mode active, want resize_marker only: %v", received())
			}
		}
		if sawResize {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sawResize {
		t.Fatalf("no resize_marker op sent, received=%v", received())
	}
}

// TestMarkerMode_ControlDial_WhenInactive_SendsJog_Unchanged is the core
// regression: with marker mode never entered, the ControlDial's jog
// behavior (incl. the speed/sustain tiers, unmodified by this feature)
// must be byte-identical to before.
func TestMarkerMode_ControlDial_WhenInactive_SendsJog_Unchanged(t *testing.T) {
	deviceMap := `{"knobs":{"48":"ControlDial"},"sliders":{},"buttons":{"49":"C1"}}`
	bindings := `{
		"col":  {},
		"edit": {
			"ControlDial": {"type": "jog", "maxDelta": 90},
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
	cb(midi.ControlChange(0, 48, 3), 0) // ControlDial turn, no prior C1 press

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog" {
		t.Fatalf("op = %v, want jog (marker mode never entered -- ControlDial must behave exactly as before)", recs[0]["op"])
	}
}

func TestMarkerMode_ControlDialPress_ExitsMode(t *testing.T) {
	deviceMap := `{"knobs":{"48":"ControlDial"},"sliders":{},"buttons":{"49":"C1"}}`
	bindings := `{
		"col":  {"C1": {"type": "key", "key": "Alt+S"}},
		"edit": {
			"ControlDial": {"type": "jog", "maxDelta": 90},
			"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"}
		}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": true, "frame": 42},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 49, 100), 0) // C1: enter marker mode

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveMarkerFrame() < 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveMarkerFrame(); got != 42 {
		t.Fatalf("activeMarkerFrame = %d, want 42 (test precondition)", got)
	}

	cb(midi.NoteOn(0, 48, 100), 0) // ControlDial press: exit marker mode

	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveMarkerFrame() >= 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveMarkerFrame(); got != -1 {
		t.Fatalf("activeMarkerFrame after ControlDial press = %d, want -1 (exited)", got)
	}
	// Status.MarkerMode is only refreshed once per 50ms drain tick -- poll
	// rather than assert immediately (same rationale as the C1-entry test
	// above).
	var gotStatus string
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gotStatus = eng.Status().MarkerMode
		if gotStatus == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if gotStatus != "" {
		t.Errorf("Status.MarkerMode after exit = %q, want empty", gotStatus)
	}
}

// TestMarkerMode_ControlDialPress_NotActive_NoOp is the regression for
// TestNoteOnPageAware_EditPage_JogKnobClick_NoSidecarOp (editpage_test.go)
// under this feature's new direct-intercept code path: with marker mode
// never entered, a ControlDial press must remain a harmless no-op.
func TestMarkerMode_ControlDialPress_NotActive_NoOp(t *testing.T) {
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
	cb(midi.NoteOn(0, 48, 100), 0) // ControlDial press, marker mode never entered

	time.Sleep(150 * time.Millisecond)
	if len(received()) != 0 {
		t.Errorf("ControlDial press must stay a no-op when marker mode was never entered, got %v", received())
	}
	if got := eng.getActiveMarkerFrame(); got != -1 {
		t.Errorf("activeMarkerFrame = %d, want -1 (unchanged)", got)
	}
}

// ---------------------------------------------------------------------------
// Esc / BeforeAfter explicit marker-mode exit (2026-08-25, the
// "marker-mode trap" fix). Third way out, alongside the ControlDial press
// tested above and a P-button same-colour press (marker_test.go) --
// requested because the live incident showed both existing exits are easy
// to miss under pressure, leaving the ControlDial silently repurposed as a
// marker resizer for hours.
// ---------------------------------------------------------------------------

// TestMarkerMode_EscBeforeAfter_ExitsMode mirrors
// TestMarkerMode_ControlDialPress_ExitsMode exactly, just via BeforeAfter
// (note 102, bound as the Edit page's Escape key) instead of the
// ControlDial press.
func TestMarkerMode_EscBeforeAfter_ExitsMode(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"49":"C1","102":"BeforeAfter"}}`
	bindings := `{
		"col":  {"C1": {"type": "key", "key": "Alt+S"}},
		"edit": {
			"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"},
			"BeforeAfter": {"type": "key", "key": "Escape", "label": "Esc"}
		}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": true, "frame": 42},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 49, 100), 0) // C1: enter marker mode

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveMarkerFrame() < 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveMarkerFrame(); got != 42 {
		t.Fatalf("activeMarkerFrame = %d, want 42 (test precondition)", got)
	}

	cb(midi.NoteOn(0, 102, 100), 0) // BeforeAfter/Esc: exit marker mode

	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveMarkerFrame() >= 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveMarkerFrame(); got != -1 {
		t.Fatalf("activeMarkerFrame after Esc = %d, want -1 (exited)", got)
	}
}

// TestMarkerMode_EscBeforeAfter_NotActive_NoOp proves BeforeAfter/Esc stays
// a harmless no-op (on the marker-mode-state side; its own ordinary
// Escape keystroke is unaffected either way, and is not observable via the
// sidecar) when marker mode was never entered.
func TestMarkerMode_EscBeforeAfter_NotActive_NoOp(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"102":"BeforeAfter"}}`
	bindings := `{
		"col":  {},
		"edit": {"BeforeAfter": {"type": "key", "key": "Escape", "label": "Esc"}}
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
	cb(midi.NoteOn(0, 102, 100), 0) // BeforeAfter/Esc, marker mode never entered

	time.Sleep(150 * time.Millisecond)
	if len(received()) != 0 {
		t.Errorf("Esc must not touch the sidecar when marker mode was never entered, got %v", received())
	}
	if got := eng.getActiveMarkerFrame(); got != -1 {
		t.Errorf("activeMarkerFrame = %d, want -1 (unchanged)", got)
	}
}

// TestMarkerMode_EscBeforeAfter_HeldModifier_NoExit proves the `mod == ""`
// scope: a held-modifier chord on the SAME physical button (e.g.
// edit-shift.BeforeAfter = "Render in place") is a deliberately different
// action and must not be conflated with the plain-Esc marker-mode exit just
// because it shares a control name.
func TestMarkerMode_EscBeforeAfter_HeldModifier_NoExit(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"49":"C1","102":"BeforeAfter"}}`
	bindings := `{
		"col":  {"C1": {"type": "key", "key": "Alt+S"}},
		"edit": {
			"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"},
			"BeforeAfter": {"type": "key", "key": "Escape", "label": "Esc"}
		},
		"edit-shift": {"BeforeAfter": {"type": "key", "key": "Cmd+Alt+Shift+R", "label": "Render in place"}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, _ := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": true, "frame": 42},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 49, 100), 0) // C1: enter marker mode

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveMarkerFrame() < 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveMarkerFrame(); got != 42 {
		t.Fatalf("activeMarkerFrame = %d, want 42 (test precondition)", got)
	}

	eng.setHeldOverlay("shift")
	cb(midi.NoteOn(0, 102, 100), 0) // Shift+BeforeAfter: "Render in place" chord, NOT Esc

	time.Sleep(150 * time.Millisecond)
	if got := eng.getActiveMarkerFrame(); got != 42 {
		t.Errorf("activeMarkerFrame after held-modifier BeforeAfter = %d, want 42 (a chord on this control must not exit marker mode)", got)
	}
}

func TestMarkerMode_PButton_SetsColor_WhenActive(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"49":"C1","84":"P5btn"}}`
	bindings := `{
		"col":  {"C1": {"type": "key", "key": "Alt+S"}},
		"edit": {
			"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"},
			"P5btn": {"type": "markerColor", "markerColor": "Red"}
		}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startFixedOpReplySidecar(t, "edit", map[string]map[string]any{
		"add_marker": {"ok": true, "frame": 42},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 49, 100), 0) // C1: enter marker mode

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveMarkerFrame() < 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	cb(midi.NoteOn(0, 84, 100), 0) // P5btn: set color Red

	deadline = time.Now().Add(2 * time.Second)
	var sawSetColor bool
	for time.Now().Before(deadline) {
		for _, r := range received() {
			if r["op"] == "set_marker_color" && r["color"] == "Red" {
				sawSetColor = true
			}
		}
		if sawSetColor {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !sawSetColor {
		t.Fatalf("no set_marker_color(Red) op sent, received=%v", received())
	}

	want := "active @1.0s Red"
	var gotStatus string
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gotStatus = eng.Status().MarkerMode
		if gotStatus == want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if gotStatus != want {
		t.Errorf("Status.MarkerMode = %q, want %q", gotStatus, want)
	}
}

func TestMarkerMode_PButton_NoOp_WhenInactive(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"84":"P5btn"}}`
	bindings := `{
		"col":  {},
		"edit": {"P5btn": {"type": "markerColor", "markerColor": "Red"}}
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
	cb(midi.NoteOn(0, 84, 100), 0) // P5btn press, marker mode never entered

	time.Sleep(150 * time.Millisecond)
	if len(received()) != 0 {
		t.Errorf("P5btn press must be a no-op when marker mode inactive, got %v", received())
	}
}

// TestMarkerMode_PButton_ColorPage_NoOp proves a P-button press on the
// Color page (where P-buttons have no col-bank binding at all -- only
// the sliders P1-P8 exist there, a different control) stays inert, same
// posture as C1's Color-page addMarker regression test.
func TestMarkerMode_PButton_ColorPage_NoOp(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"84":"P5btn"}}`
	bindings := `{
		"col":  {},
		"edit": {"P5btn": {"type": "markerColor", "markerColor": "Red"}}
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
	cb(midi.NoteOn(0, 84, 100), 0)

	time.Sleep(150 * time.Millisecond)
	if len(received()) != 0 {
		t.Errorf("P5btn press on Color page must be a no-op, got %v", received())
	}
}
