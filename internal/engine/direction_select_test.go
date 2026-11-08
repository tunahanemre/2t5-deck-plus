package engine

// Tests for the direction-button clip-selection feature (Up/Down/Left/
// Right/C5, Edit page) -- REPLACES D1's knob-turn selectClip rotation
// (D1's own machinery/tests in selectclip_test.go, highlight_test.go,
// selectclip_sensitivity_test.go are untouched and remain green -- they
// exercise code that is now unreachable in production via the real
// config/bindings.json, but is deliberately kept and still directly
// unit-tested, see config.Binding.IsSelectClip's doc comment):
//   - parseSelectionResult (pure decode of the sidecar's
//     select_clip_vertical/select_clip_horizontal response)
//   - isSelectClipVert / isSelectClipHorizControl / isSelectClipClear
//     (page-gated direct-intercept predicates)
//   - dispatchSelectClipVertical / dispatchSelectClipHorizontal /
//     dispatchSelectClipClear (the button handlers: sidecar op shape,
//     selection+activeTrack lockstep updates, no-op paths)
//   - Engine.getSelection/setSelection/clearSelection
//   - full Start()/MIDI drain-loop integration: Up/Down/Left/Right/C5
//     route to selection ONLY on the Edit page (Color page's Left/Right
//     still do node prev/next nav, byte-identical); a selection sets the
//     bump_property "track" override; the playhead-left-the-clip
//     auto-reset fires outside [start,end) but never within it; C5
//     clears + restores via clear_highlight
//
// All hermetic: no live Resolve, fake unix-socket sidecars only, no
// DECKPLUS_ARMED.

import (
	"context"
	"encoding/json"
	"fmt"
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

// startDirectionSelectSidecar spins up a fake unix-socket sidecar for this
// feature's tests. Records every non-"status"/"restore_highlight" command
// (same exclusion rationale as startRecordingSidecar in editpage_test.go).
// Replies to "status" with {"ok":true,"page":page,"playhead_frame":<current,
// settable via setPlayheadFrame -- omitted from the JSON entirely until the
// first setPlayheadFrame call, mirroring startStatusSequenceSidecar's
// clip_id-omitted-until-set convention, so a test can start with
// havePlayheadFrame==false>}. Replies to "select_clip_vertical"/
// "select_clip_horizontal" with a canned response settable via
// setVerticalResp/setHorizontalResp (default: not configured -- returns
// {"ok":false,"err":"not_configured"}, forcing every test to explicitly
// script what it expects). Everything else (clear_highlight, set_node,
// restore_highlight, ...) gets a plain {"ok":true}.
func startDirectionSelectSidecar(t *testing.T, page string) (
	sockPath string,
	received func() []map[string]any,
	setPlayheadFrame func(int),
	setVerticalResp func(map[string]any),
	setHorizontalResp func(map[string]any),
) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-dirsel-*.sock")
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
	var havePlayhead bool
	var playheadFrame int
	verticalResp := map[string]any{"ok": false, "err": "not_configured"}
	horizontalResp := map[string]any{"ok": false, "err": "not_configured"}

	setPlayheadFrame = func(v int) {
		mu.Lock()
		playheadFrame = v
		havePlayhead = true
		mu.Unlock()
	}
	setVerticalResp = func(r map[string]any) {
		mu.Lock()
		verticalResp = r
		mu.Unlock()
	}
	setHorizontalResp = func(r map[string]any) {
		mu.Lock()
		horizontalResp = r
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
						mu.Lock()
						resp := map[string]any{"ok": true, "page": page}
						if havePlayhead {
							resp["playhead_frame"] = playheadFrame
						}
						mu.Unlock()
						reply, _ = json.Marshal(resp)
					case "select_clip_vertical":
						mu.Lock()
						r := verticalResp
						mu.Unlock()
						reply, _ = json.Marshal(r)
					case "select_clip_horizontal":
						mu.Lock()
						r := horizontalResp
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
	}, setPlayheadFrame, setVerticalResp, setHorizontalResp
}

// findRec returns the first recorded command with the given "op", or nil.
func findRec(recs []map[string]any, op string) map[string]any {
	for _, r := range recs {
		if o, _ := r["op"].(string); o == op {
			return r
		}
	}
	return nil
}

