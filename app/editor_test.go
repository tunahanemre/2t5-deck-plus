package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"2t5-deck-plus/internal/engine"
	"2t5-deck-plus/internal/preset"
)

// toAPIEvent is pure; this test never starts an engine or touches MIDI.
func TestToAPIEventCopiesEveryField(t *testing.T) {
	in := engine.InputEvent{T: 5, Control: "D1", Kind: "turn", RawTicks: 3, TicksPerSec: 12, Accel: 2,
		Out: engine.InputOut{Text: "scroll +9", Value: 9, Unit: "lines"}, Fine: true,
		PresetID: "com.google.Chrome", Source: "app", Bank: "com.google.Chrome", Action: "browser.scroll"}
	got := toAPIEvent(in)
	if got.T != 5 || got.Control != "D1" || got.Kind != "turn" || got.RawTicks != 3 || got.TicksPerSec != 12 || got.Accel != 2 ||
		got.Out.Text != "scroll +9" || got.Out.Value != 9 || got.Out.Unit != "lines" || !got.Fine ||
		got.PresetID != "com.google.Chrome" || got.Source != "app" || got.Bank != "com.google.Chrome" || got.Action != "browser.scroll" {
		t.Fatalf("toAPIEvent = %+v", got)
	}
}

// initEditor wiring against a temp copy of the repo config. The engine is only
// constructed (engine.New loads config), never started: no MIDI, no sidecar.
func TestInitEditor_SeedsStoreServesAPIAndRecomposes(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"device-map.json", "bindings.json"} {
		b, err := os.ReadFile(filepath.Join("..", "config", f))
		if err != nil {
			t.Skipf("repo config not found: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e, err := engine.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(stateDirEnv, t.TempDir()) // never the real internal state folder
	w := initEditor(e, dir)
	if w == nil {
		t.Fatal("initEditor returned nil")
	}
	defer w.Close()

	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, loopbackReq("GET", "/api/apps"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "com.blackmagic-design.DaVinciResolve") {
		t.Fatalf("/api/apps = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	w.handler.ServeHTTP(rec, loopbackReq("GET", "/api/live"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"sidecar":"waiting"`) {
		t.Fatalf("/api/live = %d %s", rec.Code, rec.Body.String())
	}
	// wave D: engine block, armed switch and permissions are wired
	for _, want := range []string{`"engine":{`, `"appliedSeq":`, `"armed":true`, `"permissions":{`, `"readonly":false`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("/api/live lacks %s: %s", want, rec.Body.String())
		}
	}

	// An edit reaches the appdeck snapshot immediately.
	if _, err := w.store.AddApp("com.google.Chrome", "Google Chrome", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.store.Assign("com.google.Chrome", "base", "D1", map[string]any{"type": "scrollDelta", "linesPerDetent": 3}); err != nil {
		t.Fatal(err)
	}
	if !w.ad.Assigns("D1") {
		t.Fatal("appdeck snapshot not swapped after an edit")
	}
	// publish -> bus
	w.publish(engine.InputEvent{Control: "D1", Kind: "turn", T: 1})
	if _, evs := w.bus.Since(0); len(evs) != 1 || evs[0].Control != "D1" {
		t.Fatalf("bus = %+v", evs)
	}
}

// loopbackReq is a request with the Host the editor page sends (the API
// refuses any other Host).
func loopbackReq(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:2541"
	return r
}

// The HUD toggle flips the PERSISTED hudEnabled setting, and the Global preset
// gets Fn + C6 = Toggle HUD once (never again after the marker). No engine start, no MIDI.
func TestToggleHUDPersistsAndGlobalFnC6Seeded(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"device-map.json", "bindings.json"} {
		b, err := os.ReadFile(filepath.Join("..", "config", f))
		if err != nil {
			t.Skipf("repo config not found: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e, err := engine.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	t.Setenv(stateDirEnv, state)
	w := initEditor(e, dir)
	if w == nil {
		t.Fatal("initEditor returned nil")
	}
	defer w.Close()

	g, _ := w.store.Snapshot().Preset(preset.GlobalID)
	if a, ok := g.Banks["fn"]["C6"]; !ok || a["type"] != "toggleHud" {
		t.Fatalf("Global fn.C6 = %+v (present=%v), want toggleHud", a, ok)
	}
	if a := g.Banks["base"]["Fn"]; a["type"] != "modifierHold" || a["modifier"] != "fn" {
		t.Fatalf("Global base.Fn = %+v, want a modifierHold of fn (outside Resolve nothing else raises the fn bank)", a)
	}
	for i, c := range []string{"L1", "L2", "L3"} {
		if a := g.Banks["base"][c]; a["type"] != "layer" || a["layer"] != float64(i+1) && a["layer"] != i+1 {
			t.Fatalf("Global base.%s = %+v, want layer %d", c, a, i+1)
		}
	}
	read := func() any {
		r, _ := w.store.Snapshot().Preset(preset.ResolveID)
		return r.Settings["hudEnabled"]
	}
	if next, err := w.toggleHUDEnabled(); err != nil || next != false || read() != false {
		t.Fatalf("first toggle: next=%v err=%v saved=%v (want off, saved false)", next, err, read())
	}
	if next, err := w.toggleHUDEnabled(); err != nil || next != true || read() != true {
		t.Fatalf("second toggle: next=%v err=%v saved=%v (want on)", next, err, read())
	}
	// one-time: removing the assignment sticks across ensureGlobalHUDToggle
	if err := w.store.Unassign(preset.GlobalID, "fn", "C6"); err != nil {
		t.Fatal(err)
	}
	ensureGlobalHUDToggle(w.store, state)
	if g, _ := w.store.Snapshot().Preset(preset.GlobalID); g.Banks["fn"]["C6"]["type"] == "toggleHud" {
		t.Fatal("the Global Fn+C6 assignment was put back after he removed it")
	}
}
