package appdeck

import (
	"strings"
	"testing"
)

func TestDisarmedReportsButPostsNothing(t *testing.T) {
	x, r, s := setup(t)
	assign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	assign(t, s, chrome, "base", "C2", map[string]any{"type": "shell", "cmd": "/usr/bin/true"})
	assign(t, s, chrome, "base", "C3", map[string]any{"type": "layer", "layer": 2})
	assign(t, s, chrome, "base", "D1", map[string]any{"type": "scrollDelta", "linesPerDetent": 3, "detentThreshold": 1})
	assign(t, s, chrome, "base", "D2", map[string]any{"type": "keyDelta", "keyCW": "Cmd+]", "keyCCW": "Cmd+[", "detentThreshold": 1})
	x.swap(s)
	if !x.Armed() {
		t.Fatal("must start armed")
	}
	x.SetArmed(false)
	restoreArmed(t, x)
	if x.Armed() {
		t.Fatal("still armed")
	}

	press := func(c string) Result {
		return x.Handle(Input{Bundle: chrome, Layer: 1, Control: c, Kind: KindPress})
	}
	res := press("C1")
	if !res.Handled || !res.Fired || res.Text != "disarmed: Cmd+T" {
		t.Errorf("key: %+v", res)
	}
	res = press("C2")
	if !res.Fired || !strings.HasPrefix(res.Text, "disarmed: run ") {
		t.Errorf("shell: %+v", res)
	}
	res = x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D1", Kind: KindTurn, Step: 2})
	if !res.Fired || res.Text != "disarmed: scroll +6" || res.Value != 6 {
		t.Errorf("scroll: %+v", res)
	}
	res = x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D2", Kind: KindTurn, Step: -2})
	if !res.Fired || res.Text != "disarmed: Cmd+[ x2" {
		t.Errorf("keyDelta: %+v", res)
	}
	// internal effects (layer) are still reported and applied by the engine
	res = press("C3")
	if !res.Fired || res.Effect == nil || res.Effect.Kind != "layer" || res.Effect.Layer != 2 {
		t.Errorf("layer effect lost while disarmed: %+v", res)
	}
	if len(r.keys)+len(r.scrolls)+len(r.shells) != 0 {
		t.Fatalf("disarmed executor posted input: keys=%v scrolls=%v shells=%v", r.keys, r.scrolls, r.shells)
	}

	// re-arming restores normal behaviour, text without the prefix
	x.SetArmed(true)
	res = press("C1")
	if res.Text != "Cmd+T" || len(r.keys) != 1 || r.keys[0] != "Cmd+T" {
		t.Errorf("after re-arm: %+v keys=%v", res, r.keys)
	}
	res = x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D1", Kind: KindTurn, Step: 1})
	if res.Text != "scroll +3" || len(r.scrolls) != 1 {
		t.Errorf("scroll after re-arm: %+v %v", res, r.scrolls)
	}
}

func TestDisarmedHoldsForMomentumTails(t *testing.T) {
	// the tail path ends in fire(): the same guard covers it
	x, r, s := setup(t)
	assign(t, s, chrome, "base", "D1", map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 1, "momentumMs": 300})
	x.swap(s)
	x.SetArmed(false)
	restoreArmed(t, x)
	x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D1", Kind: KindTurn, Step: 3})
	if len(r.scrolls) != 0 {
		t.Errorf("scrolled while disarmed: %v", r.scrolls)
	}
}
