package engine

// Tests for the HUD v2 Edit-page target-clip readout (2026-07-08):
//   - queryStatus() includes "track" in its "status" request exactly like
//     bump_property/reset_property's own "activeTrack != 0" convention, so
//     the sidecar's edit_clip_info() resolves the SAME target the knobs
//     affect.
//   - Status.EditClipName/HaveEditClip/EditClipPan/EditClipTilt/
//     EditClipZoom get populated from the sidecar's "edit_clip" response.
//   - A status reply with no "edit_clip" (every pre-existing fake-sidecar
//     fixture in this package) leaves HaveEditClip false -- never a
//     zero-value stale readout.
//
// All hermetic: real *Engine via newTestEngine, a fake unix-socket sidecar
// (reusing lastaction_test.go's newUnixSocketListener/serveFakeSidecar
// helpers), no live Resolve, DECKPLUS_ARMED never set.

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// startEditClipStatusSidecar replies to "status" ops with `page` + the
// given `editClip` payload (nil serializes to JSON null, matching "no
// target clip"), and records the "track" field of the LAST "status"
// request received (present/value) so tests can assert queryStatus()
// threads Engine.activeTrack through correctly.
func startEditClipStatusSidecar(t *testing.T, page string, editClip map[string]any) (sockPath string, lastTrack func() (float64, bool)) {
	t.Helper()
	sockPath, ln := newUnixSocketListener(t)

	type trackSeen struct {
		val float64
		ok  bool
	}
	seenCh := make(chan trackSeen, 1)
	last := trackSeen{}

	serveFakeSidecar(ln, func(req map[string]any) []byte {
		op, _ := req["op"].(string)
		if op == "status" {
			v, ok := req["track"].(float64)
			select {
			case seenCh <- trackSeen{val: v, ok: ok}:
			default:
				// Drain and replace so lastTrack() below always reflects
				// the MOST RECENT status request, not the first.
				select {
				case <-seenCh:
				default:
				}
				seenCh <- trackSeen{val: v, ok: ok}
			}
			reply := map[string]any{"ok": true, "page": page}
			if editClip != nil {
				reply["edit_clip"] = editClip
			} else {
				reply["edit_clip"] = nil
			}
			b, _ := json.Marshal(reply)
			return b
		}
		return []byte(`{"ok":true}`)
	})

	lastTrack = func() (float64, bool) {
		select {
		case last = <-seenCh:
		default:
		}
		return last.val, last.ok
	}
	return sockPath, lastTrack
}

func TestStatus_EditClip_PopulatesFromSidecar(t *testing.T) {
	eng := newTestEngine(t)
	editClip := map[string]any{"name": "DSCF9804.MOV", "pan": 12.5, "tilt": -3.25, "zoom": 1.4}
	sockPath, _ := startEditClipStatusSidecar(t, "edit", editClip)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	deadline := time.Now().Add(2 * time.Second)
	var s Status
	for time.Now().Before(deadline) {
		s = eng.Status()
		if s.HaveEditClip {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !s.HaveEditClip {
		t.Fatal("Status().HaveEditClip never became true")
	}
	if s.EditClipName != "DSCF9804.MOV" {
		t.Errorf("EditClipName = %q, want %q", s.EditClipName, "DSCF9804.MOV")
	}
	if s.EditClipPan != 12.5 {
		t.Errorf("EditClipPan = %v, want 12.5", s.EditClipPan)
	}
	if s.EditClipTilt != -3.25 {
		t.Errorf("EditClipTilt = %v, want -3.25", s.EditClipTilt)
	}
	if s.EditClipZoom != 1.4 {
		t.Errorf("EditClipZoom = %v, want 1.4", s.EditClipZoom)
	}
}

func TestStatus_EditClip_AbsentWhenNoTargetClip(t *testing.T) {
	eng := newTestEngine(t)
	sockPath, _ := startEditClipStatusSidecar(t, "edit", nil)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	// Give a few ticks time to land, then assert the field stayed at its
	// zero value the whole time -- a null "edit_clip" must never produce
	// a fake "populated" reading.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s := eng.Status(); s.HaveEditClip {
			t.Fatalf("HaveEditClip became true from a null edit_clip reply: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestQueryStatus_OmitsTrack_WhenNoActiveSelection(t *testing.T) {
	eng := newTestEngine(t)
	sockPath, lastTrack := startEditClipStatusSidecar(t, "edit", nil)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	// Let at least one status tick land.
	time.Sleep(100 * time.Millisecond)

	if _, ok := lastTrack(); ok {
		t.Error(`"status" request included "track" even though no D1/direction-button selection is active -- want it omitted, matching bump_property/reset_property's "activeTrack != 0" convention`)
	}
}

func TestQueryStatus_SendsActiveTrack_WhenSelectionActive(t *testing.T) {
	eng := newTestEngine(t)
	sockPath, lastTrack := startEditClipStatusSidecar(t, "edit", nil)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	eng.setActiveTrack(3)

	deadline := time.Now().Add(2 * time.Second)
	var got float64
	var ok bool
	for time.Now().Before(deadline) {
		got, ok = lastTrack()
		if ok && got == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !ok || got != 3 {
		t.Errorf(`"status" request "track" = (%v, present=%v), want (3, true) -- queryStatus() must thread the active D1/direction-button track override through to edit_clip_info() exactly like bump_property/reset_property already do`, got, ok)
	}
}
