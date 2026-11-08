package preset

import (
	"2t5-deck-plus/internal/config"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"2t5-deck-plus/internal/actions"
)

// Wave N (critic pass 5): imports are judged on the state they would produce.

func globalDefaults(typ, fields string) string {
	return fmt.Sprintf(`{"actionDefaults":{%q:%s}}`, typ, fields)
}

func globalFile(t *testing.T, settings string) []byte {
	return []byte(fmt.Sprintf(`{"schema":1,"id":"_global","name":"Global","packs":["generic"],"banks":{},"settings":%s}`, settings))
}

func inheritedBundle(t *testing.T, defaultsType, defaults, appBody string) []byte {
	return bundleOf(t, map[string][]byte{
		"_global.json":   globalFile(t, globalDefaults(defaultsType, defaults)),
		chrome + ".json": pjson(chrome, appBody),
	})
}

func TestEffectiveRisk_InheritedDefaultsAreCountedAndNeedConfirmation(t *testing.T) {
	cases := map[string]struct {
		typ, defaults, body, risk string
		rawRisky                  bool // the type itself is already dangerous (shell): dropped as an assignment, no inheritance story
	}{
		"key-cmdq":    {"key", `{"key":"Cmd+Q"}`, `{"base":{"C1":{"type":"key"}}}`, "destructive", false},
		"keydelta":    {"keyDelta", `{"keyCW":"Cmd+Q","keyCCW":"Cmd+W"}`, `{"base":{"C1":{"type":"keyDelta"}}}`, "destructive", false},
		"shell-cmd":   {"shell", `{"cmd":"/usr/bin/open","args":["-a","Calculator"]}`, `{"base":{"C1":{"type":"shell"}}}`, "shell", true},
		"shift-bank":  {"key", `{"key":"Cmd+Delete"}`, `{"shift":{"C1":{"type":"key"}}}`, "destructive", false},
		"layer2-bank": {"key", `{"key":"Cmd+W"}`, `{"layer-2":{"C1":{"type":"key"}}}`, "destructive", false},
	}
	for name, c := range cases {
		s, _ := emptyStore(t)
		data := inheritedBundle(t, c.typ, c.defaults, c.body)
		rep, err := s.ImportOpts(data, true, ImportOptions{})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !rep.NeedsConfirm || (rep.Risk.Destructive+rep.Risk.Shell) != 1 {
			t.Errorf("%s: risk %+v confirm=%v (the inherited value runs)", name, rep.Risk, rep.NeedsConfirm)
		}
		if !c.rawRisky && (len(rep.Inherited) != 1 || rep.Inherited[0].Risk != c.risk || !strings.Contains(rep.Inherited[0].Reason, "inherits")) {
			t.Errorf("%s: inherited = %+v", name, rep.Inherited)
		}
		// a real unconfirmed import is refused and installs nothing
		if _, err := s.ImportOpts(data, false, ImportOptions{}); err == nil {
			t.Errorf("%s: applied without confirmation", name)
		}
		if _, ok := s.Snapshot().Preset(chrome); ok {
			t.Errorf("%s: installed", name)
		}
		// skipRisky: nothing dangerous remains in the effective state, and what was dropped is reported
		rep, err = s.ImportOpts(data, false, ImportOptions{SkipRisky: true})
		if err != nil || len(rep.Dropped) == 0 {
			t.Errorf("%s: skipRisky = %+v %v", name, rep.Dropped, err)
			continue
		}
		for k, e := range effectiveRisks(s.Snapshot().presets) {
			if dangerousRisk(e.risk) {
				t.Errorf("%s: after skipRisky %v is still %s (%v)", name, k, e.risk, e.raw)
			}
		}
		joined := fmt.Sprint(rep.Dropped)
		if !c.rawRisky && !strings.Contains(joined, "actionDefaults") {
			t.Errorf("%s: the defaults entry that created the risk was not reported: %s", name, joined)
		}
	}
}

func TestEffectiveRisk_InheritedFromTheGlobalAssignmentToo(t *testing.T) {
	s, _ := emptyStore(t)
	data := bundleOf(t, map[string][]byte{
		"_global.json":   pjson(GlobalID, `{"base":{"C1":{"type":"key","key":"Cmd+Q"}}}`),
		chrome + ".json": pjson(chrome, `{"base":{"C1":{"type":"key"}}}`),
	})
	rep, err := s.ImportOpts(data, true, ImportOptions{})
	if err != nil || !rep.NeedsConfirm || rep.Risk.Destructive != 2 { // Global's own Cmd+Q and the app's inherited one
		t.Fatalf("risk %+v confirm=%v %v", rep.Risk, rep.NeedsConfirm, err)
	}
}

