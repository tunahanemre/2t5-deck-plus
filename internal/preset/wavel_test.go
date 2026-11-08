package preset

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/config"
)

// Wave L (critic pass 4): the editor must judge an assignment the way the engine
// reads it. encoding/json matches field names case-insensitively, so a raw map
// with "Type" / "CMD" / "Key" ran while the classifier saw nothing.

var critic4Banks = []struct{ id, bank string }{
	{chrome, "base"}, {chrome, "shift"}, {chrome, "layer-2"}, {ResolveID, "edit"},
}

func bodyFor(bank, assignment string) string { return fmt.Sprintf(`{%q:{"C1":%s}}`, bank, assignment) }

func bundleFor(t *testing.T, id, body string) []byte {
	return bundleOf(t, map[string][]byte{id + ".json": pjson(id, body)})
}

func TestCritic4_NonCanonicalSpellingsAreRefusedInEveryBankAndFlow(t *testing.T) {
	assignments := map[string]string{
		"Type-cap-shell":  `{"Type":"shell","Cmd":"/usr/bin/open","Args":["-a","Calculator"]}`,
		"Type-cap-key":    `{"Type":"key","Key":"Cmd+Q"}`,
		"KEY-upper":       `{"type":"key","KEY":"Cmd+Q"}`,
		"CMD-upper-shell": `{"type":"shell","CMD":"/bin/sh"}`,
		"METHOD-cap":      `{"Method":"shell","cmd":"/usr/bin/open"}`,
		"press-Key-cap":   `{"type":"cdl","press":{"type":"key","Key":"Cmd+Q"}}`,
		"press-Type-cap":  `{"type":"cdl","press":{"Type":"key","key":"Cmd+Q"}}`,
		"Press-cap":       `{"type":"cdl","Press":{"type":"key","key":"Cmd+Q"}}`,
		"keyDelta-KeyCW":  `{"type":"keyDelta","KeyCW":"Cmd+Q","keyCCW":"Cmd+W"}`,
		"kelvin-key":      "{\"type\":\"key\",\"Key\":\"Cmd+Q\"}",
		"long-s-flags":    "{\"type\":\"scrollDelta\",\"Flagſ\":\"Cmd\"}",
		"Disarm-Type":     `{"Type":"disarm"}`,
	}
	for name, a := range assignments {
		for _, cb := range critic4Banks {
			body := bodyFor(cb.bank, a)
			s, dir := emptyStore(t)
			data := bundleFor(t, cb.id, body)
			label := name + "/" + cb.bank
			// import (dry run and real, confirmed, and skipRisky)
			for _, o := range []struct {
				dry bool
				opt ImportOptions
			}{{true, ImportOptions{}}, {false, ImportOptions{ConfirmRisk: true}}, {false, ImportOptions{SkipRisky: true}}} {
				_, err := s.ImportOpts(data, o.dry, o.opt)
				var ie *ImportError
				if !errors.As(err, &ie) || !strings.Contains(err.Error(), "canonical") {
					t.Errorf("%s: import (dry=%v %+v) = %v, want a refusal naming the canonical spelling", label, o.dry, o.opt, err)
				}
			}
			if p, ok := s.Snapshot().Preset(cb.id); ok && p.Assigned() != 0 {
				t.Errorf("%s: installed %v", label, p.Banks)
			}
			// an outside edit of the file: refused as a problem, not loaded
			if cb.id == chrome {
				os.WriteFile(filepath.Join(dir, chrome+".json"), pjson(chrome, body), 0o644)
				if changed, _ := s.CheckExternal(); changed {
					t.Errorf("%s: an outside file with the spelling was loaded", label)
				}
				if msg := s.Problems()[chrome+".json"]; !strings.Contains(msg, "canonical") {
					t.Errorf("%s: problem = %q", label, msg)
				}
			}
			// the editor's write path
			mustAdd2(t, s, cb.id)
			var one map[string]any
			json.Unmarshal([]byte(a), &one)
			if cb.id == chrome && cb.bank != "layer-2" || cb.id == ResolveID {
				if err := s.Assign(cb.id, cb.bank, "C1", one); err == nil {
					t.Errorf("%s: Assign accepted it", label)
				}
			}
		}
	}
}

