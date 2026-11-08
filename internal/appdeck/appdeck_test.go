package appdeck

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"2t5-deck-plus/internal/preset"
)

const chrome = "com.google.Chrome"

type rec struct {
	mu      sync.Mutex
	keys    []string
	scrolls []string
	shells  []string
}

func (r *rec) exec() *Executor {
	x := New()
	x.SendKey = func(s string) error { r.mu.Lock(); r.keys = append(r.keys, s); r.mu.Unlock(); return nil }
	x.Scroll = func(d int, f uint64) {
		r.mu.Lock()
		r.scrolls = append(r.scrolls, fmt.Sprintf("%d/%d", d, f))
		r.mu.Unlock()
	}
	x.ParseFlags = func(s string) (uint64, error) {
		if s == "bad" {
			return 0, fmt.Errorf("bad flags")
		}
		return 8, nil
	}
	x.Shell = func(c string, a []string) {
		r.mu.Lock()
		r.shells = append(r.shells, fmt.Sprintf("%s %v", c, a))
		r.mu.Unlock()
	}
	return x
}

func store(t *testing.T) *preset.Store {
	t.Helper()
	s, err := preset.Open(t.TempDir(), preset.Options{PollInterval: -1, LiveResolvePath: "-"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.AddApp(chrome, "Google Chrome", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Snapshot().Preset(preset.ResolveID); !ok {
		if _, err := s.AddApp(preset.ResolveID, "DaVinci Resolve", nil); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func assign(t *testing.T, s *preset.Store, app, bank, control string, a map[string]any) {
	t.Helper()
	if err := s.Assign(app, bank, control, a); err != nil {
		t.Fatalf("assign %s/%s/%s: %v", app, bank, control, err)
	}
}

func setup(t *testing.T) (*Executor, *rec, *preset.Store) {
	s := store(t)
	r := &rec{}
	x := r.exec()
	x.SetSnapshot(s.Snapshot())
	return x, r, s
}

func (x *Executor) swap(s *preset.Store) { x.SetSnapshot(s.Snapshot()) }

func TestChromeScrollTurn(t *testing.T) {
	x, r, s := setup(t)
	assign(t, s, chrome, "base", "D1", map[string]any{"type": "scrollDelta", "linesPerDetent": 3, "detentThreshold": 2})
	x.swap(s)
	// 1 raw tick: below the threshold of 2, nothing fires but the event is handled.
	res := x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D1", Kind: KindTurn, Step: 1})
	if !res.Handled || res.Fired || len(r.scrolls) != 0 {
		t.Fatalf("sub-threshold: %+v scrolls=%v", res, r.scrolls)
	}
	// 5 more ticks: accumulator 1+5=6 -> 3 detents -> 9 lines.
	res = x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D1", Kind: KindTurn, Step: 5})
	if !res.Fired || res.Value != 9 || res.Unit != "lines" || len(r.scrolls) != 1 || r.scrolls[0] != "9/0" {
		t.Fatalf("scroll: %+v %v", res, r.scrolls)
	}
	if res.Source != "app" || res.PresetID != chrome || res.Bank != chrome {
		t.Fatalf("provenance: %+v", res)
	}
	// counter-clockwise reverses
	x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D1", Kind: KindTurn, Step: -4})
	if got := r.scrolls[len(r.scrolls)-1]; got != "-6/0" {
		t.Fatalf("ccw scroll = %s", got)
	}
}

func TestScrollFlagsAndBadFlags(t *testing.T) {
	x, r, s := setup(t)
	assign(t, s, chrome, "base", "D1", map[string]any{"type": "scrollDelta", "flags": "Alt"})
	assign(t, s, chrome, "base", "D2", map[string]any{"type": "scrollDelta", "flags": "bad"})
	x.swap(s)
	x.Handle(Input{Bundle: chrome, Control: "D1", Kind: KindTurn, Step: 1})
	if len(r.scrolls) != 1 || r.scrolls[0] != "1/8" {
		t.Fatalf("flags scroll = %v", r.scrolls)
	}
	x.Handle(Input{Bundle: chrome, Control: "D2", Kind: KindTurn, Step: 1})
	if len(r.scrolls) != 1 {
		t.Fatalf("bad flags must not scroll: %v", r.scrolls)
	}
}

func TestTabSwitchKeyDelta(t *testing.T) {
	x, r, s := setup(t)
	assign(t, s, chrome, "base", "D2", map[string]any{"type": "keyDelta", "keyCW": "Cmd+Alt+Right", "keyCCW": "Cmd+Alt+Left", "detentThreshold": 1})
	x.swap(s)
	x.Handle(Input{Bundle: chrome, Control: "D2", Kind: KindTurn, Step: 2})
	x.Handle(Input{Bundle: chrome, Control: "D2", Kind: KindTurn, Step: -1})
	want := []string{"Cmd+Alt+Right", "Cmd+Alt+Right", "Cmd+Alt+Left"}
	if fmt.Sprint(r.keys) != fmt.Sprint(want) {
		t.Fatalf("keys = %v, want %v", r.keys, want)
	}
}

func TestAccumulatorPerControlAndApp(t *testing.T) {
	x, r, s := setup(t)
	for _, c := range []string{"D1", "D2"} {
		assign(t, s, chrome, "base", c, map[string]any{"type": "keyDelta", "keyCW": "Right", "keyCCW": "Left", "detentThreshold": 3})
	}
	assign(t, s, preset.GlobalID, "base", "D3", map[string]any{"type": "keyDelta", "keyCW": "Up", "keyCCW": "Down", "detentThreshold": 3})
	x.swap(s)
	x.Handle(Input{Bundle: chrome, Control: "D1", Kind: KindTurn, Step: 2})
	x.Handle(Input{Bundle: chrome, Control: "D2", Kind: KindTurn, Step: 2}) // separate accumulator
	if len(r.keys) != 0 {
		t.Fatalf("accumulators leaked across controls: %v", r.keys)
	}
	x.Handle(Input{Bundle: chrome, Control: "D1", Kind: KindTurn, Step: 1})
	if len(r.keys) != 1 {
		t.Fatalf("D1 should fire once: %v", r.keys)
	}
	// Global control used from two apps keeps separate partial detents.
	x.Handle(Input{Bundle: chrome, Control: "D3", Kind: KindTurn, Step: 2})
	x.Handle(Input{Bundle: "com.apple.Safari", Control: "D3", Kind: KindTurn, Step: 2})
	if len(r.keys) != 1 {
		t.Fatalf("per-app accumulators leaked: %v", r.keys)
	}
}

func TestGlobalFallbackAndOverride(t *testing.T) {
	x, r, s := setup(t)
	assign(t, s, preset.GlobalID, "base", "C1", map[string]any{"type": "key", "key": "Cmd+C"})
	assign(t, s, chrome, "base", "C2", map[string]any{"type": "key", "key": "Cmd+T"})
	assign(t, s, preset.GlobalID, "base", "C2", map[string]any{"type": "key", "key": "Cmd+V"})
	x.swap(s)
	res := x.Handle(Input{Bundle: chrome, Control: "C1", Kind: KindPress})
	if !res.Handled || res.Source != "global" || res.PresetID != preset.GlobalID {
		t.Fatalf("global fallback: %+v", res)
	}
	res = x.Handle(Input{Bundle: chrome, Control: "C2", Kind: KindPress})
	if res.Source != "app" || r.keys[len(r.keys)-1] != "Cmd+T" {
		t.Fatalf("app must win: %+v keys=%v", res, r.keys)
	}
	// Unknown app, no preset: Global.
	res = x.Handle(Input{Bundle: "org.unknown.App", Control: "C2", Kind: KindPress})
	if res.Source != "global" || r.keys[len(r.keys)-1] != "Cmd+V" {
		t.Fatalf("unknown app: %+v keys=%v", res, r.keys)
	}
}

func TestPerValueInheritanceChangesOutput(t *testing.T) {
	x, r, s := setup(t)
	// Global carries the scroll distance; Chrome's assignment omits the key.
	assign(t, s, preset.GlobalID, "base", "D1", map[string]any{"type": "scrollDelta", "linesPerDetent": 5})
	assign(t, s, chrome, "base", "D1", map[string]any{"type": "scrollDelta"})
	x.swap(s)
	x.Handle(Input{Bundle: chrome, Control: "D1", Kind: KindTurn, Step: 1})
	if r.scrolls[0] != "5/0" {
		t.Fatalf("inherited lines: %v", r.scrolls)
	}
	if err := s.SetValue(chrome, "base", "D1", "linesPerDetent", 2); err != nil {
		t.Fatal(err)
	}
	x.swap(s)
	x.Handle(Input{Bundle: chrome, Control: "D1", Kind: KindTurn, Step: 1})
	if r.scrolls[1] != "2/0" {
		t.Fatalf("own value must override: %v", r.scrolls)
	}
	if err := s.SetValue(chrome, "base", "D1", "linesPerDetent", nil); err != nil {
		t.Fatal(err)
	}
	x.swap(s)
	x.Handle(Input{Bundle: chrome, Control: "D1", Kind: KindTurn, Step: 1})
	if r.scrolls[2] != "5/0" {
		t.Fatalf("cleared value must inherit again: %v", r.scrolls)
	}
}

func TestUnassignedAndForeignVerbs(t *testing.T) {
	x, r, s := setup(t)
	x.swap(s)
	for _, k := range []string{KindPress, KindTurn, KindRelease} {
		if res := x.Handle(Input{Bundle: chrome, Control: "Nope", Kind: k, Step: 1}); res.Handled || res.Fired {
			t.Fatalf("unassigned %s handled: %+v", k, res)
		}
	}
	if len(r.keys)+len(r.scrolls)+len(r.shells) != 0 {
		t.Fatalf("unassigned control acted: %+v", r)
	}
	// A Resolve verb (assignable to Global via the generic seed? use Resolve app) is not run.
	assign(t, s, preset.ResolveID, "col", "C3", map[string]any{"type": "jogMinuteSnap"})
	x.swap(s)
	if res := x.Handle(Input{Bundle: preset.ResolveID, Control: "C3", Kind: KindTurn, Step: 1}); res.Handled {
		t.Fatalf("resolve frontmost with Resolve-only binding must not be handled: %+v", res)
	}
	// A turn verb on a press (and vice versa) is not handled.
	assign(t, s, chrome, "base", "D1", map[string]any{"type": "scrollDelta"})
	x.swap(s)
	if res := x.Handle(Input{Bundle: chrome, Control: "D1", Kind: KindPress}); res.Handled {
		t.Fatalf("press on a turn verb handled: %+v", res)
	}
}

func TestResolveFrontmostFallsThroughToGlobal(t *testing.T) {
	x, r, s := setup(t)
	assign(t, s, preset.GlobalID, "base", "C9", map[string]any{"type": "key", "key": "Cmd+Shift+4"})
	x.swap(s)
	res := x.Handle(Input{Bundle: preset.ResolveID, Control: "C9", Kind: KindPress})
	if !res.Handled || res.Source != "global" || r.keys[0] != "Cmd+Shift+4" {
		t.Fatalf("global via resolve: %+v keys=%v", res, r.keys)
	}
}

func TestNoneBlocksFallbackAndEffects(t *testing.T) {
	x, r, s := setup(t)
	assign(t, s, preset.GlobalID, "base", "C1", map[string]any{"type": "key", "key": "Cmd+C"})
	assign(t, s, chrome, "base", "C1", map[string]any{"type": "none"})
	assign(t, s, chrome, "base", "L1", map[string]any{"type": "layer", "layer": 2})
	assign(t, s, chrome, "base", "Cmd", map[string]any{"type": "modifierHold", "modifier": "shift"})
	assign(t, s, chrome, "base", "C6", map[string]any{"type": "toggleHud"})
	x.swap(s)
	if res := x.Handle(Input{Bundle: chrome, Control: "C1", Kind: KindPress}); !res.Handled || res.Fired || len(r.keys) != 0 {
		t.Fatalf("none: %+v keys=%v", res, r.keys)
	}
	if res := x.Handle(Input{Bundle: chrome, Control: "L1", Kind: KindPress}); res.Effect == nil || res.Effect.Kind != "layer" || res.Effect.Layer != 2 {
		t.Fatalf("layer: %+v", res)
	}
	if res := x.Handle(Input{Bundle: chrome, Control: "Cmd", Kind: KindPress}); res.Effect == nil || res.Effect.Kind != "modifierHold" || res.Effect.Modifier != "shift" {
		t.Fatalf("modifierHold: %+v", res)
	}
	if res := x.Handle(Input{Bundle: chrome, Control: "Cmd", Kind: KindRelease}); res.Effect == nil || res.Effect.Kind != "modifierRelease" {
		t.Fatalf("modifier release: %+v", res)
	}
	if res := x.Handle(Input{Bundle: chrome, Control: "C6", Kind: KindPress}); res.Effect == nil || res.Effect.Kind != "toggleHud" {
		t.Fatalf("toggleHud: %+v", res)
	}
}

func TestShellSeam(t *testing.T) {
	x, r, s := setup(t)
	assign(t, s, chrome, "base", "C4", map[string]any{"type": "shell", "cmd": "/usr/bin/open", "args": []any{"-a", "Safari"}})
	x.swap(s)
	res := x.Handle(Input{Bundle: chrome, Control: "C4", Kind: KindPress})
	if !res.Fired || len(r.shells) != 1 || r.shells[0] != "/usr/bin/open [-a Safari]" {
		t.Fatalf("shell: %+v %v", res, r.shells)
	}
}

func TestRunShellRunsAsyncWithTwoSecondDeadline(t *testing.T) {
	type call struct {
		cmd      string
		args     []string
		deadline time.Duration
	}
	ch := make(chan call, 1)
	release := make(chan struct{})
	old := execFn
	defer func() { execFn = old }()
	execFn = func(ctx context.Context, cmd string, args []string) error {
		d, _ := ctx.Deadline()
		ch <- call{cmd, args, time.Until(d)}
		<-release // would block the caller if runShell were synchronous
		return nil
	}
	done := make(chan struct{})
	go func() { runShell("/bin/echo", []string{"a b", "$HOME"}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runShell blocked the caller")
	}
	c := <-ch
	close(release)
	if c.cmd != "/bin/echo" || len(c.args) != 2 || c.args[1] != "$HOME" {
		t.Fatalf("args must pass through unexpanded: %+v", c)
	}
	if c.deadline <= 1500*time.Millisecond || c.deadline > 2*time.Second {
		t.Fatalf("deadline = %v, want ~2s", c.deadline)
	}
}

func TestAssignsIndexExcludesResolve(t *testing.T) {
	x, _, s := setup(t)
	assign(t, s, preset.ResolveID, "col", "C7", map[string]any{"type": "key", "key": "Cmd+Z"})
	assign(t, s, chrome, "base", "C8", map[string]any{"type": "key", "key": "Cmd+Z"})
	x.swap(s)
	if x.Assigns("C7") || !x.Assigns("C8") {
		t.Fatalf("Assigns: C7=%v C8=%v", x.Assigns("C7"), x.Assigns("C8"))
	}
}

// Fn + C6 on the Global preset toggles the HUD from any app, and plain C6 does not.
func TestGlobalFnC6TogglesHudFromAnyApp(t *testing.T) {
	x, _, s := setup(t)
	assign(t, s, preset.GlobalID, "fn", "C6", map[string]any{"type": "toggleHud"})
	x.swap(s)
	for _, app := range []string{chrome, "com.apple.finder", preset.ResolveID} {
		res := x.Handle(Input{Bundle: app, Mod: "fn", Control: "C6", Kind: KindPress})
		if !res.Handled || res.Effect == nil || res.Effect.Kind != "toggleHud" {
			t.Fatalf("Fn+C6 in %s: %+v", app, res)
		}
		if res := x.Handle(Input{Bundle: app, Control: "C6", Kind: KindPress}); res.Effect != nil {
			t.Fatalf("plain C6 in %s must not toggle the HUD: %+v", app, res)
		}
	}
}

func TestPressTypeResolvesGlobalForAnUnknownBundle(t *testing.T) {
	x, _, s := setup(t)
	assign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "modifierHold", "modifier": "fn"})
	assign(t, s, preset.GlobalID, "fn", "C6", map[string]any{"type": "toggleHud"})
	x.swap(s)
	if typ, m := x.PressType("com.2t5.deck-plus", "", 1, "C8"); typ != "modifierHold" || m != "fn" {
		t.Fatalf("Fn hold: %q %q", typ, m)
	}
	if typ, _ := x.PressType("com.2t5.deck-plus", "fn", 1, "C6"); typ != "toggleHud" {
		t.Fatalf("Fn+C6: %q", typ)
	}
	if typ, _ := x.PressType("com.2t5.deck-plus", "", 1, "C6"); typ == "toggleHud" {
		t.Fatalf("plain C6 must not be toggleHud")
	}
}
