package mousecal

import (
	"testing"
	"time"
)

var oldPts = map[string]LegacyPoint{
	"balance_red": {119.8, 854.8, "click"}, "gain_center": {592.5, 886.4, "click"},
	"gain_master": {657.4, 995.1, "h"}, "gain_reset": {683.3, 824.7, "click"},
	"lift_master": {181.2, 994.4, "h"}, "temp_slider": {223.9, 782.0, "h"},
}

var hd = []Display{
	{ID: "m", Name: "main", Frame: Rect{0, 0, 1920, 1080}, Scale: 1},
	{ID: "k", Name: "4K", Frame: Rect{1920, 0, 3840, 2160}, Scale: 1},
}

func TestMapLegacyKey(t *testing.T) {
	known := map[string]bool{"gain_master": true}
	for k, want := range map[string]bool{"gain_master": true, "lift_reset": true, "temp_slider": true, "tint_slider": true,
		"gain_center": false, "balance_red": false} {
		if _, ok := MapLegacyKey(k, known); ok != want {
			t.Errorf("%s: %v", k, ok)
		}
	}
}

func TestImportLegacyOnceAndHonest(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	b, ok, why := ImportLegacy(Block{V: 1, Targets: map[string]*TargetData{}}, oldPts, nil, hd, now)
	if !ok {
		t.Fatal(why)
	}
	if len(b.Import.Imported) != 4 || len(b.Import.Skipped) != 2 || b.Import.Display != "1920x1080@1" {
		t.Fatalf("%+v", b.Import)
	}
	e := b.Targets["gain_master"].Entries[0]
	if !e.Abs || e.AX != 657.4 || e.Layout != "color" || e.Source != "ld-resolve" || e.Axis != "h" {
		t.Fatalf("%+v", e)
	}
	// never twice, never over his own calibrations
	if _, ok, _ := ImportLegacy(b, oldPts, nil, hd, now); ok {
		t.Fatal("second import")
	}
	if _, ok, _ := ImportLegacy(Block{V: 1, Targets: map[string]*TargetData{"x": {}}}, oldPts, nil, hd, now); ok {
		t.Fatal("must not import over existing calibrations")
	}
	// no display holds the points: nothing is invented
	if _, ok, why := ImportLegacy(Block{V: 1, Targets: map[string]*TargetData{}}, oldPts, nil, []Display{{Frame: Rect{0, 0, 1512, 982}, Scale: 2}}, now); ok || why == "" {
		t.Fatal("laptop screen is too small for the points")
	}
}

func TestResolveImported(t *testing.T) {
	now := time.Now()
	b, _, _ := ImportLegacy(Block{V: 1, Targets: map[string]*TargetData{}}, oldPts, nil, hd, now)
	es := b.Entries("gain_master")
	ctx := Context{Display: "1920x1080@1", Layout: "color", Win: Rect{0, 25, 1920, 1055}}
	r := Resolve(es, ctx, Prior{})
	if r.Status != StOK || r.Confidence != ConfImported || r.Point != (Pt{657.4, 995.1}) || r.NeedsProbe {
		t.Fatalf("%+v", r)
	}
	// window moved so the spot is outside it: refused, never clicked blind
	ctx.Win = Rect{1000, 25, 920, 700}
	if r := Resolve(es, ctx, Prior{}); r.Status == StOK || r.Reason != ReasonImportedOff {
		t.Fatalf("%+v", r)
	}
	// another page or another screen class: refused like any calibration
	if r := Resolve(es, Context{Display: "1920x1080@1", Layout: "edit", Win: Rect{0, 0, 1920, 1080}}, Prior{}); r.Status == StOK {
		t.Fatal("other page")
	}
	if r := Resolve(es, Context{Display: "3840x2160@1", Layout: "color", Win: Rect{0, 0, 3840, 2160}}, Prior{}); r.Status == StOK {
		t.Fatal("other screen")
	}
	// a fresh window-relative capture takes over and replaces the imported point
	fresh := NewEntry(Context{Display: "1920x1080@1", Layout: "color", Win: Rect{0, 25, 1920, 1055}}, Pt{660, 990}, "cursor", "h", now)
	b = b.WithEntry("gain_master", fresh)
	if es := b.Entries("gain_master"); len(es) != 1 || es[0].Abs {
		t.Fatalf("%+v", es)
	}
}

// End to end through the service on the live Resolve preset: the migration runs
// from the store, the overview counts the points as imported, a second start does nothing.
func TestMigrateLegacyThroughService(t *testing.T) {
	r := newRig(t)
	r.fake.Displays = hd
	r.fake.With(rid, func(a *FakeApp) { a.Windows[0].Frame = Rect{0, 25, 1920, 1055} })
	before, _ := r.svc.Overview(rid)
	n, why := r.svc.MigrateLegacy(rid, oldPts)
	if n != 4 {
		t.Fatalf("%d %s", n, why)
	}
	after, _ := r.svc.Overview(rid)
	if after.Summary.Imported != 3 || after.Summary.Needs != before.Summary.Needs-3 {
		t.Fatalf("before %+v after %+v", before.Summary, after.Summary)
	}
	for _, tv := range after.Targets {
		if tv.Key == "gain_master" && (tv.Status != TImported || tv.Confidence != ConfImported) {
			t.Fatalf("%+v", tv)
		}
	}
	if n, why := r.svc.MigrateLegacy(rid, oldPts); n != 0 || why == "" {
		t.Fatalf("second start must do nothing: %d %q", n, why)
	}
	// Locate serves the point and no probe is claimed
	l := r.svc.Locate(rid, "gain_master", LocateOpts{})
	if !l.OK() || l.Confidence != ConfImported || l.Probe.Checked {
		t.Fatalf("%+v", l)
	}
	// "Refuse old-style points" refuses it
	_ = r.svc.SetOptions(rid, nil, ptr(true))
	r.tick(time.Second)
	if l := r.svc.Locate(rid, "gain_master", LocateOpts{}); l.OK() {
		t.Fatal("blockLegacy must refuse imported points")
	}
}
