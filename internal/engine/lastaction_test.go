package engine

// Tests for HUD v2's data plumbing: the humanWheelName/propertyLabel pure
// helpers, a representative sample of dispatch* functions' new LastAction
// return values, and Status carrying Node/Bank/FineMode/Timecode/
// TimelineName from a real Start()'d engine. All hermetic (no CGO, no
// window, DECKPLUS_ARMED never set) -- mirrors the existing
// togglehud_test.go/selectclip_test.go/marker_test.go direct-call style.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/ipc"
	"2t5-deck-plus/internal/mode"
)

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestHumanWheelName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"lift", "Lift"},
		{"gamma_master", "Gamma"},
		{"gain_master", "Gain"},
		{"offset_master", "Offset"},
	}
	for _, tc := range cases {
		if got := humanWheelName(tc.in); got != tc.want {
			t.Errorf("humanWheelName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPropertyLabel(t *testing.T) {
	cases := []struct {
		name string
		b    config.Binding
		want string
	}{
		{"single property", config.Binding{Property: "Pan"}, "Pan"},
		{"gang takes precedence", config.Binding{Property: "Pan", Gang: []string{"ZoomX", "ZoomY"}}, "ZoomX/ZoomY"},
		{"empty", config.Binding{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := propertyLabel(tc.b); got != tc.want {
				t.Errorf("propertyLabel(%+v) = %q, want %q", tc.b, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Fake-sidecar helpers (local to this file; startFakeSidecar in
// engine_test.go always replies {"page":"color"} regardless of op, which is
// too generic for the clips_at_playhead / status-field tests below).
// ---------------------------------------------------------------------------

// newUnixSocketListener creates a unix-domain-socket listener at a short
// path directly under os.TempDir() (NOT t.TempDir() -- macOS's sun_path
// ~104-byte limit rules that out, same rationale as engine_test.go's
// startFakeSidecar) and returns the path plus the listener for the caller
// to Accept() on.
func newUnixSocketListener(t *testing.T) (string, net.Listener) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-sidecar-*.sock")
	if err != nil {
		t.Fatalf("fake sidecar tempname: %v", err)
	}
	sockPath := f.Name()
	f.Close()
	os.Remove(sockPath)
	t.Cleanup(func() { os.Remove(sockPath) })

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("fake sidecar listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return sockPath, ln
}

// serveFakeSidecar accepts connections on ln and answers every request with
// reply(req) -- req is the decoded JSON request object.
func serveFakeSidecar(ln net.Listener, reply func(req map[string]any) []byte) {
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
					var req map[string]any
					_ = json.Unmarshal(body, &req)
					if err := ipc.WriteFrame(c, reply(req)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
}

// startFakeOKSidecar replies {"ok":true} to every request -- enough for any
// dispatch path that only checks Send's error, not the reply content.
func startFakeOKSidecar(t *testing.T) string {
	t.Helper()
	sockPath, ln := newUnixSocketListener(t)
	serveFakeSidecar(ln, func(req map[string]any) []byte { return []byte(`{"ok":true}`) })
	return sockPath
}

// startFakeClipsSidecar replies to "clips_at_playhead" with the given
// clips (ok=true) and {"ok":true} to everything else.
func startFakeClipsSidecar(t *testing.T, clips []clipEntry) string {
	t.Helper()
	sockPath, ln := newUnixSocketListener(t)
	serveFakeSidecar(ln, func(req map[string]any) []byte {
		if op, _ := req["op"].(string); op == "clips_at_playhead" {
			type wireClip struct {
				Track int    `json:"track"`
				Name  string `json:"name"`
			}
			wire := make([]wireClip, len(clips))
			for i, c := range clips {
				wire[i] = wireClip{Track: c.Track, Name: c.Name}
			}
			b, _ := json.Marshal(map[string]any{"ok": true, "clips": wire})
			return b
		}
		return []byte(`{"ok":true}`)
	})
	return sockPath
}

// startFakeStatusSidecar replies to "status" with a fixed page/timecode/
// timeline_name payload and {"ok":true} to everything else -- enough for
// processNonBalanceDrain/dispatchNoteOn's CDL and nav paths to see a
// successful send while the ticker's own "status" polls get real
// Timecode/TimelineName values to thread into Status.
func startFakeStatusSidecar(t *testing.T) string {
	t.Helper()
	sockPath, ln := newUnixSocketListener(t)
	serveFakeSidecar(ln, func(req map[string]any) []byte {
		if op, _ := req["op"].(string); op == "status" {
			return []byte(`{"ok":true,"page":"color","current_node":null,"timecode":"01:02:03:04","timeline_name":"EP10-v0"}`)
		}
		return []byte(`{"ok":true}`)
	})
	return sockPath
}

// ---------------------------------------------------------------------------
// Representative dispatch* LastAction return values (direct calls, same
// style as selectclip_test.go/marker_test.go/togglehud_test.go)
// ---------------------------------------------------------------------------

func TestDispatchNoteOn_ToggleHud_ReturnsHudShownOrHidden(t *testing.T) {
	b := config.Binding{Type: "toggleHud"}

	got := dispatchNoteOn("C6", b, mode.New(), nil, nil, 1, 0, func() string { return "color" }, nil, nil,
		nil, nil, nil, func() bool { return true })
	if got != "HUD shown" {
		t.Errorf("visible=true: dispatchNoteOn = %q, want %q", got, "HUD shown")
	}

	got = dispatchNoteOn("C6", b, mode.New(), nil, nil, 1, 0, func() string { return "color" }, nil, nil,
		nil, nil, nil, func() bool { return false })
	if got != "HUD hidden" {
		t.Errorf("visible=false: dispatchNoteOn = %q, want %q", got, "HUD hidden")
	}

	got = dispatchNoteOn("C6", b, mode.New(), nil, nil, 1, 0, func() string { return "color" }, nil, nil,
		nil, nil, nil, nil)
	if got != "" {
		t.Errorf("nil callback: dispatchNoteOn = %q, want \"\"", got)
	}
}

func TestDispatchNoteOn_QuickExport_ReturnsQuickExportImmediately(t *testing.T) {
	// dispatchQuickExport itself launches a goroutine; a nil client makes
	// it exit immediately (client==nil SKIP, no folder-picker popup, same
	// hermeticity posture as editpage_test.go's quickexport tests) so this
	// only asserts dispatchNoteOn's own SYNCHRONOUS return value.
	b := config.Binding{Type: "quickexport"}
	got := dispatchNoteOn("C5", b, mode.New(), nil, nil, 1, 0, func() string { return "color" }, nil, nil,
		func() (string, error) { return "", nil }, func() string { return "/tmp" }, func(string) {}, nil)
	if got != "Quick Export" {
		t.Errorf("dispatchNoteOn(quickexport) = %q, want %q", got, "Quick Export")
	}
}

func TestDispatchSelectClip_ReturnsD1ArrowTrack(t *testing.T) {
	sockPath := startFakeClipsSidecar(t, []clipEntry{{Track: 2, Name: "a.mov"}, {Track: 4, Name: "b.mov"}})
	client, err := ipc.Dial(sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	var active int
	got := dispatchSelectClip(1, client, func() int { return active }, func(v int) { active = v })
	if got != "D1 → V2" {
		t.Errorf("dispatchSelectClip = %q, want %q", got, "D1 → V2")
	}

	// step==0 and nil-client are both documented no-ops.
	if got := dispatchSelectClip(0, client, func() int { return 0 }, func(int) {}); got != "" {
		t.Errorf("step=0: dispatchSelectClip = %q, want \"\"", got)
	}
	if got := dispatchSelectClip(1, nil, func() int { return 0 }, func(int) {}); got != "" {
		t.Errorf("nil client: dispatchSelectClip = %q, want \"\"", got)
	}
}

func TestDispatchSelectClipRelease_AlwaysReturnsD1Release(t *testing.T) {
	if got := dispatchSelectClipRelease(nil, func(int) {}, "Blue"); got != "D1 release" {
		t.Errorf("nil client: dispatchSelectClipRelease = %q, want %q", got, "D1 release")
	}
}

func TestProcessNonBalanceDrain_Jog_ReturnsJogSummary(t *testing.T) {
	sockPath := startFakeOKSidecar(t)
	client, err := ipc.Dial(sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	b := config.Binding{Type: "jog", Base: 1.0, Gamma: 1.0}

	// Fine tier: small step, no jog tiers configured -> snapSeconds==0.
	got := processNonBalanceDrain("ControlDial", 2, b, mode.New(), nil, client, nil, "edit", nil,
		func() string { return "edit" }, 0, 0)
	if got == "" {
		t.Error("fine-tier jog: LastAction is empty, want a Jog summary")
	}

	// Sustained-snap override forces a snap-tier summary regardless of step.
	got = processNonBalanceDrain("ControlDial", 1, b, mode.New(), nil, client, nil, "edit", nil,
		func() string { return "edit" }, 0, 30)
	if got != "Jog +30s" {
		t.Errorf("sustain-snap jog: LastAction = %q, want %q", got, "Jog +30s")
	}
}

// ---------------------------------------------------------------------------
// Status.Node/Bank/FineMode/Timecode/TimelineName + a representative
// LastAction, exercised through a real Start()'d engine (matches the shape
// of TestConcurrentEvents_StatusPolling_Race in engine_test.go).
// ---------------------------------------------------------------------------

func TestStatus_HUDv2Fields_SetByRealEngine(t *testing.T) {
	eng := newTestEngine(t)
	eng.sockPath = startFakeStatusSidecar(t)
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

	// "Right" (note 79) bumps node via the atomic-promoted nodeAtomic.
	cb(midi.NoteOn(0, 79, 100), 0)
	// "Col" (note 65) sets bank explicitly to "col" (already the default,
	// but exercises the read path identically to a real press).
	cb(midi.NoteOn(0, 65, 100), 0)

	// Give the 50ms ticker time to publish the per-tick Status snapshot
	// (Timecode/TimelineName/Bank/FineMode/Node).
	deadline := time.Now().Add(2 * time.Second)
	var s Status
	for time.Now().Before(deadline) {
		s = eng.Status()
		if s.Node == 2 && s.Timecode != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if s.Node != 2 {
		t.Errorf("Status().Node = %d, want 2 (one Right press from the node=1 default)", s.Node)
	}
	if s.Bank != "col" {
		t.Errorf("Status().Bank = %q, want %q", s.Bank, "col")
	}
	if s.FineMode {
		t.Error("Status().FineMode = true, want false (C3 never pressed)")
	}
	if s.Timecode != "01:02:03:04" {
		t.Errorf("Status().Timecode = %q, want %q", s.Timecode, "01:02:03:04")
	}
	if s.TimelineName != "EP10-v0" {
		t.Errorf("Status().TimelineName = %q, want %q", s.TimelineName, "EP10-v0")
	}

	// Representative LastAction dispatch: a Saturation (CDL) knob turn,
	// drained on the next tick via processNonBalanceDrain's IsCDL() branch.
	cb(midi.ControlChange(0, 36, 1), 0)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eng.Status().LastAction != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := eng.Status().LastAction; got == "" {
		t.Error("Status().LastAction is empty after a Saturation knob turn, want a non-empty summary")
	}
}