// countRecs returns how many recorded commands have the given "op".
func countRecs(recs []map[string]any, op string) int {
	n := 0
	for _, r := range recs {
		if o, _ := r["op"].(string); o == op {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// parseSelectionResult
// ---------------------------------------------------------------------------

func TestParseSelectionResult_Success(t *testing.T) {
	resp := map[string]any{
		"ok": true, "err": nil, "track": 3.0, "unique_id": "uid-C",
		"start": 100.0, "end": 200.0, "original_color": "Orange",
	}
	r := parseSelectionResult(resp)
	if !r.ok || r.track != 3 || r.start != 100 || r.end != 200 {
		t.Errorf("parseSelectionResult = %+v, want ok=true track=3 start=100 end=200", r)
	}
}

func TestParseSelectionResult_Failure(t *testing.T) {
	resp := map[string]any{"ok": false, "err": "no_clip", "track": nil}
	r := parseSelectionResult(resp)
	if r.ok {
		t.Errorf("ok = true, want false")
	}
	if r.err != "no_clip" {
		t.Errorf("err = %q, want no_clip", r.err)
	}
	if r.track != 0 {
		t.Errorf("track = %d, want 0 (zero value on failure)", r.track)
	}
}

func TestParseSelectionResult_NilResponse(t *testing.T) {
	r := parseSelectionResult(nil)
	if r.ok || r.track != 0 || r.start != 0 || r.end != 0 {
		t.Errorf("parseSelectionResult(nil) = %+v, want zero value", r)
	}
}

// ---------------------------------------------------------------------------
// isSelectClipVert / isSelectClipHorizControl / isSelectClipClear
// ---------------------------------------------------------------------------

func TestIsSelectClipVert_PageAndBindingGated(t *testing.T) {
	cfg := loadTestConfig(t, `{
		"edit": {"Up": {"type": "selectClipVert"}, "Down": {"type": "selectClipVert"}}
	}`)
	cases := []struct {
		name    string
		page    string
		control string
		want    bool
	}{
		{"edit page, Up", "edit", "Up", true},
		{"edit page, Down", "edit", "Down", true},
		{"color page, Up present in edit bank but wrong page", "color", "Up", false},
		{"edit page, unbound control", "edit", "Left", false},
		{"empty page", "", "Up", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSelectClipVert(cfg, c.page, 1, c.control, ""); got != c.want {
				t.Errorf("isSelectClipVert(%q,%q) = %v, want %v", c.page, c.control, got, c.want)
			}
		})
	}
}

func TestIsSelectClipHorizControl_PageAndBindingGated(t *testing.T) {
	cfg := loadTestConfig(t, `{
		"edit": {"Left": {"type": "selectClipHoriz"}, "Right": {"type": "selectClipHoriz"}}
	}`)
	cases := []struct {
		name    string
		page    string
		control string
		want    bool
	}{
		{"edit page, Left", "edit", "Left", true},
		{"edit page, Right", "edit", "Right", true},
		{"color page, Left present in edit bank but wrong page", "color", "Left", false},
		{"cut page, Right", "cut", "Right", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSelectClipHorizControl(cfg, c.page, 1, c.control, ""); got != c.want {
				t.Errorf("isSelectClipHorizControl(%q,%q) = %v, want %v", c.page, c.control, got, c.want)
			}
		})
	}
}

func TestIsSelectClipClear_PageAndBindingGated(t *testing.T) {
	cfg := loadTestConfig(t, `{
		"edit": {"C5": {"type": "selectClipClear"}}
	}`)
	if !isSelectClipClear(cfg, "edit", 1, "C5", "") {
		t.Error("isSelectClipClear(edit, C5) = false, want true")
	}
	if isSelectClipClear(cfg, "color", 1, "C5", "") {
		t.Error("isSelectClipClear(color, C5) = true, want false (wrong page)")
	}
	if isSelectClipClear(cfg, "edit", 1, "C4", "") {
		t.Error("isSelectClipClear(edit, C4) = true, want false (unbound control)")
	}
}

// loadTestConfig builds a *config.Config from an inline bindings JSON
// fragment (mirrors internal/config's loadInline helper -- duplicated
// here, package-scoped to engine's own tests, rather than exporting
// config's private helper across package boundaries).
func loadTestConfig(t *testing.T, bindingsJSON string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/device-map.json", []byte(`{"knobs":{},"sliders":{},"buttons":{}}`), 0644); err != nil {
		t.Fatalf("write device-map.json: %v", err)
	}
	if err := os.WriteFile(dir+"/bindings.json", []byte(bindingsJSON), 0644); err != nil {
		t.Fatalf("write bindings.json: %v", err)
	}
	cfg, err := config.Load(dir+"/device-map.json", dir+"/bindings.json")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// dispatchSelectClipVertical
// ---------------------------------------------------------------------------

func TestDispatchSelectClipVertical_NilClient_NoOp(t *testing.T) {
	var sel clipSelection
	var track int
	got := dispatchSelectClipVertical(nil, "Up", 1, "Teal",
		func() clipSelection { return sel },
		func(s clipSelection) { sel = s },
		func(tr int) { track = tr },
	)
	if got != "" {
		t.Errorf("LastAction = %q, want \"\" (nil client)", got)
	}
	if sel.active || track != 0 {
		t.Errorf("state mutated on nil-client no-op: sel=%+v track=%d", sel, track)
	}
}

func TestDispatchSelectClipVertical_Success_UpdatesSelectionAndActiveTrack(t *testing.T) {
	sockPath, received, _, setVerticalResp, _ := startDirectionSelectSidecar(t, "edit")
	setVerticalResp(map[string]any{
		"ok": true, "track": 3.0, "unique_id": "uid-C",
		"start": 10.0, "end": 90.0, "original_color": "",
	})
	client := dialRecordingClient(t, sockPath)

	var sel clipSelection
	var track int
	got := dispatchSelectClipVertical(client, "Up", 1, "Teal",
		func() clipSelection { return sel },
		func(s clipSelection) { sel = s },
		func(tr int) { track = tr },
	)

	if got != "Up → V3" {
		t.Errorf("LastAction = %q, want %q", got, "Up → V3")
	}
	if !sel.active || sel.track != 3 || sel.start != 10 || sel.end != 90 {
		t.Errorf("selection = %+v, want active track=3 start=10 end=90", sel)
	}
	if track != 3 {
		t.Errorf("activeTrack = %d, want 3 (lockstep with selection)", track)
	}

	recs := received()
	rec := findRec(recs, "select_clip_vertical")
	if rec == nil {
		t.Fatalf("select_clip_vertical not sent: %v", recs)
	}
	if rec["direction"].(float64) != 1 {
		t.Errorf("direction = %v, want 1", rec["direction"])
	}
	if rec["current_track"].(float64) != 0 {
		t.Errorf("current_track = %v, want 0 (no prior selection)", rec["current_track"])
	}
	if rec["color"] != "Teal" {
		t.Errorf("color = %v, want Teal", rec["color"])
	}
}

func TestDispatchSelectClipVertical_SendsCurrentTrackWhenAlreadySelected(t *testing.T) {
	sockPath, received, _, setVerticalResp, _ := startDirectionSelectSidecar(t, "edit")
	setVerticalResp(map[string]any{"ok": true, "track": 4.0, "start": 0.0, "end": 50.0})
	client := dialRecordingClient(t, sockPath)

	sel := clipSelection{active: true, track: 2, start: 0, end: 50}
	dispatchSelectClipVertical(client, "Down", -1, "Teal",
		func() clipSelection { return sel },
		func(s clipSelection) { sel = s },
		func(int) {},
	)

	rec := findRec(received(), "select_clip_vertical")
	if rec == nil {
		t.Fatal("select_clip_vertical not sent")
	}
	if rec["current_track"].(float64) != 2 {
		t.Errorf("current_track = %v, want 2 (the prior selection's track)", rec["current_track"])
	}
	if rec["direction"].(float64) != -1 {
		t.Errorf("direction = %v, want -1", rec["direction"])
	}
}

func TestDispatchSelectClipVertical_Failure_LeavesStateUnchanged(t *testing.T) {
	sockPath, _, _, setVerticalResp, _ := startDirectionSelectSidecar(t, "edit")
	setVerticalResp(map[string]any{"ok": false, "err": "no_clip"})
	client := dialRecordingClient(t, sockPath)

	origSel := clipSelection{active: true, track: 1, start: 0, end: 10}
	sel := origSel
	track := 1
	got := dispatchSelectClipVertical(client, "Up", 1, "Teal",
		func() clipSelection { return sel },
		func(s clipSelection) { sel = s },
		func(tr int) { track = tr },
	)

	if got != "" {
		t.Errorf("LastAction = %q, want \"\" on failure", got)
	}
	if sel != origSel {
		t.Errorf("selection mutated on failure: got %+v, want unchanged %+v", sel, origSel)
	}
	if track != 1 {
		t.Errorf("activeTrack mutated on failure: got %d, want unchanged 1", track)
	}
}

// ---------------------------------------------------------------------------
// dispatchSelectClipHorizontal
// ---------------------------------------------------------------------------

func TestDispatchSelectClipHorizontal_NilClient_NoOp(t *testing.T) {
	var sel clipSelection
	got := dispatchSelectClipHorizontal(nil, "Left", -1, "Teal",
		func() clipSelection { return sel },
		func(s clipSelection) { sel = s },
		func(int) {},
	)
	if got != "" || sel.active {
		t.Errorf("nil-client call should be a no-op: LastAction=%q sel=%+v", got, sel)
	}
}

func TestDispatchSelectClipHorizontal_Success_SendsCurrentTrackAndStart(t *testing.T) {
	sockPath, received, _, _, setHorizontalResp := startDirectionSelectSidecar(t, "edit")
	setHorizontalResp(map[string]any{
		"ok": true, "track": 1.0, "unique_id": "uid-A0", "start": 40.0, "end": 200.0,
	})
	client := dialRecordingClient(t, sockPath)

	sel := clipSelection{active: true, track: 1, start: 200, end: 300}
	track := 1
	got := dispatchSelectClipHorizontal(client, "Left", -1, "Teal",
		func() clipSelection { return sel },
		func(s clipSelection) { sel = s },
		func(tr int) { track = tr },
	)

	if got != "Left → V1" {
		t.Errorf("LastAction = %q, want %q", got, "Left → V1")
	}
	if !sel.active || sel.start != 40 || sel.end != 200 {
		t.Errorf("selection = %+v, want active start=40 end=200", sel)
	}
	if track != 1 {
		t.Errorf("activeTrack = %d, want 1", track)
	}

	rec := findRec(received(), "select_clip_horizontal")
	if rec == nil {
		t.Fatal("select_clip_horizontal not sent")
	}
	if rec["current_track"].(float64) != 1 || rec["current_start"].(float64) != 200 {
		t.Errorf("current_track/current_start = %v/%v, want 1/200", rec["current_track"], rec["current_start"])
	}
	if rec["direction"].(float64) != -1 {
		t.Errorf("direction = %v, want -1", rec["direction"])
	}
}

func TestDispatchSelectClipHorizontal_NoSelectionYet_SendsZeroTrackAndStart(t *testing.T) {
	sockPath, received, _, _, setHorizontalResp := startDirectionSelectSidecar(t, "edit")
	setHorizontalResp(map[string]any{"ok": true, "track": 3.0, "start": 0.0, "end": 50.0})
	client := dialRecordingClient(t, sockPath)

	var sel clipSelection
	dispatchSelectClipHorizontal(client, "Right", 1, "Teal",
		func() clipSelection { return sel },
		func(s clipSelection) { sel = s },
		func(int) {},
	)

	rec := findRec(received(), "select_clip_horizontal")
	if rec == nil {
		t.Fatal("select_clip_horizontal not sent")
	}
	if rec["current_track"].(float64) != 0 || rec["current_start"].(float64) != 0 {
		t.Errorf("current_track/current_start = %v/%v, want 0/0 (bootstrap)", rec["current_track"], rec["current_start"])
	}
}

func TestDispatchSelectClipHorizontal_Failure_LeavesStateUnchanged(t *testing.T) {
	sockPath, _, _, _, setHorizontalResp := startDirectionSelectSidecar(t, "edit")
	setHorizontalResp(map[string]any{"ok": false, "err": "no_more_clips"})
	client := dialRecordingClient(t, sockPath)

	origSel := clipSelection{active: true, track: 1, start: 0, end: 10}
	sel := origSel
	got := dispatchSelectClipHorizontal(client, "Left", -1, "Teal",
		func() clipSelection { return sel },
		func(s clipSelection) { sel = s },
		func(int) {},
	)

	if got != "" {
		t.Errorf("LastAction = %q, want \"\" on failure", got)
	}
	if sel != origSel {
		t.Errorf("selection mutated on failure: got %+v, want unchanged %+v", sel, origSel)
	}
}

// ---------------------------------------------------------------------------
// dispatchSelectClipClear
// ---------------------------------------------------------------------------

func TestDispatchSelectClipClear_NilClient_StillClearsLocalState(t *testing.T) {
	track := 5
	sel := clipSelection{active: true, track: 5, start: 0, end: 100}
	got := dispatchSelectClipClear(nil, func(tr int) { track = tr }, func() { sel = clipSelection{} })

	if got != "C5 unselect" {
		t.Errorf("LastAction = %q, want %q", got, "C5 unselect")
	}
	if track != 0 || sel.active {
		t.Errorf("local state not cleared: track=%d sel=%+v", track, sel)
	}
}

func TestDispatchSelectClipClear_SendsClearHighlight(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	track := 2
	sel := clipSelection{active: true, track: 2, start: 0, end: 100}
	got := dispatchSelectClipClear(client, func(tr int) { track = tr }, func() { sel = clipSelection{} })

	if got != "C5 unselect" {
		t.Errorf("LastAction = %q, want %q", got, "C5 unselect")
	}
	if track != 0 || sel.active {
		t.Errorf("local state not cleared: track=%d sel=%+v", track, sel)
	}
	waitForRecs(t, received, 1)
	if op := received()[0]["op"]; op != "clear_highlight" {
		t.Errorf("op = %v, want clear_highlight", op)
	}
}

// ---------------------------------------------------------------------------
// Engine.getSelection/setSelection/clearSelection
// ---------------------------------------------------------------------------

func TestEngineSelection_GetSetClear(t *testing.T) {
	eng := newTestEngine(t)
	if got := eng.getSelection(); got.active {
		t.Errorf("initial selection = %+v, want inactive", got)
	}
	eng.setSelection(clipSelection{active: true, track: 2, start: 5, end: 15})
	if got := eng.getSelection(); !got.active || got.track != 2 || got.start != 5 || got.end != 15 {
		t.Errorf("selection after setSelection = %+v, want active track=2 start=5 end=15", got)
	}
	eng.clearSelection()
	if got := eng.getSelection(); got.active {
		t.Errorf("selection after clearSelection = %+v, want inactive", got)
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration
// ---------------------------------------------------------------------------

func directionButtonDeviceMap() string {
	return `{"knobs":{},"sliders":{},"buttons":{"76":"Up","77":"Down","78":"Left","79":"Right","53":"C5"}}`
}

func directionButtonBindings() string {
	return `{
		"col": {},
		"edit": {
			"Up": {"type": "selectClipVert"},
			"Down": {"type": "selectClipVert"},
			"Left": {"type": "selectClipHoriz"},
			"Right": {"type": "selectClipHoriz"},
			"C5": {"type": "selectClipClear"}
		}
	}`
}

func TestDrainLoop_Up_EditPage_RoutesToVerticalSelect(t *testing.T) {
	eng := newEngineWithConfig(t, directionButtonDeviceMap(), directionButtonBindings())
	sockPath, received, _, setVerticalResp, _ := startDirectionSelectSidecar(t, "edit")
	setVerticalResp(map[string]any{"ok": true, "track": 2.0, "start": 0.0, "end": 100.0})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 76, 100), 0) // Up, note-on

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 2 {
		t.Fatalf("activeTrack = %d, want 2", got)
	}
	if got := eng.getSelection(); !got.active || got.track != 2 {
		t.Fatalf("selection = %+v, want active track=2", got)
	}
	if findRec(received(), "select_clip_vertical") == nil {
		t.Fatalf("select_clip_vertical not sent: %v", received())
	}
}

func TestDrainLoop_ColorPage_LeftRight_StillDoNodeNav(t *testing.T) {
	// CRITICAL regression guard: off the Edit page, Left/Right must stay
	// byte-identical to the pre-existing hard-wired node prev/next nav --
	// never route to selection.
	eng := newEngineWithConfig(t, directionButtonDeviceMap(), directionButtonBindings())
	sockPath, received, _, _, _ := startDirectionSelectSidecar(t, "color")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 79, 100), 0) // Right, note-on

	waitForRecs(t, received, 1)
	recs := received()
	if findRec(recs, "select_clip_horizontal") != nil {
		t.Fatalf("select_clip_horizontal must NOT be sent on the Color page: %v", recs)
	}
	if rec := findRec(recs, "set_node"); rec == nil {
		t.Fatalf("set_node (node-nav) not sent on the Color page: %v", recs)
	} else if rec["node"].(float64) != 2 {
		t.Errorf("set_node node = %v, want 2 (Right advances from 1)", rec["node"])
	}
	if eng.getActiveTrack() != 0 {
		t.Errorf("activeTrack = %d, want 0 (Right on Color page must not select a clip)", eng.getActiveTrack())
	}
}

