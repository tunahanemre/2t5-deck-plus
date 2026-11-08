package engine

// Regression coverage for the 2026-08-12 dispatch-generalization pass:
// dispatchNoteOn's IsCDL()/IsProperty() cases and IsResetSelected()'s
// resetParam/resetProperty sub-branches now fire their client.Send call(s)
// in their own goroutine, matching the established IsQuickExport()/
// IsColorByRenderState() pattern (see dispatchQuickExport's doc comment
// and TestDispatchNoteOn_QuickExport_RunsAsync_DoesNotBlock) -- so
// dispatchNoteOn itself must return WITHOUT waiting for the sidecar round
// trip. Unlike QuickExport (which blocks on a Go callback, easy to gate
// with a channel), these paths block on a REAL client.Send round trip, so
// this file spins up a small fake unix-socket sidecar that blocks mid-
// response until the test releases it, and connects a genuine *ipc.Client
// to it -- proving the asynchrony end to end through the real IPC path,
// not just past a mocked seam.
//
// These are the paths flagged "still fully synchronous" by the 2026-08-12
// C1/Space root-cause session's audit and judged SAFE to generalize this
// session specifically because none of them write any local Engine state
// another control's dispatch depends on, and all of them reset to a FIXED
// value (a param/property default) -- two resets racing out of order
// converge on the identical final state regardless of send order. This is
// what distinguishes them from dispatchAddMarker/dispatchMarkerAtPlayhead/
// dispatchSetMarkerColor, which were deliberately left synchronous (see
// their doc comments) because their local marker-mode state (activeMarkerFrame)
// gates THREE other direct-intercept controls' synchronous dispatch
// decisions, and their remote actions (create/delete/recolor) are NOT
// order-independent the way a reset-to-default is.

import (
	"encoding/json"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/ipc"
	"2t5-deck-plus/internal/mode"
	"2t5-deck-plus/internal/target"
)

// startBlockingSidecar spins up a fake unix-socket sidecar that, for every
// received frame, increments count() immediately (so a test can observe
// "the send was issued") and THEN blocks until `release` is closed before
// replying {"ok":true} -- unlike a fixed time.Sleep delay, this gives the
// test exact control over when each round trip completes, and (since
// closing a channel unblocks every current AND future receive on it) a
// single release() lets a multi-send loop's later sends complete
// immediately too, without needing a fresh channel per iteration.
func startBlockingSidecar(t *testing.T, release <-chan struct{}) (sockPath string, count func() int32) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-block-*.sock")
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

	var n int32
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
					atomic.AddInt32(&n, 1)
					<-release
					reply, _ := json.Marshal(map[string]any{"ok": true})
					if err := ipc.WriteFrame(c, reply); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return sockPath, func() int32 { return atomic.LoadInt32(&n) }
}

// mustDialBlocking connects a real *ipc.Client to a fresh startBlockingSidecar,
// registering cleanup.
func mustDialBlocking(t *testing.T, release <-chan struct{}) (*ipc.Client, func() int32) {
	t.Helper()
	sockPath, count := startBlockingSidecar(t, release)
	client, err := ipc.Dial(sockPath)
	if err != nil {
		t.Fatalf("ipc.Dial: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client, count
}

// assertReturnsPromptly calls fn in its own goroutine and fails the test if
// it does not return within 1 second -- the shared shape behind every test
// in this file, mirroring TestDispatchNoteOn_QuickExport_RunsAsync_DoesNotBlock.
func assertReturnsPromptly(t *testing.T, name string, fn func() string) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- fn() }()
	select {
	case got := <-done:
		return got
	case <-time.After(1 * time.Second):
		t.Fatalf("%s blocked on the sidecar round trip -- must dispatch on its own goroutine, never the caller's", name)
		return ""
	}
}

