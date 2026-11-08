package actions

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every binding type the engine config knows (the Is* predicates in
// config.go) must map to a registry action, so nothing is unmappable.
func TestRegistryCoversEveryConfigType(t *testing.T) {
	src, err := os.ReadFile("../config/config.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?:effectiveType\(\)|\bt) == "([A-Za-z]+)"`)
	seen := map[string]bool{"cdl": true}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		seen[m[1]] = true
	}
	if len(seen) < 25 {
		t.Fatalf("regexp found only %d types: %v", len(seen), seen)
	}
	r := Default()
	for typ := range seen {
		if _, ok := r.ForType(typ); !ok {
			t.Errorf("config type %q has no registry action", typ)
		}
	}
	// plus the new generic types
	for _, typ := range []string{"shell", "none", "layer", "modifierHold", "toggleHud"} {
		if _, ok := r.ForType(typ); !ok {
			t.Errorf("missing %q", typ)
		}
	}
}

func TestRegistryShape(t *testing.T) {
	r := Default()
	ids := map[string]bool{}
	for _, p := range r.Packs() {
		for _, a := range p.Actions {
			if ids[a.ID] {
				t.Errorf("duplicate id %s", a.ID)
			}
			ids[a.ID] = true
			if !strings.HasPrefix(a.ID, p.ID+".") || a.Pack != p.ID {
				t.Errorf("%s: id/pack mismatch", a.ID)
			}
			if a.Kind != "press" && a.Kind != "turn" && a.Kind != "both" {
				t.Errorf("%s: bad kind %q", a.ID, a.Kind)
			}
			keys := map[string]bool{}
			for _, f := range a.Fields {
				if keys[f.Key] {
					t.Errorf("%s: duplicate field %s", a.ID, f.Key)
				}
				keys[f.Key] = true
				if f.Kind == "enum" && len(f.Options) == 0 {
					t.Errorf("%s.%s: enum without options", a.ID, f.Key)
				}
				if f.Default != nil && (f.Kind == "int" || f.Kind == "number") {
					if err := ValidateValue(f, f.Default); err != nil {
						t.Errorf("%s.%s default invalid: %v", a.ID, f.Key, err)
					}
				}
			}
		}
	}
}

// Browser actions are unit-free of Resolve vocabulary.
func TestBrowserPackNeverMentionsFramesOrMinutes(t *testing.T) {
	for _, p := range Default().Packs() {
		if p.ID != "browser" {
			continue
		}
		b, _ := json.Marshal(p)
		s := strings.ToLower(string(b))
		for _, bad := range []string{"frame", "minute", "second", "timecode", "playhead"} {
			if strings.Contains(s, bad) {
				t.Errorf("browser pack mentions %q", bad)
			}
		}
	}
}

func raw(t *testing.T, s string) map[string]any {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestValidate(t *testing.T) {
	r := Default()
	cases := []struct {
		name, in, wantField string
		strict              bool
	}{
		{"jog ok", `{"type":"jog","base":1,"gamma":1.6,"maxDelta":90}`, "", true},
		{"jog gamma too high", `{"type":"jog","gamma":9}`, "gamma", true},
		{"jog base text", `{"type":"jog","base":"x"}`, "base", true},
		{"seconds step ok", `{"action":"resolve.scrubSeconds","secondsPerStep":60}`, "", true},
		{"detent frac", `{"type":"jogMinuteSnap","detentThreshold":2.5}`, "detentThreshold", true},
		{"key bad", `{"type":"key","key":"Cmb+Z"}`, "key", true},
		{"key ok", `{"type":"key","key":"Cmd+Shift+Z"}`, "", true},
		{"key missing strict", `{"type":"key"}`, "key", true},
		{"key missing lenient", `{"type":"key"}`, "", false},
		{"keyDelta bad ccw", `{"type":"keyDelta","keyCW":"Cmd+=","keyCCW":"Cmd+Nope"}`, "keyCCW", true},
		{"scroll flags bad enum-free", `{"type":"scrollDelta","linesPerDetent":3}`, "", true},
		{"scroll lines out of range", `{"type":"scrollDelta","linesPerDetent":500}`, "linesPerDetent", true},
		{"browser scroll ok", `{"action":"browser.scroll","linesPerDetent":-3}`, "", true},
		{"unknown type strict", `{"type":"teleport"}`, "type", true},
		{"unknown type lenient", `{"type":"teleport"}`, "", false},
		{"other explicit", `{"type":"teleport","action":"other"}`, "", true},
		{"unknown action", `{"action":"nope.nope"}`, "action", true},
		{"type/action mismatch", `{"action":"browser.scroll","type":"key"}`, "type", true},
		{"shell needs cmd", `{"type":"shell"}`, "cmd", true},
		{"shell ok", `{"type":"shell","cmd":"/usr/bin/open","args":["-a","Safari"]}`, "", true},
		{"shell args bad", `{"type":"shell","cmd":"x","args":[1]}`, "args", true},
		{"layer 4", `{"type":"layer","layer":4}`, "layer", true},
		{"press object bad", `{"type":"wheel","press":{"type":"zzz"}}`, "press.type", true},
		{"finder empty tag ok", `{"type":"finderTag","tag":""}`, "", true},
		{"finder bad tag", `{"type":"finderTag","tag":"Pinkish"}`, "tag", true},
		{"cdl bad param", `{"type":"cdl","param":"gain","ch":"r"}`, "param", true},
		{"legacy method cdl", `{"method":"cdl","param":"slope","ch":"g"}`, "", true},
		{"alias colorRenders", `{"type":"colorRenders"}`, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := r.Validate(raw(t, c.in), c.strict)
			if c.wantField == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			fe, ok := err.(*FieldError)
			if !ok || fe.Field != c.wantField {
				t.Fatalf("want field %q, got %v", c.wantField, err)
			}
		})
	}
}

func TestNormalizeFillsTypeFixedSeed(t *testing.T) {
	r := Default()
	out, err := r.Normalize(raw(t, `{"action":"browser.scroll","label":"Scroll","gone":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if out["type"] != "scrollDelta" {
		t.Errorf("type %v", out["type"])
	}
	if out["linesPerDetent"] != 3.0 || out["detentThreshold"] != 1.0 {
		t.Errorf("seed: %v", out)
	}
	if _, ok := out["gone"]; ok {
		t.Error("null kept")
	}
	out, _ = r.Normalize(raw(t, `{"action":"browser.tabs"}`))
	if out["type"] != "keyDelta" || out["keyCW"] != "Cmd+Shift+]" {
		t.Errorf("fixed: %v", out)
	}
	if _, err := r.Normalize(raw(t, `{"action":"browser.tabs","type":"key"}`)); err == nil {
		t.Error("mismatch accepted")
	}
}

func TestInfer(t *testing.T) {
	r := Default()
	for _, c := range []struct{ in, want string }{
		{`{"type":"jog"}`, "resolve.scrubFrames"},
		{`{"type":"selectTarget"}`, "resolve.selectWheel"},
		{`{}`, "resolve.cdl"},
		{`{"type":"keyDelta"}`, "generic.keyDelta"},
		{`{"action":"browser.zoom","type":"keyDelta"}`, "browser.zoom"},
		{`{"type":"mystery"}`, OtherID},
	} {
		if got := r.Infer(raw(t, c.in)).ID; got != c.want {
			t.Errorf("%s -> %s want %s", c.in, got, c.want)
		}
	}
}

func TestKeySpec(t *testing.T) {
	for s, ok := range map[string]bool{"Cmd+Z": true, "Left": true, "Cmd+Shift+]": true, "Cmd+=": true, "": false, "Cmd+": false, "Hyper+A": false, "Alt": true, "A+B": false} {
		if (ValidateKeySpec(s) == nil) != ok {
			t.Errorf("%q ok=%v", s, ok)
		}
	}
}