func TestEffectiveRisk_DefaultsChangeThatMakesExistingAssignmentsDangerous(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	// an app preset that already exists with a bare key assignment (key unset: harmless today)
	if err := s.commit("seed", func(ps map[string]*Preset) error {
		ps[chrome].Banks["base"] = map[string]Assignment{"C1": {"type": "key"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data := bundleOf(t, map[string][]byte{"_global.json": globalFile(t, globalDefaults("key", `{"key":"Cmd+Q"}`))})
	rep, err := s.ImportOpts(data, true, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.NeedsConfirm || len(rep.Inherited) != 1 || rep.Inherited[0].Preset != chrome || rep.Risk.Destructive != 1 {
		t.Fatalf("a settings-only bundle that turns an existing assignment destructive: %+v inherited=%+v", rep.Risk, rep.Inherited)
	}
	rep, err = s.ImportOpts(data, false, ImportOptions{SkipRisky: true})
	if err != nil || !strings.Contains(fmt.Sprint(rep.Dropped), "actionDefaults.key") {
		t.Fatalf("skipRisky: %v %v", rep.Dropped, err)
	}
	g, _ := s.Snapshot().Preset(GlobalID)
	if ad, _ := g.Settings["actionDefaults"].(map[string]any); ad != nil && ad["key"] != nil {
		t.Errorf("the dangerous default was installed: %v", g.Settings)
	}
}

// Property: for random bundles the report's danger equals the effective runtime danger after apply.
func TestEffectiveRisk_ReportEqualsRuntimeRiskAfterApply(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	evaluated, risky := 0, 0
	keyPool := []string{"Cmd+Q", "Cmd+T", "Cmd+W", "Right", "Cmd+Delete", "Cmd+Z"}
	for trial := 0; trial < 40; trial++ {
		s, _ := emptyStore(t)
		var gsettings []string
		if rng.Intn(2) == 0 {
			gsettings = append(gsettings, fmt.Sprintf(`"key":{"key":%q}`, keyPool[rng.Intn(len(keyPool))]))
		}
		if rng.Intn(2) == 0 {
			gsettings = append(gsettings, fmt.Sprintf(`"keyDelta":{"keyCW":%q,"keyCCW":%q}`, keyPool[rng.Intn(len(keyPool))], keyPool[rng.Intn(len(keyPool))]))
		}
		if rng.Intn(3) == 0 {
			gsettings = append(gsettings, `"shell":{"cmd":"/usr/bin/open"}`)
		}
		var banks []string
		for _, c := range []string{"C1", "C2", "C3", "C4"} {
			switch rng.Intn(5) {
			case 0:
				banks = append(banks, fmt.Sprintf(`%q:{"type":"key"}`, c))
			case 1:
				banks = append(banks, fmt.Sprintf(`%q:{"type":"key","key":%q}`, c, keyPool[rng.Intn(len(keyPool))]))
			case 2:
				banks = append(banks, fmt.Sprintf(`%q:{"type":"keyDelta"}`, c))
			case 3:
				banks = append(banks, fmt.Sprintf(`%q:{"type":"shell"}`, c))
			}
		}
		data := bundleOf(t, map[string][]byte{
			"_global.json":   globalFile(t, `{"actionDefaults":{`+strings.Join(gsettings, ",")+`}}`),
			chrome + ".json": pjson(chrome, `{"base":{`+strings.Join(banks, ",")+`}}`),
		})
		rep, err := s.ImportOpts(data, true, ImportOptions{})
		if err != nil {
			continue // a bundle the strict validation refuses is not a risk question
		}
		// apply for real (confirmed), then measure what would run
		if _, err := s.ImportOpts(data, false, ImportOptions{ConfirmRisk: true}); err != nil {
			continue
		}
		runtime := ImportRisk{}
		for _, e := range effectiveRisks(s.Snapshot().presets) {
			switch e.risk {
			case actions.RiskShell:
				runtime.Shell++
			case actions.RiskDestructive:
				runtime.Destructive++
			case actions.RiskUnknown:
				runtime.Unknown++
			}
		}
		evaluated++
		got := ImportRisk{Shell: rep.Risk.Shell, Destructive: rep.Risk.Destructive, Unknown: rep.Risk.Unknown}
		if got != runtime {
			var b json.RawMessage = marshalAny(rep.Risk)
			t.Fatalf("trial %d: report %s != runtime %+v\nglobal=%v banks=%v", trial, b, runtime, gsettings, banks)
		}
		if runtime.Shell+runtime.Destructive+runtime.Unknown > 0 {
			risky++
		}
		if (runtime.Shell+runtime.Destructive+runtime.Unknown > 0) != rep.NeedsConfirm {
			t.Fatalf("trial %d: needsConfirm=%v with runtime %+v", trial, rep.NeedsConfirm, runtime)
		}
	}
	if evaluated < 20 || risky < 5 {
		t.Errorf("only %d bundles evaluated (%d risky): the property test is not exercising anything", evaluated, risky)
	}
}

// ---- P2: settings ---------------------------------------------------------------------------------

func resolveWithSettings(t *testing.T, settings string) []byte {
	body := fmt.Sprintf(`{"schema":1,"id":%q,"name":"DaVinci Resolve","packs":["resolve","generic"],"banks":{},"settings":%s}`, ResolveID, settings)
	return bundleOf(t, map[string][]byte{ResolveID + ".json": []byte(body)})
}

func TestSettingsImport_GuardWeakeningIsFlaggedConfirmedAndSkippable(t *testing.T) {
	cases := map[string]string{
		"threshold-negative": `{"finderTagConfirmThreshold":-1}`,
		"threshold-raised":   `{"finderTagConfirmThreshold":500}`,
		"dodee-off":          `{"dodeeMostGuard":false}`,
		"both":               `{"finderTagConfirmThreshold":-1,"dodeeMostGuard":false}`,
	}
	for name, settings := range cases {
		s, _ := seededStore(t)
		data := resolveWithSettings(t, settings)
		rep, err := s.ImportOpts(data, true, ImportOptions{})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		f := rep.Files[0]
		weak := 0
		for _, c := range f.SettingsChanged {
			if c.Weakens {
				weak++
				if c.Why == "" {
					t.Errorf("%s: no reason for %s", name, c.Key)
				}
			}
		}
		if weak == 0 || rep.Risk.Settings != weak || !rep.NeedsConfirm {
			t.Errorf("%s: settingsChanged=%+v risk=%+v confirm=%v", name, f.SettingsChanged, rep.Risk, rep.NeedsConfirm)
		}
		if _, err := s.ImportOpts(data, false, ImportOptions{}); err == nil {
			t.Errorf("%s: applied unconfirmed", name)
		}
		before, _ := s.Snapshot().Preset(ResolveID)
		oldVal := before.Settings["finderTagConfirmThreshold"]
		oldGuard := before.Settings["dodeeMostGuard"]
		rep, err = s.ImportOpts(data, false, ImportOptions{SkipRisky: true})
		if err != nil {
			t.Errorf("%s: skipRisky: %v", name, err)
			continue
		}
		after, _ := s.Snapshot().Preset(ResolveID)
		if fmt.Sprint(after.Settings["finderTagConfirmThreshold"]) != fmt.Sprint(oldVal) || fmt.Sprint(after.Settings["dodeeMostGuard"]) != fmt.Sprint(oldGuard) {
			t.Errorf("%s: skipRisky changed a guard: %v", name, after.Settings)
		}
		if !strings.Contains(fmt.Sprint(rep.Dropped), "settings") {
			t.Errorf("%s: dropped = %v", name, rep.Dropped)
		}
	}
}

func TestSettingsImport_HarmlessChangesAreListedNotFlagged(t *testing.T) {
	s, _ := seededStore(t)
	data := resolveWithSettings(t, `{"keyDebounceMs":80,"finderTagConfirmThreshold":3,"dodeeMostGuard":true}`)
	rep, err := s.ImportOpts(data, true, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f := rep.Files[0]
	keys := map[string]bool{}
	for _, c := range f.SettingsChanged {
		keys[c.Key] = true
		if c.Weakens {
			t.Errorf("%s flagged: a lower threshold asks MORE, and the guard stays on", c.Key)
		}
	}
	if !keys["keyDebounceMs"] || !keys["finderTagConfirmThreshold"] {
		t.Errorf("changes not listed: %+v", f.SettingsChanged)
	}
	if rep.Risk.Settings != 0 {
		t.Errorf("risk = %+v", rep.Risk)
	}
}

func TestGuardSettings_Table(t *testing.T) {
	type c struct {
		key     string
		old, nu map[string]any
		weaks   bool
	}
	num := func(n int) any { return json.Number(fmt.Sprint(n)) }
	for _, x := range []c{
		{"finderTagConfirmThreshold", nil, map[string]any{"finderTagConfirmThreshold": num(-1)}, true},
		{"finderTagConfirmThreshold", map[string]any{"finderTagConfirmThreshold": num(10)}, map[string]any{"finderTagConfirmThreshold": num(11)}, true},
		{"finderTagConfirmThreshold", nil, map[string]any{"finderTagConfirmThreshold": num(10)}, false},
		{"finderTagConfirmThreshold", map[string]any{"finderTagConfirmThreshold": num(-1)}, map[string]any{"finderTagConfirmThreshold": num(-1)}, false},
		{"finderTagConfirmThreshold", map[string]any{"finderTagConfirmThreshold": num(-1)}, map[string]any{"finderTagConfirmThreshold": num(5)}, false},
		{"finderTagConfirmThreshold", map[string]any{"finderTagConfirmThreshold": num(20)}, map[string]any{"finderTagConfirmThreshold": num(5)}, false},
		{"dodeeMostGuard", nil, map[string]any{"dodeeMostGuard": false}, true},
		{"dodeeMostGuard", map[string]any{"dodeeMostGuard": true}, map[string]any{"dodeeMostGuard": false}, true},
		{"dodeeMostGuard", map[string]any{"dodeeMostGuard": false}, map[string]any{"dodeeMostGuard": false}, false},
		{"dodeeMostGuard", map[string]any{"dodeeMostGuard": false}, map[string]any{"dodeeMostGuard": true}, false},
		// every spelling the engine's decoder reads
		{"dodeeMostGuard", nil, map[string]any{"DodeeMostGuard": false}, true},
		{"dodeeMostGuard", nil, map[string]any{"DODEEMOSTGUARD": false}, true},
		{"dodeeMostGuard", nil, map[string]any{"dodeeMo\u017ftGuard": false}, true},
		{"finderTagConfirmThreshold", nil, map[string]any{"FinderTagConfirmThreshold": num(-1)}, true},
		{"maxScrubPx", nil, map[string]any{"maxScrubPx": -5}, true},
		{"keyDebounceMs", nil, map[string]any{"keyDebounceMs": -5}, true},
		{"maxScrubPx", nil, map[string]any{"maxScrubPx": 80}, false},
	} {
		oldTL, _ := config.DecodeTopLevel(x.old)
		newTL, err := config.DecodeTopLevel(x.nu)
		if err != nil {
			t.Fatal(err)
		}
		var got bool
		for _, g := range guardSettings {
			if g.key == x.key {
				got = g.weakens(oldTL, newTL)
			}
		}
		if got != x.weaks {
			t.Errorf("%s %v -> %v: weakens=%v, want %v", x.key, x.old, x.nu, got, x.weaks)
		}
	}
}

// ---- wave O P1: key spelling cannot hide a guard, and absurd values are refused --------------------

func TestSettings_SpellingVariantsAreRefusedEverywhere(t *testing.T) {
	for _, st := range []string{
		`{"DodeeMostGuard":false}`, `{"DODEEMOSTGUARD":false}`, "{\"dodeeMo\u017ftGuard\":false}", `{"FinderTagConfirmThreshold":-1}`,
	} {
		s, _ := seededStore(t)
		data := resolveWithSettings(t, st)
		for _, o := range []struct {
			dry bool
			opt ImportOptions
		}{{true, ImportOptions{}}, {false, ImportOptions{ConfirmRisk: true}}, {false, ImportOptions{SkipRisky: true}}} {
			_, err := s.ImportOpts(data, o.dry, o.opt)
			var ie *ImportError
			if !errors.As(err, &ie) || !strings.Contains(err.Error(), "canonical") {
				t.Errorf("%s: import (dry=%v %+v) = %v", st, o.dry, o.opt, err)
			}
		}
		// the editor's write path and an outside file
		err := s.SetSetting(ResolveID, "DodeeMostGuard", false)
		var fe *actions.FieldError
		if !errors.As(err, &fe) {
			t.Errorf("SetSetting accepted a variant: %v", err)
		}
		pre, _ := s.Snapshot().Preset(ResolveID)
		body := pre.Clone()
		var m map[string]any
		json.Unmarshal([]byte(st), &m)
		for k, v := range m {
			body.Settings[k] = v
		}
		raw, _ := body.Marshal()
		os.WriteFile(filepath.Join(s.Dir(), ResolveID+".json"), raw, 0o644)
		s.CheckExternal()
		if p := s.Problems()[ResolveID+".json"]; !strings.Contains(p, "canonical") {
			t.Errorf("%s: outside file problem = %q", st, p)
		}
	}
}

func TestSettings_OutOfRangeValuesAreRefusedNamingTheField(t *testing.T) {
	bad := map[string]any{
		"maxScrubPx": -1, "pxPerUnit": 1e300, "keyDebounceMs": -5, "coalesceWindowMs": -1, "fineFactor": 50,
		"selectionGraceMs": 99999999, "markerResizeFramesPerStep": 100000, "minScrubPx": -3,
	}
	for key, v := range bad {
		s, _ := seededStore(t)
		err := s.SetSetting(ResolveID, key, v)
		var fe *actions.FieldError
		if !errors.As(err, &fe) || fe.Field != key {
			t.Errorf("SetSetting %s=%v: %v", key, v, err)
		}
		if p, _ := s.Snapshot().Preset(ResolveID); p.Settings[key] != nil && fmt.Sprint(p.Settings[key]) == fmt.Sprint(v) {
			t.Errorf("%s stored", key)
		}
		data := resolveWithSettings(t, fmt.Sprintf(`{%q:%v}`, key, v))
		if _, err := s.ImportOpts(data, true, ImportOptions{}); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("import %s=%v: %v", key, v, err)
		}
	}
	s, _ := seededStore(t)
	for key, v := range map[string]any{"maxScrubPx": 120, "keyDebounceMs": 80, "coalesceWindowMs": 150, "fineFactor": 0.3, "selectionGraceMs": 400} {
		if err := s.SetSetting(ResolveID, key, v); err != nil {
			t.Errorf("%s=%v refused: %v", key, v, err)
		}
	}
	// the compose gate refuses what the engine's range check would
	ps := map[string]*Preset{}
	for id, p := range s.Snapshot().presets {
		ps[id] = p.Clone()
	}
	ps[ResolveID].Settings["maxScrubPx"] = -10
	if err := composeCheck(ps); err == nil || !strings.Contains(err.Error(), "maxScrubPx") {
		t.Errorf("composeCheck = %v", err)
	}
}

// ---- wave N P3(d): seeding agrees with every later Open --------------------------------------------

func TestSeed_LegacyFieldSpellingInTheLiveFileIsReportedAndFallsBack(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live-bindings.json")
	os.WriteFile(live, []byte(`{"col":{"Lift":{"type":"wheel","wheel":"lift_master","Sens":15}}}`), 0o644)
	dir := filepath.Join(root, "presets")
	o := Options{SeedFrom: filepath.Join("testdata", "2t5-deck-plus-repo-bindings.json"), LiveResolvePath: live, PollInterval: -1}
	s, err := Open(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	var note string
	for _, w := range s.Warnings() {
		if w.Kind == "seed-fallback" {
			note += w.Message + " | "
		}
	}
	if !strings.Contains(note, `"Sens"`) || !strings.Contains(note, `"sens"`) || !strings.Contains(note, live) {
		t.Errorf("no clear fix hint: %q", note)
	}
	first := string(marshalOf(mustPreset(t, s, ResolveID)))
	if strings.Contains(first, `"Sens"`) {
		t.Error("the legacy spelling was seeded")
	}
	if len(s.Problems()) != 0 {
		t.Errorf("problems after the first open: %v", s.Problems())
	}
	s.Close()
	s2, err := Open(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if string(marshalOf(mustPreset(t, s2, ResolveID))) != first || len(s2.Problems()) != 0 {
		t.Errorf("the second Open disagrees with the first (problems %v)", s2.Problems())
	}
	// without a bundled default the Resolve preset is simply not created
	s3, err := Open(filepath.Join(root, "presets2"), Options{LiveResolvePath: live, PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	if _, ok := s3.Snapshot().Preset(ResolveID); ok {
		t.Error("a preset with a legacy spelling was seeded")
	}
}

func mustPreset(t *testing.T, s *Store, id string) *Preset {
	t.Helper()
	p, ok := s.Snapshot().Preset(id)
	if !ok {
		t.Fatalf("no preset %s", id)
	}
	return p
}