func TestDispatchNoteOn_CDLReset_RunsAsync_DoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	client, count := mustDialBlocking(t, release)

	b := config.Binding{Type: "cdl", Param: "offset", Ch: "master"}
	got := assertReturnsPromptly(t, "IsCDL() reset_param", func() string {
		return dispatchNoteOn("Col", b, mode.New(), nil, client, 1, 0,
			func() string { return "color" }, nil, nil, nil, nil, nil, nil)
	})
	if got != "Col reset" {
		t.Errorf("LastAction = %q, want %q", got, "Col reset")
	}

	close(release) // let the blocked send complete
	deadline := time.Now().Add(1 * time.Second)
	for count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if count() != 1 {
		t.Fatalf("reset_param was never actually sent (count=%d) -- async must not mean dropped", count())
	}
}

func TestDispatchNoteOn_PropertyReset_RunsAsync_DoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	client, count := mustDialBlocking(t, release)

	b := config.Binding{Type: "property", Property: "CropTop"}
	got := assertReturnsPromptly(t, "IsProperty() reset_property", func() string {
		return dispatchNoteOn("Temperature", b, mode.New(), nil, client, 1, 0,
			func() string { return "edit" }, nil, nil, nil, nil, nil, nil)
	})
	if got == "" {
		t.Error("LastAction empty, want a non-empty property-reset echo")
	}

	close(release)
	deadline := time.Now().Add(1 * time.Second)
	for count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if count() != 1 {
		t.Fatalf("reset_property was never actually sent (count=%d)", count())
	}
}

func TestDispatchNoteOn_ResetSelected_ResetParam_RunsAsync_DoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	client, count := mustDialBlocking(t, release)

	cfg := loadConfig(t, `{
	  "_config": {"resetTargets": {"lift": {"type":"resetParam","param":"offset","ch":"master"}}},
	  "col": {"Col": {"type":"resetSelected"}}
	}`)
	aw := target.New() // defaults to "lift", matching the resetTargets entry above

	b := config.Binding{Type: "resetSelected"}
	got := assertReturnsPromptly(t, "IsResetSelected() resetParam", func() string {
		return dispatchNoteOn("Col", b, mode.New(), nil, client, 1, 0,
			func() string { return "color" }, aw, cfg, nil, nil, nil, nil)
	})
	if got == "" {
		t.Error("LastAction empty, want a non-empty Reset echo")
	}

	close(release)
	deadline := time.Now().Add(1 * time.Second)
	for count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if count() != 1 {
		t.Fatalf("reset_param was never actually sent (count=%d)", count())
	}
}

// TestDispatchNoteOn_ResetSelected_ResetProperty_RunsAsync_DoesNotBlock is
// the direct regression test for the audit's HEAVIEST flagged path: a
// "gang" resetProperty target chains MULTIPLE sequential client.Send
// calls (one per Properties entry) for one press. Uses 3 properties so a
// pre-fix (synchronous loop) implementation would need all 3 round trips
// to complete before dispatchNoteOn returns -- with the blocking sidecar
// gating every reply on the same unreleased channel, that would hang this
// test past its 1-second bound.
func TestDispatchNoteOn_ResetSelected_ResetProperty_RunsAsync_DoesNotBlock(t *testing.T) {
	release := make(chan struct{})
	client, count := mustDialBlocking(t, release)

	cfg := loadConfig(t, `{
	  "_config": {"resetTargets": {"crop": {"type":"resetProperty","properties":["CropLeft","CropTop","CropRight"]}}},
	  "col": {"Col": {"type":"resetSelected"}}
	}`)
	aw := target.New()
	aw.Set("crop")

	b := config.Binding{Type: "resetSelected"}
	got := assertReturnsPromptly(t, "IsResetSelected() resetProperty (3-property gang)", func() string {
		return dispatchNoteOn("Col", b, mode.New(), nil, client, 1, 0,
			func() string { return "edit" }, aw, cfg, nil, nil, nil, nil)
	})
	if got == "" {
		t.Error("LastAction empty, want a non-empty Reset echo")
	}

	close(release)
	deadline := time.Now().Add(1 * time.Second)
	for count() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := count(); n != 3 {
		t.Fatalf("reset_property sent %d times, want 3 (one per Properties entry -- the whole gang must still land, just off the MIDI-callback goroutine)", n)
	}
}
