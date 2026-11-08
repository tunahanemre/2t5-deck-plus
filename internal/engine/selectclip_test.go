package engine

// Tests for the D1 "clip-select" feature:
//   - Engine.getActiveTrack/setActiveTrack (mutex-guarded state, persists
//     across Start/Stop like target.State)
//   - parseClipsAtPlayhead (pure decode of the sidecar's clips_at_playhead
//     response)
//   - nextTrackIndex (pure rotation math: clamp-to-ends, no wrap)
//   - dispatchSelectClip (the D1 knob's drain-loop handler: queries
//     clips_at_playhead, moves by sign(step), sets the new active track)
//   - processNonBalanceDrain/dispatchNoteOn's new activeTrack param,
//     passed through to bump_property/reset_property as "track"
//   - full Start()/MIDI drain-loop integration: D1 CC events actually
//     rotate Engine.activeTrack
//   - Status.ActiveClip populated from the active track via the cached
//     query
//
// All hermetic: no live Resolve, fake unix-socket sidecars only, no
// DECKPLUS_ARMED.
//
// Every D1 binding below that drives a full Start()/MIDI drain-loop
// integration test sets "detentThreshold": 1, restoring the original
// "fire on every raw CC step" behavior (see selectClipDetent's doc
// comment in engine.go) -- this file's tests are about rotation/clamp/
// release/status-display behavior, not the sensitivity curve itself
// (that lives in selectclip_sensitivity_test.go), and each simulated
// turn below is exactly one raw MIDI CC step.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"reflect"
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

