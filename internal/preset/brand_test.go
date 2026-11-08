package preset

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every export and backup carries a plain-text credit in manifest.json. Importers ignore it,
// so bundles made before the credit existed (old kind or new) still import.

func TestExportCarriesMadeWithCredit(t *testing.T) {
	s, _, _ := chromeAndSafari(t)
	data, _, err := s.Export("all")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(zipEntry(t, data, "manifest.json"), &m); err != nil {
		t.Fatal(err)
	}
	if m["madeWith"] != MadeWith || !strings.Contains(MadeWith, "2T5 DECK+") || !strings.Contains(MadeWith, "2T5 Film") {
		t.Errorf("madeWith = %v, want %q", m["madeWith"], MadeWith)
	}
	if m["madeWithUrl"] != MadeWithURL || MadeWithURL != "https://2t5.co" {
		t.Errorf("madeWithUrl = %v", m["madeWithUrl"])
	}
	if m["kind"] != BundleKind {
		t.Errorf("kind changed: %v", m["kind"])
	}
}

func TestBundleWithCreditRoundTrips(t *testing.T) {
	s, _, _ := chromeAndSafari(t)
	data, _, err := s.Export("all")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ParseBundle(data)
	if err != nil {
		t.Fatal(err)
	}
	if b.Manifest.MadeWith != MadeWith || b.Manifest.MadeWithURL != MadeWithURL {
		t.Errorf("credit lost on parse: %+v", b.Manifest)
	}
	s2, _, _ := clockStore(t, 0)
	if _, err := s2.Import(data, false); err != nil {
		t.Fatalf("new bundle did not import: %v", err)
	}
	if _, ok := s2.Snapshot().Preset("com.google.Chrome"); !ok {
		t.Error("preset missing after import")
	}
	// re-export of the imported store still carries it (no doubling, no loss)
	again, _, err := s2.Export("all")
	if err != nil || !strings.Contains(string(zipEntry(t, again, "manifest.json")), `"madeWith": "`+MadeWith+`"`) {
		t.Errorf("re-export: %v", err)
	}
}

func TestOldBundlesWithoutCreditStillImport(t *testing.T) {
	for _, kind := range []string{BundleKind, LegacyBundleKind} {
		s, _, _ := clockStore(t, 0)
		man := `{"kind":"` + kind + `","schema":1,"created":"x","app":"all","files":[]}`
		if _, err := s.Import(mkZip(t, map[string]string{"manifest.json": man, "com.google.Chrome.json": presetChrome}), false); err != nil {
			t.Errorf("old bundle (%s) refused: %v", kind, err)
		}
	}
}

func TestUnknownManifestCreditFieldsAndReadmeAreIgnored(t *testing.T) {
	s, _, _ := clockStore(t, 0)
	man := `{"kind":"` + BundleKind + `","schema":1,"created":"x","app":"all","files":[],"madeWith":"anything","madeWithUrl":"https://2t5.co","future":{"a":1}}`
	zip := mkZip(t, map[string]string{"manifest.json": man, "com.google.Chrome.json": presetChrome, "README.txt": "Made with 2T5 DECK+\n"})
	if _, err := s.Import(zip, false); err != nil {
		t.Fatalf("bundle with credit fields and README.txt refused: %v", err)
	}
	if _, ok := s.Snapshot().Preset("com.google.Chrome"); !ok {
		t.Error("preset not imported")
	}
}
