package preset

import (
	"2t5-deck-plus/internal/actions"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- H3: import without programs ------------------------------------------------

const riskyChrome = `{"base":{
 "C1":{"type":"key","key":"Cmd+T"},
 "C2":{"type":"shell","cmd":"/usr/bin/open","args":["-a","Calculator"]},
 "C3":{"type":"key","key":"Cmd+W"},
 "C4":{"type":"scrollDelta","linesPerDetent":3,"detentThreshold":1}}}`

func riskyBundle(t *testing.T) []byte {
	return bundleOf(t, map[string][]byte{chrome + ".json": pjson(chrome, riskyChrome)})
}

func TestImportSkipRisky_DryRunNamesWhatIsDropped(t *testing.T) {
	s, _ := emptyStore(t)
	rep, err := s.ImportOpts(riskyBundle(t), true, ImportOptions{SkipRisky: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.NeedsConfirm || rep.Risk.Shell != 0 || rep.Risk.Destructive != 0 {
		t.Errorf("risk after skipping: %+v confirm=%v", rep.Risk, rep.NeedsConfirm)
	}
	if len(rep.Dropped) != 2 {
		t.Fatalf("dropped = %+v", rep.Dropped)
	}
	got := map[string]string{}
	for _, d := range rep.Dropped {
		if d.Preset != chrome || d.Bank != "base" {
			t.Errorf("dropped = %+v", d)
		}
		got[d.Control] = d.Action
	}
	if got["C2"] == "" || got["C3"] == "" || got["C1"] != "" || got["C4"] != "" {
		t.Errorf("dropped controls = %v", got)
	}
	if rep.Files[0].Added != 2 { // C1 and C4 would be added
		t.Errorf("added = %d", rep.Files[0].Added)
	}
	if _, ok := s.Snapshot().Preset(chrome); ok {
		t.Error("a dry run wrote")
	}
	// without skipRisky the same bundle needs a confirmation, as before
	rep, err = s.ImportOpts(riskyBundle(t), true, ImportOptions{})
	if err != nil || !rep.NeedsConfirm || len(rep.Dropped) != 0 {
		t.Errorf("plain dry run: %+v %v", rep, err)
	}
}

func TestImportSkipRisky_ApplyIsOneUndoStepAndKeepsTheUsersOwn(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+Y")) // the user already has a (safe) C2
	before := s.Snapshot()
	nUndo, _ := s.History()
	rep, err := s.ImportOpts(riskyBundle(t), false, ImportOptions{SkipRisky: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Dropped) != 2 {
		t.Fatalf("dropped = %+v", rep.Dropped)
	}
	p, _ := s.Snapshot().Preset(chrome)
	base := p.Banks["base"]
	if base["C1"] == nil || base["C4"] == nil {
		t.Errorf("safe assignments not imported: %v", base)
	}
	if base["C3"] != nil {
		t.Error("destructive assignment imported")
	}
	if a := base["C2"]; a == nil || a["key"] != "Cmd+Y" {
		t.Errorf("the user's own C2 was replaced by the dropped shell action: %v", a)
	}
	after, _ := s.History()
	if len(after) != len(nUndo)+1 {
		t.Errorf("history grew by %d, want 1 undo step", len(after)-len(nUndo))
	}
	if _, ok, err := s.Undo(); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if p, _ := s.Snapshot().Preset(chrome); p.Assigned() != before.presets[chrome].Assigned() {
		t.Error("one undo did not bring the old state back")
	}
}

func TestImportSkipRisky_NothingRiskyDropsNothing(t *testing.T) {
	s, _ := emptyStore(t)
	b := bundleOf(t, map[string][]byte{chrome + ".json": pjson(chrome, `{"base":{"C1":{"type":"key","key":"Cmd+T"}}}`)})
	rep, err := s.ImportOpts(b, false, ImportOptions{SkipRisky: true})
	if err != nil || len(rep.Dropped) != 0 || rep.Dropped == nil {
		t.Errorf("%+v %v (Dropped must be an empty array)", rep, err)
	}
	if strings.Contains(strings.ToLower(err2s(err)), "refused") {
		t.Error("refused")
	}
}

func err2s(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

// ---- H4: the hint for destructive actions without a hold --------------------------------

func TestWarnings_DestructiveWithoutHoldSuggestsIt(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+W"})
	mustAssign(t, s, chrome, "base", "C2", map[string]any{"type": "key", "key": "Cmd+W", "holdMs": 500})
	mustAssign(t, s, chrome, "base", "C3", map[string]any{"type": "key", "key": "Cmd+T"})
	mustAssign(t, s, chrome, "base", "C4", map[string]any{"type": "key", "key": "Cmd+Q", "holdMs": 0})
	var got []Warning
	for _, w := range s.Warnings() {
		if w.Kind == "destructive-no-hold" {
			got = append(got, w)
		}
	}
	if len(got) != 2 || got[0].Control != "C1" || got[1].Control != "C4" {
		t.Fatalf("hints = %+v", got)
	}
	for _, w := range got {
		if w.Severity != "hint" || w.Suggest != "holdMs" || w.Field != "holdMs" || w.App != chrome {
			t.Errorf("hint = %+v", w)
		}
	}
}

func TestRegistry_HoldMsOnKeyAndShellPressesOnly(t *testing.T) {
	s, _ := emptyStore(t)
	reg := s.Registry()
	for _, id := range []string{"generic.key", "generic.shell"} {
		a, _ := reg.Get(id)
		f, ok := a.FieldByKey("holdMs")
		if !ok || f.Kind != "int" || f.Label != "Hold to fire (ms)" || f.Primary || *f.Max != 2000 || *f.Min != 0 {
			t.Errorf("%s holdMs = %+v ok=%v", id, f, ok)
		}
		if f.Default != 0.0 {
			t.Errorf("%s default %v: never seeded, off by default", id, f.Default)
		}
	}
	for _, id := range []string{"generic.keyDelta", "generic.layer", "generic.modifierHold", "generic.scrollDelta"} {
		if a, _ := reg.Get(id); func() bool { _, ok := a.FieldByKey("holdMs"); return ok }() {
			t.Errorf("%s has holdMs", id)
		}
	}
	mustAdd(t, s, chrome)
	for _, bad := range []any{2001, -1, 1.5, "x"} {
		if err := s.Assign(chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+T", "holdMs": bad}); err == nil {
			t.Errorf("holdMs=%v accepted", bad)
		}
	}
	if err := s.Assign(chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+T", "holdMs": 2000}); err != nil {
		t.Error(err)
	}
	if err := s.SetValue(chrome, "base", "C1", "holdMs", 300); err != nil {
		t.Error(err)
	}
}

// ---- H8: labelled snapshots, diffs, copy dry run -------------------------------------------

func TestBackupLabel_StoredAndListed(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	b, ok, err := s.BackupNowLabelled("  before the shoot\n\t")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if b.Label != "before the shoot" || b.Reason != "manual" {
		t.Errorf("returned = %+v", b)
	}
	if _, _, err := s.BackupNow(); err != nil { // an unlabelled one has none
		t.Fatal(err)
	}
	var labelled, plain int
	for _, x := range s.Backups() {
		if x.Label == "before the shoot" {
			labelled++
		} else if x.Reason == "manual" && x.Label == "" {
			plain++
		}
	}
	if labelled != 1 || plain != 1 {
		t.Errorf("listed: %+v", s.Backups())
	}
	if z := string(mustRead(t, filepath.Join(dir, ".backups", b.Name))); !strings.Contains(z, "manifest.json") {
		t.Error("not a bundle")
	}
	if CleanLabel(strings.Repeat("x", 200)) != strings.Repeat("x", 80) || CleanLabel("\x01\x02 ") != "" {
		t.Error("CleanLabel")
	}
	// a labelled backup is still a normal backup: restorable
	if _, err := s.RestoreBackup(b.Name); err != nil {
		t.Error(err)
	}
}

func TestDiffBackup_PerPresetCountsVersusNow(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	for i, c := range []string{"C1", "C2", "C3", "C4"} {
		mustAssign(t, s, chrome, "base", c, keyA("Cmd+"+string(rune('A'+i))))
	}
	mustAdd(t, s, "com.apple.Safari")
	mustAssign(t, s, "com.apple.Safari", "base", "C1", keyA("Cmd+L"))
	b, ok, err := s.BackupNowLabelled("four controls")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	// now: Chrome loses C3, C4; C2 changes; C5 is new; Safari removed; Firefox added
	s.Unassign(chrome, "base", "C3")
	s.Unassign(chrome, "base", "C4")
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+Z"))
	mustAssign(t, s, chrome, "base", "C5", keyA("Cmd+X"))
	if err := s.RemoveApp("com.apple.Safari"); err != nil {
		t.Fatal(err)
	}
	mustAdd(t, s, "org.mozilla.firefox")
	before := s.Snapshot()
	d, err := s.DiffBackup(b.Name)
	if err != nil {
		t.Fatal(err)
	}
	if s.Snapshot() != before {
		t.Error("a diff changed the store")
	}
	by := map[string]BackupDiffFile{}
	for _, f := range d.Files {
		by[f.ID] = f
	}
	c := by[chrome]
	if c.Status != "changed" || c.Current != 3 || c.Backup != 4 || c.Added != 2 || c.Removed != 1 || c.Changed != 1 {
		t.Errorf("chrome = %+v", c)
	}
	if !strings.Contains(c.Summary, "3 assignments now and 4 in this backup") {
		t.Errorf("summary = %q", c.Summary)
	}
	if f := by["com.apple.Safari"]; f.Status != "new" || f.Backup != 1 {
		t.Errorf("safari = %+v", f)
	}
	if f := by["org.mozilla.firefox"]; f.Status != "removed" {
		t.Errorf("firefox = %+v", f)
	}
	if d.Label != "four controls" || d.Reason != "manual" {
		t.Errorf("label/reason = %q %q", d.Label, d.Reason)
	}
	if d.Added == 0 || d.Removed == 0 || d.Changed != 1 {
		t.Errorf("totals = %d %d %d", d.Added, d.Removed, d.Changed)
	}
	if _, err := s.DiffBackup("presets-19990101-000000-manual.zip"); !errors.Is(err, ErrNoBackup) {
		t.Errorf("unknown backup: %v", err)
	}
	if _, err := s.DiffBackup("../x"); !errors.Is(err, ErrNoBackup) {
		t.Errorf("bad name: %v", err)
	}
}

func TestCopyPlan_CountsWithoutWriting(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAdd(t, s, "com.apple.Safari")
	mustAssign(t, s, chrome, "base", "C1", keyA("Cmd+T"))
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+W"))
	mustAssign(t, s, "com.apple.Safari", "base", "C3", keyA("Cmd+L"))
	before := s.Snapshot()
	nHist, _ := s.History()
	res, err := s.CopyPlan("com.apple.Safari", chrome, "all", "", "", "replace", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Copied != 2 || res.Removed != 1 || len(res.Skipped) != 0 {
		t.Errorf("plan = %+v", res)
	}
	if s.Snapshot() != before {
		t.Error("the dry run changed the store")
	}
	if h, _ := s.History(); len(h) != len(nHist) {
		t.Error("the dry run took an undo step")
	}
	real, err := s.Copy("com.apple.Safari", chrome, "all", "", "", "replace", false)
	if err != nil || real.Copied != res.Copied || real.Removed != res.Removed {
		t.Errorf("real copy %+v differs from the plan %+v (%v)", real, res, err)
	}
	if _, err := s.CopyPlan("com.apple.Safari", "com.nope", "all", "", "", "merge", false); err == nil {
		t.Error("plan with a bad source succeeded")
	}
}

// ---- H10: template metadata and the Blender navigation set ---------------------------------

func templateByID(t *testing.T, id string) Template {
	t.Helper()
	for _, tp := range Templates() {
		if tp.ID == id {
			return tp
		}
	}
	t.Fatalf("no template %s", id)
	return Template{}
}

func TestBlenderTemplate_RequiresEmulateNumpadAndHasTheNavigationSet(t *testing.T) {
	tp := templateByID(t, "blender")
	if len(tp.Requires) != 1 || tp.Requires[0] != (Requirement{ID: "blender-emulate-numpad", Label: "Needs Emulate Numpad", Help: "Blender Preferences > Input > Emulate Numpad"}) {
		t.Fatalf("requires = %+v", tp.Requires)
	}
	s := deckStore(t)
	p, _, err := s.AddFromTemplate("blender")
	if err != nil {
		t.Fatal(err)
	}
	b := p.Banks["base"]
	want := map[string][2]string{"Contrast": {"6", "4"}, "Saturation": {"8", "2"}}
	for c, k := range want {
		a := b[c]
		if a == nil || a["type"] != "keyDelta" || a["keyCW"] != k[0] || a["keyCCW"] != k[1] {
			t.Errorf("%s = %v, want keyDelta %v", c, a, k)
		}
	}
	for c, k := range map[string]string{"P1btn": "1", "P2btn": "3", "P3btn": "7"} {
		if a := b[c]; a == nil || a["type"] != "key" || a["key"] != k {
			t.Errorf("%s = %v, want key %s", c, a, k)
		}
	}
	if a := b["D1"]; a["type"] != "scrollDelta" || a["flags"] != nil {
		t.Errorf("zoom = %v", a)
	}
	if b["D2"]["flags"] != "Ctrl" || b["Exposure"]["flags"] != "Shift" {
		t.Errorf("pan flags: D2=%v Exposure=%v", b["D2"]["flags"], b["Exposure"]["flags"])
	}
	for c, a := range b {
		if l, _ := a["label"].(string); strings.HasPrefix(l, "Navigate") {
			continue
		} else if c == "Contrast" || c == "Saturation" || c == "P1btn" {
			t.Errorf("%s label %q", c, l)
		}
	}
	for _, id := range []string{"google-chrome", "safari", "terminal", "iterm2", "adobe-photoshop", "google-chrome-comfyui", "design-preview"} {
		if r := templateByID(t, id).Requires; r == nil {
			t.Errorf("%s: requires must be an array, not null", id)
		}
	}
}

func TestDesignTemplate_PreviewWithPageAndZoomKeys(t *testing.T) {
	tp := templateByID(t, "design-preview")
	if tp.BundleID != "com.apple.Preview" || tp.Controls < 5 {
		t.Fatalf("template = %+v", tp)
	}
	s := deckStore(t)
	p, _, err := s.AddFromTemplate("design-preview") // every key spec is validated by AddFromTemplate
	if err != nil {
		t.Fatal(err)
	}
	b := p.Banks["base"]
	if b["D2"]["keyCW"] != "Cmd+=" || b["D2"]["keyCCW"] != "Cmd+-" || b["Highlights"]["keyCW"] != "Cmd+Down" {
		t.Errorf("keys: %v %v", b["D2"], b["Highlights"])
	}
}

// ---- wave K, critic pass 3, finding 1: the action alias cannot hide what runs ----------------

func importOf(t *testing.T, body string) []byte {
	return bundleOf(t, map[string][]byte{chrome + ".json": pjson(chrome, body)})
}

func TestRiskBypass_AliasOverATypeOrMethodIsRefusedByName(t *testing.T) {
	cases := map[string]struct{ body, field string }{
		"method-shell-under-layer-alias": {`{"base":{"C1":{"action":"generic.layer","method":"shell","cmd":"/usr/bin/open","args":["-a","Calculator"]}}}`, "method"},
		"method-key-cmdq-under-hud":      {`{"base":{"C1":{"action":"generic.toggleHud","method":"key","key":"Cmd+Q"}}}`, "method"},
		"method-key-delete-under-layer":  {`{"base":{"C1":{"action":"generic.layer","method":"key","key":"Cmd+Delete"}}}`, "method"},
		"type-shell-under-layer-alias":   {`{"base":{"C1":{"action":"generic.layer","type":"shell","cmd":"/usr/bin/open"}}}`, "type"},
		"type-and-method-disagree":       {`{"base":{"C1":{"type":"layer","method":"shell","cmd":"/usr/bin/open"}}}`, "method"},
		"key-alias-over-shell-type":      {`{"base":{"C1":{"action":"generic.key","type":"shell","cmd":"/usr/bin/open"}}}`, "type"},
	}
	for name, c := range cases {
		s, _ := emptyStore(t)
		_, err := s.ImportOpts(importOf(t, c.body), true, ImportOptions{})
		var ie *ImportError
		if !errors.As(err, &ie) || !strings.Contains(err.Error(), c.field) {
			t.Errorf("%s: dry run = %v, want an ImportError naming %q", name, err, c.field)
		}
		if _, err := s.ImportOpts(importOf(t, c.body), false, ImportOptions{ConfirmRisk: true}); err == nil {
			t.Errorf("%s: a confirmed real import installed it", name)
		}
		if _, ok := s.Snapshot().Preset(chrome); ok {
			t.Errorf("%s: installed", name)
		}
		// the editor's own write path refuses it too
		mustAdd(t, s, chrome)
		a := map[string]any{}
		_ = json.Unmarshal([]byte(c.body), &struct{}{})
		var b map[string]map[string]map[string]any
		json.Unmarshal([]byte(c.body), &b)
		for k, v := range b["base"]["C1"] {
			a[k] = v
		}
		if err := s.Assign(chrome, "base", "C1", a); err == nil {
			t.Errorf("%s: Assign accepted it", name)
		}
	}
}

func TestRiskBypass_ClassifiedByWhatTheRuntimeRuns(t *testing.T) {
	type want struct {
		confirm                     bool
		shell, destructive, unknown int
	}
	cases := map[string]struct {
		body string
		want want
	}{
		"other-alias-type-shell":   {`{"base":{"C1":{"action":"other","type":"shell","cmd":"/usr/bin/open","args":["-a","Calculator"]}}}`, want{true, 1, 0, 0}},
		"method-only-shell":        {`{"base":{"C1":{"method":"shell","cmd":"/usr/bin/open"}}}`, want{true, 1, 0, 0}},
		"shell-alias-agrees":       {`{"base":{"C1":{"action":"generic.shell","type":"shell","cmd":"/usr/bin/open"}}}`, want{true, 1, 0, 0}},
		"upper-case-type":          {`{"base":{"C1":{"type":"Shell","cmd":"/usr/bin/open"}}}`, want{true, 0, 0, 1}},
		"unknown-type-with-cmd":    {`{"base":{"C1":{"type":"runit","cmd":"/usr/bin/open"}}}`, want{true, 0, 0, 1}},
		"keyDelta-cmdq":            {`{"base":{"C1":{"type":"keyDelta","keyCW":"Cmd+Q","keyCCW":"Cmd+W"}}}`, want{true, 0, 1, 0}},
		"key-spaces":               {`{"base":{"C1":{"type":"key","key":"cmd + q"}}}`, want{true, 0, 1, 0}},
		"key-cmd-option-w":         {`{"base":{"C1":{"type":"key","key":"Cmd+Option+W"}}}`, want{true, 0, 1, 0}},
		"key-delete":               {`{"base":{"C1":{"type":"key","key":"Delete"}}}`, want{true, 0, 1, 0}},
		"plain-key":                {`{"base":{"C1":{"type":"key","key":"Cmd+T"}}}`, want{false, 0, 0, 0}},
		"layer":                    {`{"base":{"C1":{"type":"layer","layer":2}}}`, want{false, 0, 0, 0}},
		"cmd-field-on-a-safe-type": {`{"base":{"C1":{"type":"layer","layer":2,"cmd":"/usr/bin/open"}}}`, want{false, 0, 0, 0}}, // the runtime does not run it
	}
	for name, c := range cases {
		s, _ := emptyStore(t)
		rep, err := s.ImportOpts(importOf(t, c.body), true, ImportOptions{})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got := want{rep.NeedsConfirm, rep.Risk.Shell, rep.Risk.Destructive, rep.Risk.Unknown}
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
		// skipRisky drops exactly the risky ones and installs the rest
		rep, err = s.ImportOpts(importOf(t, c.body), false, ImportOptions{SkipRisky: true})
		if err != nil {
			t.Errorf("%s: skipRisky: %v", name, err)
			continue
		}
		p, _ := s.Snapshot().Preset(chrome)
		installed := p.Banks["base"]["C1"] != nil
		if c.want.confirm && (installed || len(rep.Dropped) != 1) {
			t.Errorf("%s: skipRisky installed=%v dropped=%d", name, installed, len(rep.Dropped))
		}
		if !c.want.confirm && (!installed || len(rep.Dropped) != 0) {
			t.Errorf("%s: skipRisky installed=%v dropped=%d", name, installed, len(rep.Dropped))
		}
	}
}

func TestRiskBypass_RuntimeAndClassifierAgreeForEveryAliasTypeMethodPermutation(t *testing.T) {
	s, _ := emptyStore(t)
	reg := s.Registry()
	aliases := []string{"", "generic.layer", "generic.toggleHud", "generic.key", "generic.shell", "other"}
	types := []string{"", "layer", "key", "shell", "Shell", "toggleHud"}
	methods := []string{"", "layer", "key", "shell"}
	for _, al := range aliases {
		for _, ty := range types {
			for _, me := range methods {
				raw := map[string]any{"cmd": "/usr/bin/open", "key": "Cmd+Q", "layer": 2}
				if al != "" {
					raw["action"] = al
				}
				if ty != "" {
					raw["type"] = ty
				}
				if me != "" {
					raw["method"] = me
				}
				runtime := ty
				if runtime == "" {
					runtime = me
				}
				risk := reg.RiskOfRaw(raw)
				switch runtime {
				case "shell":
					if risk != actions.RiskShell {
						t.Errorf("%v: runtime shell classified %s", raw, risk)
					}
				case "key":
					if risk != actions.RiskDestructive {
						t.Errorf("%v: runtime Cmd+Q key classified %s", raw, risk)
					}
				case "Shell":
					if risk != actions.RiskUnknown {
						t.Errorf("%v: unknown runtime type classified %s", raw, risk)
					}
				case "layer", "toggleHud":
					if risk != actions.RiskSafe {
						t.Errorf("%v: runtime %s classified %s", raw, runtime, risk)
					}
				}
				// whatever Validate lets through, the classification above holds; a story that disagrees is refused
				if err := reg.Validate(raw, false); err == nil && al != "" && al != "other" && runtime != "" {
					a, _ := reg.Get(al)
					if !a.Matches(runtime) {
						t.Errorf("%v: accepted although the alias (%s) is not the runtime type (%s)", raw, a.Type, runtime)
					}
				}
			}
		}
	}
}

// ---- wave K, finding 3: holdMs everywhere, and the engine's own typed decode ------------------

func TestHoldMs_ValidatedForEveryActionType(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	for _, typ := range []map[string]any{{"type": "toggleHud"}, {"type": "layer", "layer": 2}, {"type": "key", "key": "Cmd+T"}, {"type": "none"}, {"type": "keyDelta", "keyCW": "Right", "keyCCW": "Left"}} {
		for _, bad := range []any{"abc", 2001, -1, 1.5, true, []any{1}} {
			a := map[string]any{"holdMs": bad}
			for k, v := range typ {
				a[k] = v
			}
			err := s.Assign(chrome, "base", "C1", a)
			var fe *actions.FieldError
			if !errors.As(err, &fe) || fe.Field != "holdMs" {
				t.Errorf("%v holdMs=%v: err = %v, want a holdMs field error", typ["type"], bad, err)
			}
		}
		ok := map[string]any{"holdMs": 500}
		for k, v := range typ {
			ok[k] = v
		}
		if err := s.Assign(chrome, "base", "C1", ok); err != nil {
			t.Errorf("%v holdMs=500: %v", typ["type"], err)
		}
	}
	// imports refuse it too
	if _, err := s.ImportOpts(importOf(t, `{"base":{"C1":{"type":"toggleHud","holdMs":"abc"}}}`), true, ImportOptions{}); err == nil || !strings.Contains(err.Error(), "holdMs") {
		t.Errorf("import: %v", err)
	}
}

func TestComposeCheck_RunsTheEnginesTypedDecode(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", keyA("Cmd+T"))
	// a file edited from outside, with a value the registry never sees but the engine's load rejects
	bad := pjson("com.example.Bad", `{"base":{"C1":{"type":"toggleHud","holdMs":"abc"}}}`)
	os.WriteFile(filepath.Join(dir, "com.example.Bad.json"), bad, 0o644)
	if changed, _ := s.CheckExternal(); changed {
		t.Fatal("a file the engine could not load was admitted")
	}
	if msg := s.Problems()["com.example.Bad.json"]; msg == "" {
		t.Errorf("not reported: %v", s.Problems())
	}
	// and the compose of what IS in the store still loads
	if err := composeCheck(s.Snapshot().presets); err != nil {
		t.Error(err)
	}
	// a hand-built bad state is refused by composeCheck itself
	ps := map[string]*Preset{}
	for id, p := range s.Snapshot().presets {
		ps[id] = p.Clone()
	}
	ps[chrome].Banks["base"]["C1"] = Assignment{"type": "toggleHud", "holdMs": "abc"}
	if err := composeCheck(ps); err == nil || !strings.Contains(err.Error(), "engine could not read") {
		t.Errorf("composeCheck = %v", err)
	}
}

func TestCleanLabel_StripsBidiAndFormatCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"before\u202Egnp.exe":          "beforegnp.exe",
		"a\u200Bb\u200Dc\uFEFFd":       "abcd",
		"x\u2066y\u2069z\u2028w\u2029": "xyzw",
		"  plain label  ":              "plain label",
		"Ünïcode ok – fine":            "Ünïcode ok – fine",
		"\u202A\u202B\u202C\u202D":     "",
	} {
		if got := CleanLabel(in); got != want {
			t.Errorf("CleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- wave K, finding 4: start-disarmed hardening -------------------------------------------

func TestStartDisarmed_FailedPreUpdateBackupStillStartsDisarmed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 2)
	s.MarkFirstRunDone()
	s.Close()
	// the backups folder cannot be made: a regular file sits where it should be
	os.RemoveAll(filepath.Join(dir, ".backups"))
	os.WriteFile(filepath.Join(dir, ".backups"), []byte("not a folder"), 0o644)
	s2 := canaryStore(t, dir, "b2", nil)
	if n := len(preUpdates(s2)); n != 0 {
		t.Fatalf("setup: the backup should have failed (%d made)", n)
	}
	if d, why := s2.StartDisarmed(noEnv); !d || why != StartReasonPreUpdate {
		t.Errorf("failed backup start = %v %q, want disarmed (pre-update)", d, why)
	}
}

func TestStartDisarmed_ReadOnlySecondInstance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	holder, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, Lock: true})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	holder.MarkFirstRunDone()
	second, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, Lock: true})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if ro, _ := second.ReadOnly(); !ro {
		t.Skip("lock not contended here")
	}
	if d, why := second.StartDisarmed(noEnv); !d || why != StartReasonReadOnly {
		t.Errorf("second instance = %v %q", d, why)
	}
	if d, _ := holder.StartDisarmed(noEnv); d {
		t.Error("the holder (marker present) is disarmed")
	}
}

func TestStartDisarmed_DisarmedMarkerPersistsAcrossBuilds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	s.MarkFirstRunDone()
	if d, _ := s.StartDisarmed(noEnv); d {
		t.Fatal("setup")
	}
	if err := s.MarkDisarmed(); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDisarmed(); err != nil { // idempotent
		t.Fatal(err)
	}
	s.Close()
	for _, build := range []string{"b1", "b2", "b3"} { // any build
		s2 := canaryStore(t, dir, build, nil)
		if d, why := s2.StartDisarmed(noEnv); !d || why != StartReasonDisarmedLast {
			t.Errorf("build %s: %v %q", build, d, why)
		}
		s2.Close()
	}
	s3 := canaryStore(t, dir, "b3", nil)
	if err := s3.ClearDisarmed(); err != nil {
		t.Fatal(err)
	}
	if err := s3.ClearDisarmed(); err != nil { // nothing to clear is fine
		t.Fatal(err)
	}
	s3.Close()
	s4 := canaryStore(t, dir, "b3", nil)
	if d, why := s4.StartDisarmed(noEnv); d {
		t.Errorf("after arming: still disarmed (%q)", why)
	}
	// env still wins over everything and the order is documented by the reasons
	env := func(k string) string { return map[bool]string{true: "1"}[k == EnvStartDisarmed] }
	s4.MarkDisarmed()
	if _, why := s4.StartDisarmed(env); why != StartReasonEnv {
		t.Errorf("env + marker = %q", why)
	}
	// the marker is never a preset, an export or a backup
	names, _ := presetFiles(dir)
	for _, n := range names {
		if strings.Contains(n, "disarmed") {
			t.Error("marker listed as a preset")
		}
	}
	data, _, _ := s4.Export("")
	if strings.Contains(string(data), "disarmed") {
		t.Error("marker in export")
	}
}

