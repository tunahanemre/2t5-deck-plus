package preset

import (
	"bytes"
	"testing"

	"2t5-deck-plus/internal/config"
)

func TestMarshalRoundTripIsByteStable(t *testing.T) {
	in := `{"schema":1,"id":"com.x.y","name":"X","packs":["generic"],
	"banks":{"base":{"D1":{"type":"scrollDelta","linesPerDetent":3,"sens":80.0,"weird":{"a":[1,2,"<&>"]},"label":"S"}}},
	"settings":{"hud":{"edge":"bottom"},"pxPerUnit":10.0},"futureKey":{"z":1,"a":2}}`
	p, err := ParsePreset([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	b1, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	p2, err := ParsePreset(b1)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := p2.Marshal()
	if !bytes.Equal(b1, b2) {
		t.Fatalf("not stable:\n%s\n---\n%s", b1, b2)
	}
	for _, want := range []string{`"sens": 80.0`, `"pxPerUnit": 10.0`, `"<&>"`, `"weird"`, `"futureKey"`} {
		if !bytes.Contains(b1, []byte(want)) {
			t.Errorf("lost %s in\n%s", want, b1)
		}
	}
	if !bytes.HasSuffix(b1, []byte("}\n")) {
		t.Error("no trailing newline")
	}
}

func TestParseRejects(t *testing.T) {
	for name, in := range map[string]string{
		"no id":       `{"schema":1}`,
		"bad id":      `{"id":"../x"}`,
		"banks list":  `{"id":"a","banks":[]}`,
		"assign list": `{"id":"a","banks":{"base":{"D1":[]}}}`,
		"packs num":   `{"id":"a","packs":[1]}`,
		"not json":    `{`,
	} {
		if _, err := ParsePreset([]byte(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestRuntimeBankNames(t *testing.T) {
	cases := []struct{ id, bank, want string }{
		{ResolveID, "col-fn", "col-fn"},
		{ResolveID, "edit-shift", "edit-shift"},
		{GlobalID, "base", "default"},
		{GlobalID, "layer-2", "default-2"},
		{GlobalID, "shift", "default-shift"},
		{"com.google.Chrome", "base", "com.google.Chrome"},
		{"com.google.Chrome", "layer-3", "com.google.Chrome-3"},
		{"com.google.Chrome", "cmd", "com.google.Chrome-cmd"},
	}
	for _, c := range cases {
		if got := RuntimeBank(c.id, c.bank); got != c.want {
			t.Errorf("RuntimeBank(%s,%s)=%s want %s", c.id, c.bank, got, c.want)
		}
	}
	for n := 0; n <= 3; n++ {
		if layerBank("x", n) != config.LayerBank("x", n) {
			t.Errorf("layerBank drifted from config.LayerBank at %d", n)
		}
	}
}

func TestSplitAppBankInvertsRuntimeBank(t *testing.T) {
	for _, bank := range []string{"base", "shift", "cmd", "alt", "ctrl", "fn", "layer-2", "layer-3"} {
		rb := RuntimeBank("com.a.b", bank)
		app, got := splitAppBank(rb)
		if app != "com.a.b" || got != bank {
			t.Errorf("%s -> %s,%s", rb, app, got)
		}
	}
}

func TestValidBank(t *testing.T) {
	for _, c := range []struct {
		id, bank string
		ok       bool
	}{
		{"com.a.b", "base", true}, {"com.a.b", "shift", true}, {"com.a.b", "layer-2", true}, {"com.a.b", "layer-1", false},
		{"com.a.b", "layer-4", false}, {"com.a.b", "col", false}, {ResolveID, "col-fn", true}, {ResolveID, "../x", false},
		{GlobalID, "base", true},
	} {
		if ValidBank(c.id, c.bank) != c.ok {
			t.Errorf("ValidBank(%s,%s) != %v", c.id, c.bank, c.ok)
		}
	}
}