// startClipsSidecar spins up a fake unix-socket sidecar (same frame
// protocol as startRecordingSidecar in editpage_test.go) that additionally
// replies to "clips_at_playhead" with {"ok":true,"clips":clips}. Kept as a
// separate helper (rather than extending startRecordingSidecar) to avoid
// touching editpage_test.go's existing, already-covered helper.
func startClipsSidecar(t *testing.T, page string, clips []any) (sockPath string, received func() []map[string]any) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-clips-*.sock")
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
					if op != "status" {
						mu.Lock()
						recs = append(recs, m)
						mu.Unlock()
					}

					var reply []byte
					switch op {
					case "status":
						reply, _ = json.Marshal(map[string]any{"ok": true, "page": page})
					case "clips_at_playhead":
						reply, _ = json.Marshal(map[string]any{"ok": true, "clips": clips})
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

// startFailingClipsSidecar replies {"ok":false,"err":failErr} to
// clips_at_playhead unconditionally (and {"ok":true,"page":page} to status)
// -- for testing dispatchSelectClip's/queryActiveClip's error-handling path.
func startFailingClipsSidecar(t *testing.T, page, failErr string) (sockPath string, received func() []map[string]any) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-clipsfail-*.sock")
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
					if op != "status" {
						mu.Lock()
						recs = append(recs, m)
						mu.Unlock()
					}

					var reply []byte
					switch op {
					case "status":
						reply, _ = json.Marshal(map[string]any{"ok": true, "page": page})
					case "clips_at_playhead":
						reply, _ = json.Marshal(map[string]any{"ok": false, "err": failErr})
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
// Engine.getActiveTrack / setActiveTrack
// ---------------------------------------------------------------------------

func TestEngine_ActiveTrack_DefaultZero(t *testing.T) {
	eng := newTestEngine(t)
	if got := eng.getActiveTrack(); got != 0 {
		t.Errorf("getActiveTrack() = %d, want 0 (unset/auto)", got)
	}
}

func TestEngine_SetGetActiveTrack(t *testing.T) {
	eng := newTestEngine(t)
	eng.setActiveTrack(4)
	if got := eng.getActiveTrack(); got != 4 {
		t.Errorf("getActiveTrack() = %d, want 4", got)
	}
	eng.setActiveTrack(2)
	if got := eng.getActiveTrack(); got != 2 {
		t.Errorf("getActiveTrack() after second set = %d, want 2", got)
	}
}

func TestEngine_ActiveTrack_ConcurrentSetGet(t *testing.T) {
	eng := newTestEngine(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			eng.setActiveTrack(i % 8)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = eng.getActiveTrack()
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// parseClipsAtPlayhead
// ---------------------------------------------------------------------------

func TestParseClipsAtPlayhead_ValidResponse(t *testing.T) {
	resp := map[string]any{
		"ok": true,
		"clips": []any{
			map[string]any{"track": float64(1), "name": "A.mov"},
			map[string]any{"track": float64(3), "name": "C.mov"},
		},
	}
	ok, clips := parseClipsAtPlayhead(resp)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []clipEntry{{Track: 1, Name: "A.mov"}, {Track: 3, Name: "C.mov"}}
	if !reflect.DeepEqual(clips, want) {
		t.Errorf("clips = %+v, want %+v", clips, want)
	}
}

func TestParseClipsAtPlayhead_NotOk(t *testing.T) {
	resp := map[string]any{"ok": false, "err": "no_timeline"}
	ok, clips := parseClipsAtPlayhead(resp)
	if ok {
		t.Error("ok = true, want false")
	}
	if clips != nil {
		t.Errorf("clips = %v, want nil", clips)
	}
}

func TestParseClipsAtPlayhead_NilResponse(t *testing.T) {
	ok, clips := parseClipsAtPlayhead(nil)
	if ok || clips != nil {
		t.Errorf("got ok=%v clips=%v, want false/nil", ok, clips)
	}
}

func TestParseClipsAtPlayhead_MissingClipsKey(t *testing.T) {
	resp := map[string]any{"ok": true}
	ok, clips := parseClipsAtPlayhead(resp)
	if !ok {
		t.Error("ok = false, want true")
	}
	if len(clips) != 0 {
		t.Errorf("clips = %v, want empty", clips)
	}
}

func TestParseClipsAtPlayhead_MalformedEntrySkipped(t *testing.T) {
	resp := map[string]any{
		"ok": true,
		"clips": []any{
			"not-a-map",
			map[string]any{"track": float64(2), "name": "B.mov"},
		},
	}
	ok, clips := parseClipsAtPlayhead(resp)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []clipEntry{{Track: 2, Name: "B.mov"}}
	if !reflect.DeepEqual(clips, want) {
		t.Errorf("clips = %+v, want %+v", clips, want)
	}
}

// ---------------------------------------------------------------------------
// nextTrackIndex
// ---------------------------------------------------------------------------

func TestNextTrackIndex(t *testing.T) {
	clips := []clipEntry{{Track: 1}, {Track: 2}, {Track: 3}}
	cases := []struct {
		name    string
		current int
		dir     int
		want    int
	}{
		{"unset, up -> first", 0, 1, 0},
		{"unset, down -> last", 0, -1, 2},
		{"middle, up", 2, 1, 2},
		{"middle, down", 2, -1, 0},
		{"top, up clamps at top (no wrap)", 3, 1, 2},
		{"bottom, down clamps at bottom (no wrap)", 1, -1, 0},
		{"not-in-list, up -> first", 99, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextTrackIndex(clips, tc.current, tc.dir); got != tc.want {
				t.Errorf("nextTrackIndex(current=%d, dir=%d) = %d, want %d", tc.current, tc.dir, got, tc.want)
			}
		})
	}
}

func TestNextTrackIndex_EmptyClips(t *testing.T) {
	if got := nextTrackIndex(nil, 0, 1); got != -1 {
		t.Errorf("nextTrackIndex(empty) = %d, want -1 (sentinel; caller must check len before indexing)", got)
	}
}

// ---------------------------------------------------------------------------
// dispatchSelectClip
// ---------------------------------------------------------------------------

func TestDispatchSelectClip_MovesUpFromUnset_SelectsFirstOccupiedTrack(t *testing.T) {
	sockPath, received := startClipsSidecar(t, "edit", []any{
		map[string]any{"track": 1, "name": "A.mov"},
		map[string]any{"track": 3, "name": "C.mov"},
	})
	client := dialRecordingClient(t, sockPath)

	active := 0
	dispatchSelectClip(1, client, func() int { return active }, func(v int) { active = v })

	waitForRecs(t, received, 1)
	if active != 1 {
		t.Errorf("active = %d, want 1 (first occupied track)", active)
	}
}

func TestDispatchSelectClip_MovesUpFromMiddle(t *testing.T) {
	sockPath, received := startClipsSidecar(t, "edit", []any{
		map[string]any{"track": 1, "name": "A.mov"},
		map[string]any{"track": 2, "name": "B.mov"},
		map[string]any{"track": 3, "name": "C.mov"},
	})
	client := dialRecordingClient(t, sockPath)

	active := 2
	dispatchSelectClip(1, client, func() int { return active }, func(v int) { active = v })

	waitForRecs(t, received, 1)
	if active != 3 {
		t.Errorf("active = %d, want 3", active)
	}
}

func TestDispatchSelectClip_MovesDownFromMiddle(t *testing.T) {
	sockPath, received := startClipsSidecar(t, "edit", []any{
		map[string]any{"track": 1, "name": "A.mov"},
		map[string]any{"track": 2, "name": "B.mov"},
		map[string]any{"track": 3, "name": "C.mov"},
	})
	client := dialRecordingClient(t, sockPath)

	active := 2
	dispatchSelectClip(-1, client, func() int { return active }, func(v int) { active = v })

	waitForRecs(t, received, 1)
	if active != 1 {
		t.Errorf("active = %d, want 1", active)
	}
}

func TestDispatchSelectClip_ClampsAtTop_NoWrap(t *testing.T) {
	// Bidirectional, clamped, no-wrap stepping (user-confirmed desired
	// behavior): dialing UP past the top clip stops ON the top clip
	// rather than jumping to the bottom.
	sockPath, received := startClipsSidecar(t, "edit", []any{
		map[string]any{"track": 1, "name": "A.mov"},
		map[string]any{"track": 2, "name": "B.mov"},
	})
	client := dialRecordingClient(t, sockPath)

	active := 2 // already at the top
	dispatchSelectClip(1, client, func() int { return active }, func(v int) { active = v })

	waitForRecs(t, received, 1)
	if active != 2 {
		t.Errorf("active = %d, want 2 (clamped at top, no wrap to bottom)", active)
	}
}

func TestDispatchSelectClip_ClampsAtBottom_NoWrap(t *testing.T) {
	// Mirror of the top case: dialing DOWN past the bottom clip stops ON
	// the bottom clip rather than jumping to the top.
	sockPath, received := startClipsSidecar(t, "edit", []any{
		map[string]any{"track": 1, "name": "A.mov"},
		map[string]any{"track": 2, "name": "B.mov"},
	})
	client := dialRecordingClient(t, sockPath)

	active := 1 // already at the bottom
	dispatchSelectClip(-1, client, func() int { return active }, func(v int) { active = v })

	waitForRecs(t, received, 1)
	if active != 1 {
		t.Errorf("active = %d, want 1 (clamped at bottom, no wrap to top)", active)
	}
}

func TestDispatchSelectClip_EmptyOccupiedList_NoOp(t *testing.T) {
	sockPath, received := startClipsSidecar(t, "edit", []any{})
	client := dialRecordingClient(t, sockPath)

	setCalled := false
	dispatchSelectClip(1, client, func() int { return 0 }, func(v int) { setCalled = true })

	waitForRecs(t, received, 1) // the clips_at_playhead call itself IS recorded
	if setCalled {
		t.Error("setActiveTrack should not be called when the occupied list is empty")
	}
}

func TestDispatchSelectClip_SidecarNotOk_NoOp(t *testing.T) {
	sockPath, received := startFailingClipsSidecar(t, "edit", "no_timeline")
	client := dialRecordingClient(t, sockPath)

	setCalled := false
	dispatchSelectClip(1, client, func() int { return 0 }, func(v int) { setCalled = true })

	waitForRecs(t, received, 1)
	if setCalled {
		t.Error("setActiveTrack should not be called when clips_at_playhead returns ok=false")
	}
}

func TestDispatchSelectClip_NilClient_NoPanicNoOp(t *testing.T) {
	setCalled := false
	dispatchSelectClip(1, nil, func() int { return 0 }, func(v int) { setCalled = true })
	if setCalled {
		t.Error("setActiveTrack should not be called with a nil client")
	}
}

func TestDispatchSelectClip_ZeroStep_NoOp(t *testing.T) {
	sockPath, received := startClipsSidecar(t, "edit", []any{
		map[string]any{"track": 1, "name": "A.mov"},
	})
	client := dialRecordingClient(t, sockPath)

	setCalled := false
	dispatchSelectClip(0, client, func() int { return 0 }, func(v int) { setCalled = true })

	time.Sleep(100 * time.Millisecond)
	if len(received()) != 0 {
		t.Errorf("step=0 should never call the sidecar, got %v", received())
	}
	if setCalled {
		t.Error("setActiveTrack should not be called for step=0")
	}
}

// ---------------------------------------------------------------------------
// processNonBalanceDrain / dispatchNoteOn — activeTrack passthrough
// ---------------------------------------------------------------------------

func TestProcessNonBalanceDrain_Property_ActiveTrackSet_IncludesTrackKey(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "property", Property: "Pan", Sens: 2.0}
	processNonBalanceDrain("Contrast", 3, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 4, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["track"].(float64); got != 4 {
		t.Errorf("track = %v, want 4", got)
	}
}

func TestProcessNonBalanceDrain_Property_ActiveTrackZero_OmitsTrackKey(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "property", Property: "Pan", Sens: 2.0}
	processNonBalanceDrain("Contrast", 3, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 0, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if _, has := recs[0]["track"]; has {
		t.Errorf("track key should be absent when activeTrack=0 (auto), got %v", recs[0]["track"])
	}
}

func TestProcessNonBalanceDrain_Jog_ActiveTrackSet_TrackKeyStillAbsent(t *testing.T) {
	// jog has no clip-target concept (it's a global playhead scrub) --
	// activeTrack must never leak into a jog command.
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "jog"}
	processNonBalanceDrain("ControlDial", 1, b, mode.New(), nil, client, nil, "edit", nil, func() string { return "edit" }, 4, 0)

	waitForRecs(t, received, 1)
	recs := received()
	if _, has := recs[0]["track"]; has {
		t.Errorf("jog command should never include track, got %v", recs[0])
	}
}

func TestDispatchNoteOn_Property_ActiveTrackSet_IncludesTrackInResetProperty(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "property", Property: "CropTop"}
	dispatchNoteOn("Temperature", b, mode.New(), nil, client, 1, 4, func() string { return "edit" }, nil, nil, nil, nil, nil, nil)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "reset_property" {
		t.Fatalf("op = %v, want reset_property", recs[0]["op"])
	}
	if got := recs[0]["track"].(float64); got != 4 {
		t.Errorf("track = %v, want 4", got)
	}
}

func TestDispatchNoteOn_Property_ActiveTrackZero_OmitsTrackKey(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	b := config.Binding{Type: "property", Property: "CropTop"}
	dispatchNoteOn("Temperature", b, mode.New(), nil, client, 1, 0, func() string { return "edit" }, nil, nil, nil, nil, nil, nil)

	waitForRecs(t, received, 1)
	recs := received()
	if _, has := recs[0]["track"]; has {
		t.Errorf("track key should be absent when activeTrack=0 (auto), got %v", recs[0]["track"])
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration: D1 rotates Engine.activeTrack
// ---------------------------------------------------------------------------

func TestDrainLoop_D1_SelectClip_RotatesActiveTrack(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	clips := []any{
		map[string]any{"track": 1, "name": "A.mov"},
		map[string]any{"track": 2, "name": "B.mov"},
		map[string]any{"track": 3, "name": "C.mov"},
	}
	sockPath, _ := startClipsSidecar(t, "edit", clips)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0) // D1 up: unset -> track 1

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 1 {
		t.Fatalf("activeTrack after first D1 up-turn = %d, want 1", got)
	}

	// Give the drain loop a full tick to settle before the next MIDI event
	// so the two turns land in SEPARATE 50ms drain ticks (one selectClip
	// rotation per detent, not summed).
	time.Sleep(80 * time.Millisecond)
	cb(midi.ControlChange(0, 41, 1), 0) // D1 up again: track 1 -> track 2

	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 2 {
		t.Fatalf("activeTrack after second D1 up-turn = %d, want 2", got)
	}

	time.Sleep(80 * time.Millisecond)
	cb(midi.ControlChange(0, 41, 127), 0) // D1 down: track 2 -> track 1

	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 1 {
		t.Fatalf("activeTrack after D1 down-turn = %d, want 1", got)
	}
}

func TestDrainLoop_D1_SelectClip_ColorPage_NoOp_ByteIdentical(t *testing.T) {
	// D1 has no "col" binding at all (config/bindings.json only defines it
	// in "edit") -- confirms turning D1 on the Color page is a harmless
	// no-op, same as any unbound control.
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startClipsSidecar(t, "color", []any{
		map[string]any{"track": 1, "name": "A.mov"},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0)

	time.Sleep(150 * time.Millisecond)
	if got := eng.getActiveTrack(); got != 0 {
		t.Errorf("activeTrack = %d, want 0 (D1 must be a no-op on the Color page)", got)
	}
	for _, r := range received() {
		if r["op"] == "clips_at_playhead" {
			t.Errorf("clips_at_playhead should never be sent while page=color, got %v", r)
		}
	}
}

// ---------------------------------------------------------------------------
// Status.ActiveClip — populated from the active track via the cached query
// ---------------------------------------------------------------------------

func TestEngine_Status_ActiveClip_EmptyByDefault_BeforeD1Use(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, _ := startClipsSidecar(t, "edit", []any{
		map[string]any{"track": 1, "name": "A.mov"},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	time.Sleep(80 * time.Millisecond)
	if got := eng.Status().ActiveClip; got != "" {
		t.Errorf("ActiveClip before any D1 use = %q, want empty", got)
	}
}

func TestEngine_Status_ActiveClip_PopulatedAfterD1Rotation(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	clips := []any{
		map[string]any{"track": 1, "name": "A.mov"},
		map[string]any{"track": 4, "name": "DSCF9804.MOV"},
	}
	sockPath, _ := startClipsSidecar(t, "edit", clips)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0) // D1 up: unset -> track 1
	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	time.Sleep(80 * time.Millisecond)   // settle into a fresh drain tick
	cb(midi.ControlChange(0, 41, 1), 0) // D1 up: track 1 -> track 4
	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() != 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 4 {
		t.Fatalf("activeTrack = %d, want 4 (test precondition)", got)
	}

	want := "V4 · DSCF9804.MOV"
	var got string
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got = eng.Status().ActiveClip
		if got == want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got != want {
		t.Fatalf("ActiveClip = %q, want %q", got, want)
	}
}

func TestEngine_Status_ActiveClip_EmptyOnNonEditPage_EvenIfTrackSet(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)
	eng.setActiveTrack(1) // simulate a prior D1 selection surviving a page switch

	sockPath, _ := startClipsSidecar(t, "color", []any{
		map[string]any{"track": 1, "name": "A.mov"},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	time.Sleep(150 * time.Millisecond)
	if got := eng.Status().ActiveClip; got != "" {
		t.Errorf("ActiveClip while page=color = %q, want empty (Edit-page-only display)", got)
	}
}

// ---------------------------------------------------------------------------
// D1 knob-press "release / normalize" — the mirror action to D1's TURN
// (dispatchSelectClip/setActiveTrack): pressing D1 on the Edit page clears
// the pinned active-clip target back to "auto" and sets EVERY occupied
// video clip under the playhead to a uniform release color (default
// "Blue", from _config.releaseColor) via the sidecar's set_clips_color op
// -- a clean normalize/deselect gesture. This REPLACED the earlier
// clear_highlight-based "restore the one tinted clip" behavior;
// clear_highlight itself is UNCHANGED and still sent by engine Stop().
//   - isSelectClipRelease (pure predicate: page + binding type check)
//   - dispatchSelectClipRelease (the actual action: setActiveTrack(0) +
//     best-effort set_clips_color send with the configured release color)
//   - full Start()/MIDI drain-loop integration via note_on
// ---------------------------------------------------------------------------

func TestIsSelectClipRelease(t *testing.T) {
	cfg := loadConfig(t, `{
		"col": {},
		"edit": {
			"D1": {"type": "selectClip", "detentThreshold": 1},
			"Contrast": {"type": "property", "property": "Pan"}
		}
	}`)
	cases := []struct {
		name    string
		page    string
		control string
		want    bool
	}{
		{"edit page, D1 selectClip binding", "edit", "D1", true},
		{"edit page, property control is NOT a release", "edit", "Contrast", false},
		{"edit page, control absent from edit bank", "edit", "Copy", false},
		{"color page, D1 present in edit bank but wrong page", "color", "D1", false},
		{"cut page, D1", "cut", "D1", false},
		{"empty page, D1", "", "D1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSelectClipRelease(cfg, c.page, 1, c.control, ""); got != c.want {
				t.Errorf("isSelectClipRelease(page=%q, control=%q) = %v, want %v", c.page, c.control, got, c.want)
			}
		})
	}
}

func TestDispatchSelectClipRelease_SetsActiveTrackZero_SendsSetClipsColor(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	gotTrack := -1
	setActiveTrack := func(track int) { gotTrack = track }

	dispatchSelectClipRelease(client, setActiveTrack, "Blue")

	if gotTrack != 0 {
		t.Errorf("setActiveTrack called with %d, want 0 (release -> auto)", gotTrack)
	}
	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "set_clips_color" {
		t.Fatalf("op = %v, want set_clips_color", recs[0]["op"])
	}
	if recs[0]["color"] != "Blue" {
		t.Errorf("color = %v, want Blue", recs[0]["color"])
	}
}

func TestDispatchSelectClipRelease_PassesConfiguredColorThrough(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchSelectClipRelease(client, func(int) {}, "Orange")

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "set_clips_color" {
		t.Fatalf("op = %v, want set_clips_color", recs[0]["op"])
	}
	if recs[0]["color"] != "Orange" {
		t.Errorf("color = %v, want Orange (configured value must pass through unchanged)", recs[0]["color"])
	}
}

func TestDispatchSelectClipRelease_NilClient_NoPanic_StillSetsActiveTrackZero(t *testing.T) {
	gotTrack := -1
	setActiveTrack := func(track int) { gotTrack = track }

	dispatchSelectClipRelease(nil, setActiveTrack, "Blue") // must not panic

	if gotTrack != 0 {
		t.Errorf("setActiveTrack called with %d, want 0 (must still run with a nil client)", gotTrack)
	}
}

func TestNoteOnD1Release_EditPage_ClearsActiveTrackAndSendsSetClipsColor(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	clips := []any{
		map[string]any{"track": 1, "name": "A.mov"},
	}
	sockPath, received := startClipsSidecar(t, "edit", clips)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0) // D1 turn: unset -> track 1

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 1 {
		t.Fatalf("activeTrack after D1 turn = %d, want 1 (test precondition)", got)
	}

	cb(midi.NoteOn(0, 41, 100), 0) // D1 press: release/unselect

	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 0 {
		t.Fatalf("activeTrack after D1 press = %d, want 0 (release -> auto)", got)
	}

	var found bool
	var gotColor any
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range received() {
			if r["op"] == "set_clips_color" {
				found = true
				gotColor = r["color"]
				break
			}
		}
		if found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Errorf("no set_clips_color op sent after D1 press, received=%v", received())
	}
	if gotColor != "Blue" {
		t.Errorf("set_clips_color color = %v, want Blue (default releaseColor, unset in this test's bindings)", gotColor)
	}
}

// TestNoteOnD1Release_ColorPage_NoOp_ByteIdentical is the regression check:
// D1 has no "col" binding at all (same precondition as
// TestDrainLoop_D1_SelectClip_ColorPage_NoOp_ByteIdentical), so pressing D1
// on the Color page must stay a harmless no-op exactly like before this
// feature existed -- activeTrack untouched, no set_clips_color sent.
func TestNoteOnD1Release_ColorPage_NoOp_ByteIdentical(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startClipsSidecar(t, "color", []any{
		map[string]any{"track": 1, "name": "A.mov"},
	})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 41, 100), 0) // D1 press on Color page

	time.Sleep(150 * time.Millisecond)
	if got := eng.getActiveTrack(); got != 0 {
		t.Errorf("activeTrack = %d, want 0 (D1 press must stay a no-op on the Color page)", got)
	}
	for _, r := range received() {
		if r["op"] == "set_clips_color" {
			t.Errorf("set_clips_color should never be sent on the Color page, got %v", received())
		}
	}
}
