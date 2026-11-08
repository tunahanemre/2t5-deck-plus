package actions

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeRaw(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFeelFieldsOnEveryVelocityTurnAction(t *testing.T) {
	want := map[string]bool{"scrollDelta": true, "keyDelta": true, "jog": true, "jogSeconds": true}
	n := 0
	for _, p := range Default().Packs() {
		for _, a := range p.Actions {
			if !want[a.Type] {
				continue
			}
			n++
			for _, k := range []string{"velocityProfile", "momentumMs"} {
				f, ok := a.FieldByKey(k)
				if !ok {
					t.Errorf("%s lacks %s", a.ID, k)
					continue
				}
				if f.Primary || f.Required || f.Seed || f.Help == "" {
					t.Errorf("%s.%s must be non-primary, optional, unseeded, with help", a.ID, k)
				}
			}
			if f, _ := a.FieldByKey("velocityProfile"); len(f.Options) != 4 || f.Default != "linear" {
				t.Errorf("%s velocityProfile options/default wrong: %+v", a.ID, f)
			}
		}
	}
	if n < 8 {
		t.Fatalf("expected the generic, resolve jog and browser turn actions, found %d", n)
	}
	// non-turn actions never carry them
	if a, _ := Default().Get("generic.key"); func() bool { _, ok := a.FieldByKey("momentumMs"); return ok }() {
		t.Error("press action must not carry momentumMs")
	}
}

func TestFeelValidation(t *testing.T) {
	r := Default()
	ok := []string{
		`{"type":"scrollDelta","velocityProfile":"precise","momentumMs":300}`,
		`{"type":"keyDelta","keyCW":"Right","keyCCW":"Left","velocityProfile":"linear","momentumMs":0}`,
		`{"type":"jog","velocityProfile":"fast"}`,
	}
	for _, s := range ok {
		if err := r.Validate(decodeRaw(t, s), true); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := map[string]string{
		`{"type":"scrollDelta","velocityProfile":"turbo"}`: "velocityProfile",
		`{"type":"scrollDelta","velocityProfile":3}`:       "velocityProfile",
		`{"type":"scrollDelta","momentumMs":-1}`:           "momentumMs",
		`{"type":"scrollDelta","momentumMs":2001}`:         "momentumMs",
		`{"type":"scrollDelta","momentumMs":1.5}`:          "momentumMs",
		`{"type":"scrollDelta","momentumMs":"slow"}`:       "momentumMs",
	}
	for s, field := range bad {
		err := r.Validate(decodeRaw(t, s), true)
		fe, isFE := err.(*FieldError)
		if !isFE || fe.Field != field {
			t.Errorf("%s: want field error on %s, got %v", s, field, err)
		}
	}
}

func TestNormalizeDoesNotSeedFeel(t *testing.T) {
	out, err := Default().Normalize(map[string]any{"action": "browser.scroll"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["velocityProfile"]; ok {
		t.Error("velocityProfile must stay absent (absent = today's behaviour)")
	}
	if _, ok := out["momentumMs"]; ok {
		t.Error("momentumMs must stay absent")
	}
}

func TestPressGroup(t *testing.T) {
	r := Default()
	for _, id := range []string{"resolve.wheel", "resolve.cdl"} {
		a, _ := r.Get(id)
		f, ok := a.FieldByKey("press")
		if !ok || f.Kind != "object" || len(f.Group) != 4 {
			t.Fatalf("%s press group missing: %+v", id, f)
		}
		if f.Group[0].Key != "type" || !f.Group[0].Required || len(f.Group[0].Options) != 5 {
			t.Errorf("press.type schema wrong: %+v", f.Group[0])
		}
	}
	cases := []struct {
		json  string
		field string // "" = valid
	}{
		{`{"type":"wheel","press":{"type":"none"}}`, ""},
		{`{"type":"wheel","press":{"type":"key","key":"Cmd+Z"}}`, ""},
		{`{"type":"wheel","press":{"type":"click","point":"lift_reset"}}`, ""},
		{`{"type":"cdl","press":{"type":"selectWheel","wheel":"gain"}}`, ""},
		{`{"type":"cdl","press":{"type":"resetActiveWheel"}}`, ""},
		{`{"type":"wheel","press":{"type":"key"}}`, "press.key"},
		{`{"type":"wheel","press":{"type":"key","key":"Cmd+NoSuch"}}`, "press.key"},
		{`{"type":"wheel","press":{"type":"click"}}`, "press.point"},
		{`{"type":"wheel","press":{"type":"selectWheel"}}`, "press.wheel"},
		{`{"type":"wheel","press":{"type":"selectWheel","wheel":"mid"}}`, "press.wheel"},
		{`{"type":"wheel","press":{"type":"zzz"}}`, "press.type"},
		{`{"type":"wheel","press":{}}`, "press.type"},
		{`{"type":"wheel","press":{"type":"click","point":4}}`, "press.point"},
		{`{"type":"wheel","press":"none"}`, "press"},
	}
	for _, c := range cases {
		err := r.Validate(decodeRaw(t, c.json), true)
		if c.field == "" {
			if err != nil {
				t.Errorf("%s: %v", c.json, err)
			}
			continue
		}
		fe, ok := err.(*FieldError)
		if !ok || fe.Field != c.field {
			t.Errorf("%s: want error on %s, got %v", c.json, c.field, err)
		}
	}
}

func TestLabsActionsAreMarkedAndNeverSeeded(t *testing.T) {
	r := Default()
	for _, id := range []string{"resolve.shuttle", "resolve.scrollTimeline"} {
		a, ok := r.Get(id)
		if !ok || !a.Labs || !a.Unimplemented {
			t.Fatalf("%s must exist with labs+unimplemented: %+v", id, a)
		}
		out, err := r.Normalize(map[string]any{"action": id})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range a.Fields {
			if _, set := out[f.Key]; set && f.Key != "label" {
				t.Errorf("%s.%s must not be seeded", id, f.Key)
			}
		}
	}
	// no other action is labs
	for _, p := range r.Packs() {
		for _, a := range p.Actions {
			if a.Labs && a.ID != "resolve.shuttle" && a.ID != "resolve.scrollTimeline" {
				t.Errorf("unexpected labs action %s", a.ID)
			}
		}
	}
	b, _ := json.Marshal(mustGet(t, r, "resolve.shuttle"))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["labs"] != true || m["unimplemented"] != true {
		t.Errorf("JSON must carry labs and unimplemented: %s", b)
	}
	// ordinary actions omit both keys (additive JSON)
	b, _ = json.Marshal(mustGet(t, r, "generic.key"))
	m = nil
	_ = json.Unmarshal(b, &m)
	if _, has := m["labs"]; has {
		t.Error("labs must be omitted when false")
	}
}

func mustGet(t *testing.T, r *Registry, id string) Action {
	a, ok := r.Get(id)
	if !ok {
		t.Fatal(id)
	}
	return a
}