func TestDrainLoop_EditPage_LeftRight_RoutesToHorizontalSelect(t *testing.T) {
	eng := newEngineWithConfig(t, directionButtonDeviceMap(), directionButtonBindings())
	sockPath, received, _, _, setHorizontalResp := startDirectionSelectSidecar(t, "edit")
	setHorizontalResp(map[string]any{"ok": true, "track": 1.0, "start": 40.0, "end": 200.0})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 78, 100), 0) // Left, note-on

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 1 {
		t.Fatalf("activeTrack = %d, want 1", got)
	}
	recs := received()
	if findRec(recs, "select_clip_horizontal") == nil {
		t.Fatalf("select_clip_horizontal not sent: %v", recs)
	}
	if findRec(recs, "set_node") != nil {
		t.Fatalf("set_node (node-nav) must NOT be sent on the Edit page: %v", recs)
	}
}

func TestDrainLoop_Selection_SetsBumpPropertyTrackOverride(t *testing.T) {
	// CRITICAL: the "must not regress the now-working edit knobs" contract
	// -- once Up/Down/Left/Right selects a clip, a subsequent property
	// bump (e.g. Exposure knob) must carry "track": <selected track>, the
	// exact override mechanism D1's activeTrack always used.
	deviceMap := `{"knobs":{"33":"Exposure"},"sliders":{},"buttons":{"76":"Up"}}`
	bindings := `{
		"col": {},
		"edit": {
			"Up": {"type": "selectClipVert"},
			"Exposure": {"type": "property", "property": "Pan", "sens": 2.0}
		}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)
	sockPath, received, _, setVerticalResp, _ := startDirectionSelectSidecar(t, "edit")
	setVerticalResp(map[string]any{"ok": true, "track": 4.0, "start": 0.0, "end": 1000.0})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 76, 100), 0) // Up -> selects track 4

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 4 {
		t.Fatalf("activeTrack = %d, want 4 (test precondition)", got)
	}

	cb(midi.ControlChange(0, 33, 1), 0) // Exposure knob turn -> bump_property

	// Poll for "bump_property" specifically, rather than a raw rec count --
	// once activeTrack != 0, queryActiveClip's background Status.ActiveClip
	// display polling ALSO sends "clips_at_playhead" every tick, which
	// would otherwise satisfy a generic waitForRecs count before
	// bump_property itself has actually landed.
	deadline = time.Now().Add(2 * time.Second)
	var rec map[string]any
	for time.Now().Before(deadline) {
		if rec = findRec(received(), "bump_property"); rec != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rec == nil {
		t.Fatalf("bump_property not sent: %v", received())
	}
	if tr, ok := rec["track"]; !ok || tr.(float64) != 4 {
		t.Errorf("bump_property track = %v (present=%v), want 4", tr, ok)
	}
}

func TestDrainLoop_C5_ClearsSelectionAndSendsClearHighlight(t *testing.T) {
	eng := newEngineWithConfig(t, directionButtonDeviceMap(), directionButtonBindings())
	sockPath, received, _, setVerticalResp, _ := startDirectionSelectSidecar(t, "edit")
	setVerticalResp(map[string]any{"ok": true, "track": 2.0, "start": 0.0, "end": 100.0})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 76, 100), 0) // Up -> select

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.getActiveTrack() != 2 {
		t.Fatalf("activeTrack after Up = %d, want 2 (test precondition)", eng.getActiveTrack())
	}

	cb(midi.NoteOn(0, 53, 100), 0) // C5 -> unselect

	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 0 {
		t.Fatalf("activeTrack after C5 = %d, want 0", got)
	}
	if got := eng.getSelection(); got.active {
		t.Fatalf("selection after C5 = %+v, want inactive", got)
	}
	if findRec(received(), "clear_highlight") == nil {
		t.Fatalf("clear_highlight not sent: %v", received())
	}
}

func TestDrainLoop_AutoReset_PlayheadLeavesRange_ClearsSelection(t *testing.T) {
	eng := newEngineWithConfig(t, directionButtonDeviceMap(), directionButtonBindings())
	sockPath, received, setPlayheadFrame, setVerticalResp, _ := startDirectionSelectSidecar(t, "edit")
	setVerticalResp(map[string]any{"ok": true, "track": 2.0, "start": 100.0, "end": 200.0})
	setPlayheadFrame(150) // inside [100,200)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 76, 100), 0) // Up -> select [100,200)

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.getActiveTrack() != 2 {
		t.Fatalf("activeTrack after Up = %d, want 2 (test precondition)", eng.getActiveTrack())
	}

	// Playhead moves OUTSIDE [100,200) -- must clear.
	setPlayheadFrame(250)

	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 0 {
		t.Fatalf("activeTrack after playhead left range = %d, want 0 (auto-reset)", got)
	}
	if got := eng.getSelection(); got.active {
		t.Fatalf("selection after auto-reset = %+v, want inactive", got)
	}
	if n := countRecs(received(), "clear_highlight"); n < 1 {
		t.Errorf("clear_highlight sent %d times, want >=1 (auto-reset must restore color)", n)
	}
}

func TestDrainLoop_AutoReset_PlayheadWithinRange_DoesNotClear(t *testing.T) {
	eng := newEngineWithConfig(t, directionButtonDeviceMap(), directionButtonBindings())
	sockPath, _, setPlayheadFrame, setVerticalResp, _ := startDirectionSelectSidecar(t, "edit")
	setVerticalResp(map[string]any{"ok": true, "track": 2.0, "start": 100.0, "end": 200.0})
	setPlayheadFrame(150)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 76, 100), 0) // Up -> select [100,200)

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.getActiveTrack() != 2 {
		t.Fatalf("activeTrack after Up = %d, want 2 (test precondition)", eng.getActiveTrack())
	}

	// Playhead nudges WITHIN [100,200) -- must NOT clear, across many ticks.
	setPlayheadFrame(180)
	time.Sleep(400 * time.Millisecond)

	if got := eng.getActiveTrack(); got != 2 {
		t.Errorf("activeTrack after in-range playhead move = %d, want 2 (must NOT auto-reset within the selected clip)", got)
	}
}

func TestDrainLoop_AutoReset_EndIsExclusive(t *testing.T) {
	// [start, end) -- landing exactly ON end must clear (end is exclusive).
	eng := newEngineWithConfig(t, directionButtonDeviceMap(), directionButtonBindings())
	sockPath, _, setPlayheadFrame, setVerticalResp, _ := startDirectionSelectSidecar(t, "edit")
	setVerticalResp(map[string]any{"ok": true, "track": 2.0, "start": 100.0, "end": 200.0})
	setPlayheadFrame(150)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 76, 100), 0)

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.getActiveTrack() != 2 {
		t.Fatalf("activeTrack after Up = %d, want 2 (test precondition)", eng.getActiveTrack())
	}

	setPlayheadFrame(200) // == end, exclusive -> outside

	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 0 {
		t.Fatalf("activeTrack at playhead==end = %d, want 0 (end is exclusive)", got)
	}
}

// ---------------------------------------------------------------------------
// BUG 3 (2026-08-12 live report): auto-reset race. select_clip_horizontal
// moves the playhead via SetCurrentTimecode, but the drain loop's 250ms
// status tick can read the OLD playhead value before Resolve commits that
// write -- observed live 17ms after a Right press: the tick still reported
// the pre-move playhead, judged it "outside" the just-selected range, and
// cleared the selection that was never actually wrong. The fake sidecar
// here models this precisely: setPlayheadFrame is completely independent of
// setHorizontalResp, so a select "succeeding" never by itself updates what
// the next status tick reports -- exactly like the real race, where issuing
// SetCurrentTimecode and Resolve's timeline actually reflecting it are two
// separate moments.
// ---------------------------------------------------------------------------

// directionButtonBindingsWithGrace is directionButtonBindings() with an
// explicit _config.selectionGraceMs, so tests can use a short window and
// stay fast/deterministic instead of waiting out the real ~500ms default.
func directionButtonBindingsWithGrace(graceMs int) string {
	return fmt.Sprintf(`{
		"_config": {"selectionGraceMs": %d},
		"col": {},
		"edit": {
			"Up": {"type": "selectClipVert"},
			"Down": {"type": "selectClipVert"},
			"Left": {"type": "selectClipHoriz"},
			"Right": {"type": "selectClipHoriz"},
			"C5": {"type": "selectClipClear"}
		}
	}`, graceMs)
}

func TestDrainLoop_AutoReset_HorizontalSelect_SuppressesStalePlayheadUntilConfirmed(t *testing.T) {
	eng := newEngineWithConfig(t, directionButtonDeviceMap(), directionButtonBindingsWithGrace(2000))
	sockPath, _, setPlayheadFrame, _, setHorizontalResp := startDirectionSelectSidecar(t, "edit")
	// Playhead starts OUTSIDE the range the Right press is about to
	// select -- models the exact reported race: the very next status
	// tick's own read is stale relative to the SetCurrentTimecode
	// select_clip_horizontal is about to issue.
	setPlayheadFrame(843)
	setHorizontalResp(map[string]any{"ok": true, "track": 2.0, "start": 998.0, "end": 1021.0})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 79, 100), 0) // Right -> selects [998,1021)

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.getActiveTrack() != 2 {
		t.Fatalf("activeTrack after Right = %d, want 2 (test precondition)", eng.getActiveTrack())
	}

	// Several drain ticks (50ms each) pass with the STALE (outside-range)
	// playhead still being reported -- the selection must survive, not be
	// auto-reset away before Resolve's own write has even been observed.
	time.Sleep(200 * time.Millisecond)
	if got := eng.getActiveTrack(); got != 2 {
		t.Fatalf("activeTrack after stale-playhead ticks = %d, want 2 (must NOT auto-reset before arrival is confirmed)", got)
	}
	if got := eng.getSelection(); !got.active {
		t.Fatalf("selection after stale-playhead ticks = %+v, want still active", got)
	}
}

func TestDrainLoop_AutoReset_HorizontalSelect_ConfirmsThenClearsOnGenuineExit(t *testing.T) {
	eng := newEngineWithConfig(t, directionButtonDeviceMap(), directionButtonBindingsWithGrace(2000))
	sockPath, _, setPlayheadFrame, _, setHorizontalResp := startDirectionSelectSidecar(t, "edit")
	setPlayheadFrame(843) // stale, outside the about-to-be-selected range
	setHorizontalResp(map[string]any{"ok": true, "track": 2.0, "start": 998.0, "end": 1021.0})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 79, 100), 0) // Right -> selects [998,1021)

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.getActiveTrack() != 2 {
		t.Fatalf("activeTrack after Right = %d, want 2 (test precondition)", eng.getActiveTrack())
	}

	// Resolve "catches up": the playhead now reads inside [998,1021) --
	// this must be observed and CONFIRM arrival.
	setPlayheadFrame(1005)
	deadline = time.Now().Add(2 * time.Second)
	for {
		if sel := eng.getSelection(); sel.confirmed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("selection never confirmed after playhead entered range")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 2 {
		t.Fatalf("activeTrack after confirmation = %d, want still 2", got)
	}

	// NOW leave the range for real -- must clear immediately, same as the
	// pre-existing (non-race) auto-reset behaviour once a selection has
	// been confirmed.
	setPlayheadFrame(1200)
	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 0 {
		t.Fatalf("activeTrack after genuine exit post-confirmation = %d, want 0", got)
	}
	if got := eng.getSelection(); got.active {
		t.Fatalf("selection after genuine exit post-confirmation = %+v, want inactive", got)
	}
}

func TestDrainLoop_AutoReset_HorizontalSelect_GraceWindowElapses_ClearsEvenWithoutConfirmation(t *testing.T) {
	// Small grace window: if the playhead NEVER reports inside the
	// selected range (e.g. SetCurrentTimecode genuinely failed, or an
	// extreme stall), the selection must still eventually clear --
	// suppression is a bounded grace, not a permanent hold.
	eng := newEngineWithConfig(t, directionButtonDeviceMap(), directionButtonBindingsWithGrace(80))
	sockPath, _, setPlayheadFrame, _, setHorizontalResp := startDirectionSelectSidecar(t, "edit")
	setPlayheadFrame(843) // stays outside [998,1021) for the whole test
	setHorizontalResp(map[string]any{"ok": true, "track": 2.0, "start": 998.0, "end": 1021.0})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 79, 100), 0) // Right -> selects [998,1021)

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.getActiveTrack() != 2 {
		t.Fatalf("activeTrack after Right = %d, want 2 (test precondition)", eng.getActiveTrack())
	}

	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 0 {
		t.Fatalf("activeTrack after grace window elapsed = %d, want 0 (must fall back to clearing rather than suppress forever)", got)
	}
}
