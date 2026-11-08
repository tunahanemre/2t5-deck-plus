package preset

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/config"
)

func deckControls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "editorapi", "profile", "loupedeck-plus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Controls []struct {
			ID string `json:"id"`
		} `json:"controls"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range p.Controls {
		out = append(out, c.ID)
	}
	return out
}

func deckStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "presets")
	s, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, Controls: deckControls(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestValidBundleID(t *testing.T) {
	good := []string{"com.google.Chrome", "com.apple.finder", "org.blenderfoundation.blender", "com.googlecode.iterm2",
		"company.thebrowser.Browser", "com.tinyspeck.slackmacgap", "a.b", "net.my-app.Some_App"}
	bad := []string{"", "notabundle", "Chrome", "com..Chrome", ".com.x", "com.x.", "1com.x", "com.x y", "com/x.y", "../x.y",
		"com.x.y\n", strings.Repeat("a.", 200) + "b", "_global", "com.-x.y"}
	for _, g := range good {
		if !ValidBundleID(g) {
			t.Errorf("%q should be valid", g)
		}
	}
	for _, b := range bad {
		if ValidBundleID(b) {
			t.Errorf("%q should be invalid", b)
		}
	}
}

func TestRemoveApp(t *testing.T) {
	s := deckStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "none"})
	before := len(s.undo)
	if err := s.RemoveApp(chrome); err != nil {
		t.Fatal(err)
	}
	if len(s.undo) != before+1 {
		t.Errorf("removal is %d undo steps", len(s.undo)-before)
	}
	if _, ok := s.Snapshot().Preset(chrome); ok {
		t.Fatal("still there")
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), chrome+".json")); !os.IsNotExist(err) {
		t.Error("file not removed")
	}
	if _, ok, err := s.Undo(); err != nil || !ok {
		t.Fatalf("undo: %v %v", ok, err)
	}
	p, ok := s.Snapshot().Preset(chrome)
	if !ok || p.Assigned() != 1 {
		t.Fatal("undo did not bring the app back")
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), chrome+".json")); err != nil {
		t.Error("file not restored by undo")
	}
	for _, id := range []string{GlobalID, "global", ResolveID} {
		if err := s.RemoveApp(id); !errors.Is(err, ErrSystemApp) {
			t.Errorf("RemoveApp(%s) = %v", id, err)
		}
	}
	if err := s.RemoveApp("com.nope.App"); !errors.Is(err, ErrNoPreset) {
		t.Errorf("unknown = %v", err)
	}
}

func TestRisk(t *testing.T) {
	cases := []struct {
		spec string
		want bool
	}{
		{"Cmd+Q", true}, {"cmd+q", true}, {"Command+Q", true}, {"Cmd+W", true}, {"Cmd+Shift+W", true}, {"Alt+Cmd+Q", true},
		{"Delete", true}, {"Cmd+Delete", true}, {"Cmd+Shift+Delete", true}, {"Shift+ForwardDelete", true},
		{"Q", false}, {"W", false}, {"Ctrl+W", false}, {"Cmd+T", false}, {"Cmd+Z", false}, {"Cmd+Shift+Z", false}, {"Cmd+0", false},
	}
	for _, c := range cases {
		if got := actions.KeyIsDestructive(c.spec); got != c.want {
			t.Errorf("KeyIsDestructive(%q) = %v", c.spec, got)
		}
	}
	reg := actions.Default()
	for rawJSON, want := range map[string]string{
		`{"type":"key","key":"Cmd+T"}`:                                                "keys",
		`{"type":"key","key":"Cmd+Q"}`:                                                "destructive",
		`{"type":"keyDelta","keyCW":"Right","keyCCW":"Cmd+Delete"}`:                   "destructive",
		`{"type":"keyDelta","keyCW":"Right","keyCCW":"Left"}`:                         "keys",
		`{"type":"shell","cmd":"/usr/bin/open"}`:                                      "shell",
		`{"type":"scrollDelta","linesPerDetent":3}`:                                   "safe",
		`{"type":"layer","layer":2}`:                                                  "safe",
		`{"type":"wheel","wheel":"lift_master","press":{"type":"key","key":"Cmd+W"}}`: "destructive",
		`{"type":"wheel","wheel":"lift_master","press":{"type":"key","key":"Cmd+Z"}}`: "keys",
		`{"type":"jog"}`:                "safe",
		`{"action":"browser.closeTab"}`: "destructive",
		`{"action":"browser.reload"}`:   "keys",
	} {
		a := rawMap(t, rawJSON)
		if na, err := reg.Normalize(a); err == nil {
			a = na
		}
		if got := reg.RiskOfRaw(a); got != want {
			t.Errorf("risk(%s) = %s, want %s", rawJSON, got, want)
		}
	}
	// registry carries risk on every action
	for _, p := range reg.Packs() {
		for _, a := range p.Actions {
			switch a.Risk {
			case "safe", "keys", "shell", "destructive":
			default:
				t.Errorf("%s has risk %q", a.ID, a.Risk)
			}
		}
	}
	want := map[string]string{"generic.key": "keys", "generic.keyDelta": "keys", "generic.shell": "shell", "browser.closeTab": "destructive",
		"browser.tabs": "keys", "browser.scroll": "safe", "generic.layer": "safe", "resolve.scrubFrames": "safe", "generic.none": "safe"}
	for id, w := range want {
		a, _ := reg.Get(id)
		if a.Risk != w {
			t.Errorf("registry %s risk = %s, want %s", id, a.Risk, w)
		}
	}
}

func TestEffectiveCarriesRisk(t *testing.T) {
	s := deckStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+W"})
	mustAssign(t, s, chrome, "base", "C2", map[string]any{"type": "key", "key": "Cmd+T"})
	e, ok := s.Snapshot().Effective(chrome, "base", "C1")
	if !ok || e.Risk != "destructive" {
		t.Errorf("C1 risk = %q", e.Risk)
	}
	e, _ = s.Snapshot().Effective(chrome, "base", "C2")
	if e.Risk != "keys" {
		t.Errorf("C2 risk = %q", e.Risk)
	}
	// inherited from Global
	mustAssign(t, s, GlobalID, "base", "C3", map[string]any{"type": "shell", "cmd": "/usr/bin/true"})
	e, _ = s.Snapshot().Effective(chrome, "base", "C3")
	if e.Risk != "shell" || e.Source != "global" {
		t.Errorf("inherited risk = %q source %q", e.Risk, e.Source)
	}
}

// ---- templates ---------------------------------------------------------------------

func TestTemplatesAreValidAndSafe(t *testing.T) {
	if err := TemplatesError(); err != nil {
		t.Fatal(err)
	}
	tpls := Templates()
	if len(tpls) < 6 {
		t.Fatalf("only %d templates", len(tpls))
	}
	wantBundles := map[string]string{"google-chrome": "com.google.Chrome", "safari": "com.apple.Safari", "blender": "org.blenderfoundation.blender",
		"adobe-photoshop": "com.adobe.Photoshop", "terminal": "com.apple.Terminal", "iterm2": "com.googlecode.iterm2"}
	got := map[string]string{}
	for _, tp := range tpls {
		got[tp.ID] = tp.BundleID
		if !ValidBundleID(tp.BundleID) || tp.Name == "" || tp.Summary == "" || tp.Controls == 0 {
			t.Errorf("template %+v incomplete", tp)
		}
		// each one creates cleanly in a store restricted to the real deck controls
		s := deckStore(t)
		p, _, err := s.AddFromTemplate(tp.ID)
		if err != nil {
			t.Errorf("%s: %v", tp.ID, err)
			continue
		}
		if p.ID != tp.BundleID || p.Assigned() != tp.Controls {
			t.Errorf("%s: created %s with %d controls, listed %d", tp.ID, p.ID, p.Assigned(), tp.Controls)
		}
		snap := s.Snapshot()
		for bn, bm := range p.Banks {
			for cn := range bm {
				e, ok := snap.Effective(p.ID, bn, cn)
				if !ok {
					t.Errorf("%s %s.%s: no effective", tp.ID, bn, cn)
					continue
				}
				if e.Risk == "destructive" || e.Risk == "shell" {
					t.Errorf("%s %s.%s is %s: %s", tp.ID, bn, cn, e.Risk, e.Summary)
				}
			}
		}
		for _, w := range snap.Warnings(func(c string) bool { return true }) {
			if w.App == p.ID && w.Severity == "warn" {
				t.Errorf("%s: warning %+v", tp.ID, w)
			}
		}
		// the composed config loads
		if _, err := snap.ComposeBindingsJSON(); err != nil {
			t.Errorf("%s: compose: %v", tp.ID, err)
		}
	}
	for id, b := range wantBundles {
		if got[id] != b {
			t.Errorf("template %s bundle = %q, want %q", id, got[id], b)
		}
	}
}

func TestAddFromTemplateRules(t *testing.T) {
	s := deckStore(t)
	before := len(s.undo)
	p, tp, err := s.AddFromTemplate("safari")
	if err != nil || p.ID != "com.apple.Safari" || tp.ID != "safari" {
		t.Fatal(p, tp, err)
	}
	if len(s.undo) != before+1 {
		t.Errorf("template add is %d undo steps", len(s.undo)-before)
	}
	if _, _, err := s.AddFromTemplate("safari"); !errors.Is(err, ErrExists) {
		t.Errorf("second add = %v", err)
	}
	if _, _, err := s.AddFromTemplate("nope"); !errors.Is(err, ErrNoTemplate) {
		t.Errorf("unknown = %v", err)
	}
	// the two Chrome layouts share a bundle id: the second is refused
	if _, _, err := s.AddFromTemplate("google-chrome-comfyui"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AddFromTemplate("google-chrome"); !errors.Is(err, ErrExists) {
		t.Errorf("same bundle = %v", err)
	}
	// stored form equals what an editor assign would store (round trip stable)
	snap := s.Snapshot()
	sp, _ := snap.Preset("com.apple.Safari")
	b, _ := sp.Marshal()
	again, err := ParsePreset(b)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := again.Marshal()
	if string(b) != string(b2) {
		t.Error("not byte-stable")
	}
	// one undo removes the app again
	if _, ok, _ := s.Undo(); !ok {
		t.Fatal("undo")
	}
	if _, ok := s.Snapshot().Preset("com.apple.Safari"); !ok {
		// undo reverted the LAST step (the comfyui chrome); safari must remain
		t.Error("undo removed the wrong app")
	}
	// a template with the registry packs: the keys of every template pass the key validator
	for _, tp := range Templates() {
		for _, bm := range tp.preset.Banks {
			for cn, a := range bm {
				na, err := actions.Default().Normalize(a)
				if err != nil || actions.Default().Validate(na, true) != nil {
					t.Errorf("%s %s does not validate: %v", tp.ID, cn, err)
				}
			}
		}
	}
}

// ---- coverage lens -----------------------------------------------------------------

func TestCoverageOverriddenAndChangedFromDefault(t *testing.T) {
	s, _ := seededStore(t)
	mustAssign(t, s, GlobalID, "base", "C1", map[string]any{"type": "key", "key": "Cmd+Z"})
	mustAssign(t, s, GlobalID, "base", "C2", map[string]any{"type": "key", "key": "Cmd+Y"})
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+Z"}) // same as Global: not an override
	mustAssign(t, s, chrome, "base", "C2", map[string]any{"type": "key", "key": "Cmd+T"}) // differs
	mustAssign(t, s, chrome, "base", "C3", map[string]any{"type": "key", "key": "Cmd+R"}) // only in the app
	cov, ok := s.CoverageLens(chrome, []string{"C1", "C2", "C3", "C4"})
	if !ok {
		t.Fatal("no coverage")
	}
	var base CoverageBank
	for _, b := range cov.Banks {
		if b.Bank == "base" {
			base = b
		}
	}
	by := map[string]CoverageControl{}
	for _, c := range base.Controls {
		by[c.ID] = c
	}
	for id, want := range map[string]bool{"C1": false, "C2": true, "C3": true, "C4": false} {
		if by[id].Overridden != want {
			t.Errorf("%s overridden = %v, want %v (%+v)", id, by[id].Overridden, want, by[id])
		}
	}
	// no seed for Chrome: everything the app assigns reads as changed from the empty default
	if !by["C2"].ChangedFromDefault || by["C4"].ChangedFromDefault {
		t.Errorf("chrome changedFromDefault: %+v %+v", by["C2"], by["C4"])
	}
	// Global never overrides
	gcov, _ := s.CoverageLens(GlobalID, []string{"C1"})
	for _, b := range gcov.Banks {
		for _, c := range b.Controls {
			if c.Overridden {
				t.Errorf("Global control %s overridden", c.ID)
			}
		}
	}

	// Resolve vs its .seed: untouched = no change, edited = changed, removed = changed
	rp, _ := s.Snapshot().Preset(ResolveID)
	var bank, ctl string
	for bn, bm := range rp.Banks {
		for cn := range bm {
			bank, ctl = bn, cn
			break
		}
		if ctl != "" {
			break
		}
	}
	rcov, _ := s.CoverageLens(ResolveID, []string{ctl})
	for _, b := range rcov.Banks {
		for _, c := range b.Controls {
			if c.ChangedFromDefault {
				t.Errorf("fresh seed reads as changed: %s %s", b.Bank, c.ID)
			}
		}
	}
	if err := s.SetValue(ResolveID, bank, ctl, "label", "Edited label"); err != nil {
		t.Fatal(err)
	}
	rcov, _ = s.CoverageLens(ResolveID, []string{ctl})
	var seen bool
	for _, b := range rcov.Banks {
		if b.Bank != bank {
			continue
		}
		for _, c := range b.Controls {
			seen = true
			if !c.ChangedFromDefault {
				t.Errorf("edited control not flagged: %+v", c)
			}
		}
	}
	if !seen {
		t.Fatal("bank missing")
	}
	if err := s.Unassign(ResolveID, bank, ctl); err != nil {
		t.Fatal(err)
	}
	rcov, _ = s.CoverageLens(ResolveID, []string{ctl})
	for _, b := range rcov.Banks {
		if b.Bank != bank {
			continue
		}
		for _, c := range b.Controls {
			if !c.ChangedFromDefault {
				t.Errorf("removed control not flagged: %+v", c)
			}
		}
	}
}

// ---- fine factor -------------------------------------------------------------------

func TestFineFactorSettingValidatesAndReloads(t *testing.T) {
	s, _ := seededStore(t)
	if err := s.SetSetting(ResolveID, "fineFactor", jsonNumber("0.25")); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Snapshot().Preset(ResolveID)
	if v, _ := p.Settings["fineFactor"].(json.Number); v.String() != "0.25" {
		t.Errorf("stored %v", p.Settings["fineFactor"])
	}
	// the composed config the engine hot-reloads carries it
	raw, err := s.Snapshot().ComposeBindingsJSON()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bp := filepath.Join(dir, "bindings.json")
	os.WriteFile(bp, raw, 0o644)
	cfg, err := config.Load(filepath.Join("..", "..", "config", "device-map.json"), bp)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TopLevel.FineFactor != 0.25 {
		t.Errorf("config FineFactor = %v", cfg.TopLevel.FineFactor)
	}
	for _, bad := range []any{jsonNumber("0"), jsonNumber("0.01"), jsonNumber("1.5"), jsonNumber("-0.5"), "half", true} {
		if err := s.SetSetting(ResolveID, "fineFactor", bad); err == nil {
			t.Errorf("fineFactor %v accepted", bad)
		}
	}
	// reaches a snapshot change (hot reload fires)
	var n int
	s.OnChange(func(*Snapshot) { n++ })
	if err := s.SetSetting(ResolveID, "fineFactor", jsonNumber("0.1")); err != nil || n != 1 {
		t.Errorf("set 0.1: %v, notified %d", err, n)
	}
	// clearing restores the engine default (key removed)
	if err := s.SetSetting(ResolveID, "fineFactor", nil); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Snapshot().Preset(ResolveID)
	if _, has := p.Settings["fineFactor"]; has {
		t.Error("not cleared")
	}
	// only Resolve has a fine factor
	mustAdd(t, s, chrome)
	if err := s.SetSetting(chrome, "fineFactor", jsonNumber("0.5")); err == nil {
		t.Error("fineFactor accepted on a non-Resolve preset")
	}
	// unknown keys still pass through
	if err := s.SetSetting(chrome, "whatever", 3); err != nil {
		t.Errorf("free-form setting: %v", err)
	}
}

func rawMap(t *testing.T, s string) map[string]any {
	t.Helper()
	return raw(t, s)
}
