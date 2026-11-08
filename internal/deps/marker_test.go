package deps

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"2t5-deck-plus/docs"
)

func TestMarker_AbsentAndCorruptReadAsNil(t *testing.T) {
	dir := t.TempDir()
	if m, err := ReadMarker(dir); m != nil || err != nil {
		t.Errorf("absent: %v %v", m, err)
	}
	if err := os.WriteFile(filepath.Join(dir, MarkerName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m, _ := ReadMarker(dir); m != nil {
		t.Errorf("a corrupt marker must read as first run, got %+v", m)
	}
}

func TestMarker_RoundTripAtomicAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "2t5-deck-plus")
	in := Marker{Host: "mac", DoneAt: "2026-11-08T10:00:00Z", OK: []string{"accessibility"}, RequiredMissing: []string{"loupedeck"}, OptionalSkipped: []string{"python3"}}
	if err := WriteMarker(dir, in); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMarker(dir)
	if err != nil || got == nil || !reflect.DeepEqual(*got, in) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	fi, _ := os.Stat(filepath.Join(dir, MarkerName))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("temp file left behind: %v", ents)
	}
}

func TestMarkerDir_UsesTheHomeFolderNeverTheRealOneInTests(t *testing.T) {
	orig := userHomeDir
	userHomeDir = func() (string, error) { return "/tmp/fake-home", nil }
	t.Cleanup(func() { userHomeDir = orig })
	d, err := MarkerDir()
	if err != nil || d != "/tmp/fake-home/Library/Application Support/2t5-deck-plus" {
		t.Errorf("%q %v", d, err)
	}
	userHomeDir = func() (string, error) { return "", os.ErrNotExist }
	if _, err := MarkerDir(); err == nil {
		t.Error("no home must be an error")
	}
}

func TestReconcile(t *testing.T) {
	cat := []Entry{{ID: "a", Required: true}, {ID: "b", Required: true}, {ID: "c", Required: false}, {ID: "d", Required: true, Informational: true}}
	res := map[string]string{"a": OK, "b": Missing, "c": OK}
	// no marker: first run, nothing "came back"
	r := Reconcile(cat, res, nil)
	if !r.FirstRun || len(r.CameBack) != 0 || r.Changed {
		t.Errorf("first run: %+v", r)
	}
	// marker says a and b were ok; now b is missing -> it came back; a stays; c (optional) is never tracked
	m := &Marker{OK: []string{"a", "b"}}
	r = Reconcile(cat, res, m)
	if r.FirstRun || !reflect.DeepEqual(r.CameBack, []string{"b"}) || r.Changed {
		t.Errorf("came back: %+v", r)
	}
	if !r.WasOK["a"] || !r.WasOK["b"] || r.WasOK["c"] {
		t.Errorf("wasOK = %v", r.WasOK)
	}
	// a required item that is ok now but unknown to the marker is added (so a later loss counts)
	m = &Marker{OK: []string{"b"}}
	r = Reconcile(cat, map[string]string{"a": OK, "b": OK, "c": OK}, m)
	sort.Strings(r.NewOK)
	if !r.Changed || !reflect.DeepEqual(r.NewOK, []string{"a", "b"}) {
		t.Errorf("add: %+v", r)
	}
	// unknown never counts as missing or as ok
	r = Reconcile(cat, map[string]string{"a": Unknown, "b": Unknown}, &Marker{OK: []string{"a"}})
	if len(r.CameBack) != 0 || r.Changed {
		t.Errorf("unknown: %+v", r)
	}
}

func TestFeaturesInTheCatalogAreKnown(t *testing.T) {
	cat, err := LoadCatalog(docs.DependenciesJSON)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{"resolve": true, "finder-tags": true, "calibration": true}
	seen := map[string]bool{}
	for _, e := range cat {
		for _, f := range e.Features {
			if !known[f] {
				t.Errorf("%s: unknown feature tag %q", e.ID, f)
			}
			seen[f] = true
		}
	}
	for f := range known {
		if !seen[f] {
			t.Errorf("no dependency carries the %q tag", f)
		}
	}
}
