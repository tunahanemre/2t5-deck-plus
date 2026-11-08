package preset

import (
	"errors"
	"testing"
)

func tplByID(t *testing.T, id string) Template {
	t.Helper()
	for _, x := range Templates() {
		if x.ID == id {
			return x
		}
	}
	t.Fatalf("no template %q", id)
	return Template{}
}

// A template app restores the template's assignments (not an empty app), as
// one undo step, and reports where it came from.
func TestRestoreDefaultsTemplateApp(t *testing.T) {
	s, _ := emptyStore(t)
	p, tpl, err := s.AddFromTemplate("google-chrome")
	if err != nil {
		t.Fatal(err)
	}
	want := p.Assigned()
	if want == 0 {
		t.Fatal("template has no assignments")
	}
	if p.Extra[TemplateKey] != "google-chrome" {
		t.Fatalf("templateId not recorded: %v", p.Extra)
	}
	// wreck it: unassign everything and set a setting
	for bn, bm := range p.Banks {
		for cn := range bm {
			if err := s.Unassign(p.ID, bn, cn); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = s.SetSetting(p.ID, "zzz", 1)
	if cur, _ := s.Snapshot().Preset(p.ID); cur.Assigned() != 0 {
		t.Fatal("setup: not emptied")
	}
	_, undoBefore := s.History()
	info, err := s.RestoreDefaultsInfo(p.ID, "app", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != "template" || info.TemplateID != tpl.ID || info.TemplateName != tpl.Name || info.Controls != want {
		t.Errorf("info %+v want %d controls", info, want)
	}
	cur, _ := s.Snapshot().Preset(p.ID)
	if cur.Assigned() != want || cur.Settings["zzz"] != nil || cur.Extra[TemplateKey] != "google-chrome" || len(cur.Packs) == 0 {
		t.Errorf("not restored: assigned %d settings %v extra %v", cur.Assigned(), cur.Settings, cur.Extra)
	}
	if _, after := s.History(); after != undoBefore+1 {
		t.Errorf("restore was %d undo steps", after-undoBefore)
	}
	// control scope restores that control from the template too
	var bank, ctl string
	for bn, bm := range p.Banks {
		for cn := range bm {
			bank, ctl = bn, cn
		}
	}
	_ = s.Unassign(p.ID, bank, ctl)
	if info, err = s.RestoreDefaultsInfo(p.ID, "control", bank, ctl); err != nil || info.Source != "template" {
		t.Fatalf("control scope: %v %+v", err, info)
	}
	if cur, _ = s.Snapshot().Preset(p.ID); cur.Banks[bank][ctl] == nil {
		t.Error("control not restored from the template")
	}
	// undo returns to the pre-restore state in one step
	if _, _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
}

// Of two templates for one bundle, the recorded id decides which is restored.
func TestRestoreDefaultsRecordedTemplateWins(t *testing.T) {
	s, _ := emptyStore(t)
	p, _, err := s.AddFromTemplate("google-chrome-comfyui")
	if err != nil {
		t.Fatal(err)
	}
	want := tplByID(t, "google-chrome-comfyui").Controls
	if p.Assigned() != want {
		t.Fatalf("created with %d, want %d", p.Assigned(), want)
	}
	_ = s.Unassign(p.ID, tplFirstBank(p), tplFirstControl(p))
	info, err := s.RestoreDefaultsInfo(p.ID, "app", "", "")
	if err != nil || info.TemplateID != "google-chrome-comfyui" || info.Controls != want {
		t.Errorf("%v %+v want %d", err, info, want)
	}
}

func tplFirstBank(p *Preset) string {
	for bn := range p.Banks {
		return bn
	}
	return ""
}
func tplFirstControl(p *Preset) string {
	for _, bm := range p.Banks {
		for cn := range bm {
			return cn
		}
	}
	return ""
}

// A custom app has no shipped preset: app scope refuses with ErrNoShippedPreset
// and changes nothing (no undo step); control scope still just unassigns.
func TestRestoreDefaultsCustomAppRefuses(t *testing.T) {
	s, _ := emptyStore(t)
	if _, err := s.AddApp("com.example.Custom", "Custom", nil); err != nil {
		t.Fatal(err)
	}
	mustAssign(t, s, "com.example.Custom", "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	_, n := s.History()
	info, err := s.RestoreDefaultsInfo("com.example.Custom", "app", "", "")
	if !errors.Is(err, ErrNoShippedPreset) || info != nil {
		t.Fatalf("got %v %+v", err, info)
	}
	if cur, _ := s.Snapshot().Preset("com.example.Custom"); cur.Assigned() != 1 {
		t.Error("refused restore emptied the app")
	}
	if _, after := s.History(); after != n {
		t.Error("refused restore left an undo step")
	}
	info, err = s.RestoreDefaultsInfo("com.example.Custom", "control", "base", "C1")
	if err != nil || info.Source != "none" {
		t.Fatalf("control scope: %v %+v", err, info)
	}
	if cur, _ := s.Snapshot().Preset("com.example.Custom"); cur.Assigned() != 0 {
		t.Error("control not unassigned")
	}
}

// Resolve restores the seed and says so.
func TestRestoreDefaultsResolveInfo(t *testing.T) {
	s, _ := seededStore(t)
	orig, _ := s.Snapshot().Preset(ResolveID)
	want := orig.Assigned()
	_ = s.SetSetting(ResolveID, "zzz", 1)
	info, err := s.RestoreDefaultsInfo(ResolveID, "app", "", "")
	if err != nil || info.Source != "resolve-seed" || info.Controls != want {
		t.Errorf("%v %+v want %d", err, info, want)
	}
}

// An app added before the id was recorded still restores when only one
// template exists for its bundle id (Safari), and refuses when ambiguous (Chrome).
func TestRestoreDefaultsLegacyTemplateApp(t *testing.T) {
	s, _ := emptyStore(t)
	p, _, err := s.AddFromTemplate("safari")
	if err != nil {
		t.Fatal(err)
	}
	want := p.Assigned()
	if err := s.Unassign(p.ID, tplFirstBank(p), tplFirstControl(p)); err != nil {
		t.Fatal(err)
	}
	if err := s.commit("simulate older file", func(ps map[string]*Preset) error {
		delete(ps[p.ID].Extra, TemplateKey)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	info, err := s.RestoreDefaultsInfo(p.ID, "app", "", "")
	if err != nil || info.TemplateID != "safari" || info.Controls != want {
		t.Errorf("legacy safari: %v %+v want %d", err, info, want)
	}
}

func dropTemplateID(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.commit("simulate older file", func(ps map[string]*Preset) error {
		delete(ps[id].Extra, TemplateKey)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func lastLabel(s *Store) string {
	h, n := s.History()
	return h[n].Label
}

// Several templates share the bundle id and none is recorded: the template
// whose assignments overlap most wins, source says "template-guess", and the id
// is recorded so the next restore is exact.
func TestRestoreDefaultsTemplateGuessOverlap(t *testing.T) {
	for _, id := range []string{"google-chrome", "google-chrome-comfyui"} {
		s, _ := emptyStore(t)
		p, _, err := s.AddFromTemplate(id)
		if err != nil {
			t.Fatal(err)
		}
		want := tplByID(t, id)
		_ = s.SetSetting(p.ID, "zzz", 1)
		dropTemplateID(t, s, p.ID)
		info, err := s.RestoreDefaultsInfo(p.ID, "app", "", "")
		if err != nil || info.Source != "template-guess" || info.TemplateID != id || info.TemplateName != want.Name || info.Controls != want.Controls {
			t.Fatalf("%s: %v %+v", id, err, info)
		}
		cur, _ := s.Snapshot().Preset(p.ID)
		if cur.Extra[TemplateKey] != id || cur.Settings["zzz"] != nil {
			t.Errorf("%s: not recorded/restored: %v", id, cur.Extra)
		}
		// recorded now: the next restore is exact, not a guess
		if info, err = s.RestoreDefaultsInfo(p.ID, "app", "", ""); err != nil || info.Source != "template" || info.TemplateID != id {
			t.Errorf("%s second restore: %v %+v", id, err, info)
		}
	}
}

// With no overlap at all the first template in stable order is the primary.
func TestRestoreDefaultsTemplateGuessNoOverlapIsStable(t *testing.T) {
	s, _ := emptyStore(t)
	if _, err := s.AddApp("com.google.Chrome", "Google Chrome", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		info, err := s.RestoreDefaultsInfo("com.google.Chrome", "app", "", "")
		if err != nil || info.Source != "template-guess" || info.TemplateID != "google-chrome" {
			t.Fatalf("run %d: %v %+v", i, err, info)
		}
		dropTemplateID(t, s, "com.google.Chrome")
	}
}

// Control and bank scope never use a guess; they unassign as before.
func TestRestoreDefaultsGuessOnlyForApp(t *testing.T) {
	s, _ := emptyStore(t)
	if _, err := s.AddApp("com.google.Chrome", "Google Chrome", nil); err != nil {
		t.Fatal(err)
	}
	mustAssign(t, s, "com.google.Chrome", "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	info, err := s.RestoreDefaultsInfo("com.google.Chrome", "control", "base", "C1")
	if err != nil || info.Source != "none" {
		t.Fatalf("%v %+v", err, info)
	}
}

// The undo/history label names the app (and the control or bank).
func TestRestoreDefaultsLabels(t *testing.T) {
	s, _ := emptyStore(t)
	p, _, err := s.AddFromTemplate("google-chrome")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ scope, bank, ctl, want string }{
		{"app", "", "", "Restore Google Chrome defaults"},
		{"control", "base", "D1", "Restore D1 defaults in Google Chrome"},
		{"bank", "base", "", "Restore bank base defaults in Google Chrome"},
	} {
		_ = s.SetSetting(p.ID, "zzz", c.scope) // a real change, so the restore is an undo step
		_ = s.Unassign(p.ID, "base", "D1")
		if _, err := s.RestoreDefaultsInfo(p.ID, c.scope, c.bank, c.ctl); err != nil {
			t.Fatal(err)
		}
		if got := lastLabel(s); got != c.want {
			t.Errorf("%s: label %q want %q", c.scope, got, c.want)
		}
	}
}