func TestWarnings_PanicKeyOverridesOtherAssignmentsOnItsControl(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAdd(t, s, "com.apple.Safari")
	mustAssign(t, s, GlobalID, "base", "C8", map[string]any{"type": "disarm"})
	mustAssign(t, s, chrome, "base", "C8", keyA("Cmd+T"))
	mustAssign(t, s, chrome, "shift", "C8", keyA("Cmd+Y"))
	mustAssign(t, s, "com.apple.Safari", "base", "C9", keyA("Cmd+L")) // another control: untouched
	var got []Warning
	for _, w := range s.Warnings() {
		if w.Kind == "panic-overrides" {
			got = append(got, w)
		}
	}
	if len(got) != 2 {
		t.Fatalf("warnings = %+v", got)
	}
	for _, w := range got {
		if w.Severity != "warn" || w.Control != "C8" || w.App != chrome || !strings.Contains(w.Message, "panic disarm key") {
			t.Errorf("warning = %+v", w)
		}
	}
	// no panic assignment, no such warnings
	s2, _ := emptyStore(t)
	mustAdd(t, s2, chrome)
	mustAssign(t, s2, chrome, "base", "C8", keyA("Cmd+T"))
	for _, w := range s2.Warnings() {
		if w.Kind == "panic-overrides" {
			t.Errorf("unexpected %+v", w)
		}
	}
	a, _ := s.Registry().Get("generic.disarm")
	if !strings.Contains(a.Help, "overrides anything else") || !strings.Contains(a.Help, "every app") {
		t.Errorf("help = %q", a.Help)
	}
}