func mustAdd2(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, ok := s.Snapshot().Preset(id); !ok {
		if _, err := s.AddApp(id, "", nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCritic4_RestoreOfABackupHoldingTheSpellingNeverInstallsIt(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+T"))
	files := map[string][]byte{chrome + ".json": pjson(chrome, bodyFor("base", `{"Type":"shell","Cmd":"/usr/bin/open"}`))}
	zb, _ := zipBundle(files, "all", "manual", time.Now())
	os.MkdirAll(filepath.Join(dir, ".backups"), 0o755)
	name := "presets-20260101-000002-manual.zip"
	os.WriteFile(filepath.Join(dir, ".backups", name), zb, 0o644)
	s.RestoreBackup(name)
	p, _ := s.Snapshot().Preset(chrome)
	for _, a := range p.Banks["base"] {
		if len(config.NonCanonicalKeys(a)) > 0 || actions.EffectiveType(a) == "shell" {
			t.Errorf("restored a non-canonical assignment: %v", a)
		}
	}
}

func TestCritic4_CanonicalDangerousShapesAreClassified(t *testing.T) {
	cases := map[string]struct {
		a    string
		risk string
	}{
		"press-key-cmdq-cdl": {`{"type":"cdl","press":{"type":"key","key":"Cmd+Q"}}`, "destructive"},
		"force-quit":         {`{"type":"key","key":"Cmd+Alt+Escape"}`, "destructive"},
		"force-quit-option":  {`{"type":"key","key":"Option+Cmd+Escape"}`, "destructive"},
		"ctrl-d":             {`{"type":"key","key":"Ctrl+D"}`, "destructive"},
		"ctrl-cmd-q":         {`{"type":"key","key":"Ctrl+Cmd+Q"}`, "destructive"},
		"cmd-alt-w-delta":    {`{"type":"keyDelta","keyCW":"Right","keyCCW":"Cmd+Option+W"}`, "destructive"},
		"unicode-type":       {"{\"type\":\"ѕhell\",\"cmd\":\"/usr/bin/open\"}", "unknown"},
		"ws-type":            {`{"type":" shell","cmd":"/usr/bin/open"}`, "unknown"},
		"nested-binding":     {`{"type":"toggleHud","binding":{"type":"shell","cmd":"/bin/sh"}}`, "safe"},
		"plain-escape":       {`{"type":"key","key":"Escape"}`, "keys"},
		"ctrl-c":             {`{"type":"key","key":"Ctrl+C"}`, "keys"},
	}
	for name, c := range cases {
		var raw map[string]any
		if err := json.Unmarshal([]byte(c.a), &raw); err != nil {
			t.Fatal(err)
		}
		if got := actions.Default().RiskOfRaw(raw); got != c.risk {
			t.Errorf("%s: risk %q, want %q", name, got, c.risk)
		}
	}
}

// Property: for random case / unicode-fold variants of every Binding field name
// the editor's verdict equals the engine's own decode. A variant is refused by
// Validate (non-canonical) AND, even if some path skipped that, classified by
// what the engine's decoder read.
func TestCritic4_EveryFoldedFieldNameAgreesWithTheEngineDecode(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	variants := func(name string) []string {
		out := []string{strings.ToUpper(name), strings.Title(name)}
		for i := 0; i < 6; i++ {
			rs := []rune(name)
			for j := range rs {
				if rng.Intn(2) == 0 {
					rs[j] = []rune(strings.ToUpper(string(rs[j])))[0]
				}
			}
			out = append(out, string(rs))
		}
		out = append(out, strings.ReplaceAll(name, "k", "K"), strings.ReplaceAll(strings.ReplaceAll(name, "s", "ſ"), "S", "ſ"))
		return out
	}
	reg := actions.Default()
	oracle := func(raw map[string]any) string { // the engine's decode, read without any of our helpers
		data, _ := json.Marshal(raw)
		var b config.Binding
		if json.Unmarshal(data, &b) != nil {
			return actions.RiskUnknown
		}
		typ := b.Type
		if typ == "" {
			typ = b.Method
		}
		switch typ {
		case "shell":
			return actions.RiskShell
		case "key":
			if actions.KeyIsDestructive(b.Key) {
				return actions.RiskDestructive
			}
			return actions.RiskKeys
		}
		return actions.RiskSafe
	}
	checked := 0
	for _, field := range config.BindingFieldNames() {
		if strings.Contains(field, ".") {
			continue
		}
		for _, v := range variants(field) {
			if v == field {
				continue
			}
			for _, base := range []map[string]any{
				{"type": "key", "key": "Cmd+Q"}, {"type": "shell", "cmd": "/usr/bin/open"}, {"type": "cdl"}, {"type": "toggleHud"},
			} {
				raw := map[string]any{}
				for k, val := range base {
					raw[k] = val
				}
				delete(raw, field) // the variant replaces the canonical key
				val := any("Cmd+Q")
				switch field {
				case "type", "method":
					val = "shell"
				case "holdMs", "layer", "sens":
					val = 3
				case "args", "gang":
					val = []any{"-a", "x"}
				case "press":
					val = map[string]any{"type": "key", "key": "Cmd+Q"}
				case "min", "max":
					val = 1
				}
				raw[v] = val
				checked++
				if len(config.NonCanonicalKeys(raw)) == 0 {
					t.Errorf("%q (variant of %q) not flagged non-canonical in %v", v, field, raw)
					continue
				}
				if err := reg.Validate(raw, false); err == nil {
					t.Errorf("Validate accepted the variant %q of %q: %v", v, field, raw)
				}
				if got, want := reg.RiskOfRaw(raw), oracle(raw); want != actions.RiskSafe && got != want && !(want == actions.RiskKeys && got == actions.RiskUnknown) {
					t.Errorf("risk verdict %q != engine decode verdict %q for %v", got, want, raw)
				}
			}
		}
	}
	if checked < 300 {
		t.Errorf("only %d variants checked", checked)
	}
}

func TestCritic4_TemplatesAndSeedsUseCanonicalSpellings(t *testing.T) {
	check := func(where string, p *Preset) {
		for bn, bm := range p.Banks {
			for cn, a := range bm {
				if nc := config.NonCanonicalKeys(a); len(nc) > 0 {
					t.Errorf("%s %s.%s.%s: %v", where, p.ID, bn, cn, nc[0])
				}
			}
		}
	}
	for _, tp := range Templates() {
		check("template "+tp.ID, tp.preset)
	}
	s, _ := seededStore(t)
	for _, p := range s.Snapshot().Presets() {
		check("seed", p)
	}
}

// ---- P2: an imported panic key is flagged -----------------------------------------------------

func TestImport_PanicKeyIsFlaggedAndNamesWhatItSilences(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C8", keyA("Cmd+T"))
	// Resolve's legacy edit bank also binds C8
	body := pjson(ResolveID, `{"edit":{"C8":{"type":"key","key":"Cmd+Q","label":"Quit"}}}`)
	if err := os.WriteFile(filepath.Join(s.Dir(), ResolveID+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	s.CheckExternal()
	for _, pe := range s.PendingExternal() { // an outside Resolve file with Cmd+Q is held back (wave O): approve it
		if err := s.ApproveExternal(pe.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	zipb := bundleOf(t, map[string][]byte{"_global.json": pjson(GlobalID, `{"base":{"C8":{"type":"disarm"}}}`)})
	rep, err := s.ImportOpts(zipb, true, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Risk.Panic != 1 || rep.Risk.PanicOverrides < 1 || !rep.NeedsConfirm {
		t.Fatalf("risk = %+v confirm=%v", rep.Risk, rep.NeedsConfirm)
	}
	joined := strings.Join(rep.Files[0].Overrides, ";")
	if !strings.Contains(joined, "C8") || !strings.Contains(joined, "edit") {
		t.Errorf("overrides = %v (must name the Resolve edit bank control)", rep.Files[0].Overrides)
	}
	// without confirmation a real import is refused; skipRisky drops the panic key
	if _, err := s.ImportOpts(zipb, false, ImportOptions{}); err == nil {
		t.Error("an overriding panic key was imported unconfirmed")
	}
	rep, err = s.ImportOpts(zipb, false, ImportOptions{SkipRisky: true})
	if err != nil || len(rep.Dropped) != 1 || rep.Dropped[0].Action != "generic.disarm" {
		t.Fatalf("skipRisky = %+v %v", rep.Dropped, err)
	}
	g, _ := s.Snapshot().Preset(GlobalID)
	if g.Banks["base"]["C8"] != nil {
		t.Error("skipRisky installed the panic key")
	}
	// a panic key on a control nothing else uses is counted but needs no confirmation
	z2 := bundleOf(t, map[string][]byte{"_global.json": pjson(GlobalID, `{"base":{"P8btn":{"type":"disarm"}}}`)})
	rep, err = s.ImportOpts(z2, true, ImportOptions{})
	if err != nil || rep.Risk.Panic != 1 || rep.Risk.PanicOverrides != 0 || rep.NeedsConfirm {
		t.Errorf("harmless panic key: %+v confirm=%v %v", rep.Risk, rep.NeedsConfirm, err)
	}
}
