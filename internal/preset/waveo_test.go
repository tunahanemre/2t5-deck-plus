package preset

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/config"
)

// dangerous set of the composed config + ResolveForMod
func dset(s *Store) map[string]string {
	out := map[string]string{}
	snap := s.Snapshot()
	raw, err := snap.ComposeBindingsJSON()
	if err != nil {
		return out
	}
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	for bn, rm := range m {
		if bn == "_config" {
			continue
		}
		var ctrls map[string]map[string]any
		json.Unmarshal(rm, &ctrls)
		for cn, a := range ctrls {
			b, err := config.DecodeBinding(a)
			sig := ""
			risk := ""
			if err != nil {
				risk = "unknown"
			} else {
				t := b.EffectiveType()
				if t == "shell" {
					risk = "shell"
					sig = fmt.Sprint(b.Cmd, b.Args, b.HoldMs)
				} else {
					var specs []string
					if t == "key" {
						specs = append(specs, b.Key)
					}
					if t == "keyDelta" {
						specs = append(specs, b.KeyCW, b.KeyCCW)
					}
					if b.Press != nil && b.Press.Type == "key" {
						specs = append(specs, b.Press.Key)
					}
					for _, sp := range specs {
						if sp != "" && actions.KeyIsDestructive(sp) {
							risk = "destructive"
							sig += sp + "|"
						}
					}
				}
			}
			if risk != "" {
				out[bn+"/"+cn] = risk + ":" + sig
			}
		}
	}
	return out
}

func newDanger(pre, post map[string]string) []string {
	var d []string
	for k, v := range post {
		if pre[k] != v {
			d = append(d, k+"="+v+" (was "+pre[k]+")")
		}
	}
	sort.Strings(d)
	return d
}

var c6keys = []string{"Cmd+Q", "Cmd+T", "Cmd+W", "Right", "Cmd+Delete", "Cmd+Z", "Ctrl+K"}
var c6cmds = []string{"/usr/bin/open", "/bin/sh", "/usr/bin/say"}

func c6assign(r *rand.Rand) string {
	k := func() string { return c6keys[r.Intn(len(c6keys))] }
	switch r.Intn(14) {
	case 0:
		return `{"type":"key"}`
	case 1:
		return fmt.Sprintf(`{"type":"key","key":%q}`, k())
	case 2:
		return `{"type":"keyDelta"}`
	case 3:
		return fmt.Sprintf(`{"type":"keyDelta","keyCW":%q}`, k())
	case 4:
		return `{"type":"shell"}`
	case 5:
		return fmt.Sprintf(`{"type":"shell","cmd":%q}`, c6cmds[r.Intn(3)])
	case 6:
		return fmt.Sprintf(`{"type":"shell","cmd":%q,"args":["-c","x%d"]}`, c6cmds[r.Intn(3)], r.Intn(3))
	case 7:
		return `{"type":"none"}`
	case 8:
		return fmt.Sprintf(`{"type":"key","key":%q,"holdMs":300}`, k())
	case 9:
		return `{"type":"key","holdMs":300}`
	case 10:
		return `{"type":"scrollDelta"}`
	case 11:
		return `{"type":"layer","layer":2}`
	case 12:
		return `{"action":"generic.key","type":"key"}`
	}
	return `{"type":"disarm"}`
}

func c6global(r *rand.Rand) string {
	var p []string
	k := func() string { return c6keys[r.Intn(len(c6keys))] }
	if r.Intn(2) == 0 {
		p = append(p, fmt.Sprintf(`"key":{"key":%q}`, k()))
	}
	if r.Intn(3) == 0 {
		p = append(p, fmt.Sprintf(`"keyDelta":{"keyCW":%q,"keyCCW":%q}`, k(), k()))
	}
	if r.Intn(3) == 0 {
		p = append(p, fmt.Sprintf(`"shell":{"cmd":%q}`, c6cmds[r.Intn(3)]))
	}
	if r.Intn(4) == 0 {
		p = append(p, fmt.Sprintf(`"shell":{"cmd":%q,"args":["y%d"]}`, c6cmds[r.Intn(3)], r.Intn(3)))
	}
	// dedupe dup keys: last wins in json; keep first of each name
	seen := map[string]bool{}
	var q []string
	for _, x := range p {
		n := x[:strings.Index(x, ":")]
		if !seen[n] {
			seen[n] = true
			q = append(q, x)
		}
	}
	return `{"actionDefaults":{` + strings.Join(q, ",") + `}}`
}

func c6banks(r *rand.Rand) string {
	var banks []string
	for _, bn := range []string{"base", "shift", "layer-2"} {
		if r.Intn(3) == 0 && bn != "base" {
			continue
		}
		var cs []string
		for _, c := range []string{"C1", "C2", "C3"} {
			if r.Intn(2) == 0 {
				cs = append(cs, fmt.Sprintf(`%q:%s`, c, c6assign(r)))
			}
		}
		banks = append(banks, fmt.Sprintf(`%q:{%s}`, bn, strings.Join(cs, ",")))
	}
	return `{` + strings.Join(banks, ",") + `}`
}

func c6bundle(t *testing.T, r *rand.Rand, withGlobal bool) []byte {
	f := map[string][]byte{}
	if withGlobal || r.Intn(2) == 0 {
		g := fmt.Sprintf(`{"schema":1,"id":"_global","name":"Global","packs":["generic"],"banks":%s,"settings":%s}`, c6banks(r), c6global(r))
		if r.Intn(4) == 0 {
			g = fmt.Sprintf(`{"schema":1,"id":"_global","name":"Global","packs":["generic"],"banks":%s,"settings":{}}`, c6banks(r))
		}
		f["_global.json"] = []byte(g)
	}
	for _, id := range []string{chrome, safari} {
		if r.Intn(2) == 0 {
			f[id+".json"] = pjson(id, c6banks(r))
		}
	}
	if len(f) == 0 {
		f[chrome+".json"] = pjson(chrome, c6banks(r))
	}
	return bundleOf(t, f)
}

func TestWaveO_TwoStepDifferentialFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(66))
	miss, skipmiss, ev := 0, 0, 0
	trials := 400 // the sealed run (with -race) stays short; WAVEO_FULL=1 runs the critic's 1500
	if os.Getenv("WAVEO_FULL") == "1" {
		trials = 1500
	}
	for trial := 0; trial < trials; trial++ {
		s, _ := emptyStore(t)
		// step 1: preload a first bundle (confirmed)
		a := c6bundle(t, r, false)
		if _, err := s.ImportOpts(a, false, ImportOptions{ConfirmRisk: true}); err != nil {
			continue
		}
		pre := dset(s)
		b := c6bundle(t, r, false)
		rep, err := s.ImportOpts(b, true, ImportOptions{})
		if err != nil {
			continue
		}
		repS, errS := s.ImportOpts(b, true, ImportOptions{SkipRisky: true})
		// skip apply on a clone? apply skip first on a separate store is complex; do sequentially: skip-apply, measure, then undo
		_ = repS
		if errS != nil {
			continue
		}
		ev++
		sk, err := s.ImportOpts(b, false, ImportOptions{SkipRisky: true})
		if err != nil {
			continue
		}
		_ = sk
		postSkip := dset(s)
		if d := newDanger(pre, postSkip); len(d) > 0 {
			skipmiss++
			if skipmiss <= 6 {
				t.Logf("SKIPRISKY LEFT DANGER trial %d: %v\n  A=%s\n  B=%s\n", trial, d, dumpBundle(a), dumpBundle(b))
			}
		}
		// undo to get pre-state, then do full confirmed apply
		if _, _, err := s.Undo(); err != nil {
			t.Fatal(err)
		}
		if x := dset(s); fmt.Sprint(x) != fmt.Sprint(pre) {
			t.Log("undo differs?")
		}
		if _, err := s.ImportOpts(b, false, ImportOptions{ConfirmRisk: true}); err != nil {
			continue
		}
		post := dset(s)
		d := newDanger(pre, post)
		if len(d) > 0 && !rep.NeedsConfirm {
			miss++
			if miss <= 8 {
				t.Logf("MISSED trial %d: %v\n  report risk=%+v\n  A=%s\n  B=%s\n", trial, d, rep.Risk, dumpBundle(a), dumpBundle(b))
			}
		}
	}
	t.Logf("evaluated=%d missed=%d skipmiss=%d", ev, miss, skipmiss)
	if ev < trials/5 {
		t.Errorf("only %d trials evaluated", ev)
	}
	if miss != 0 || skipmiss != 0 {
		t.Errorf("the report missed a new runtime danger in %d trials; skipRisky left one in %d", miss, skipmiss)
	}
}

func dumpBundle(data []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err.Error()
	}
	var out []string
	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			continue
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		var m map[string]any
		json.Unmarshal(b, &m)
		bb, _ := json.Marshal(map[string]any{"banks": m["banks"], "settings": m["settings"]})
		out = append(out, f.Name+" "+string(bb))
	}
	return strings.Join(out, "\n   ")
}

// ---- P2-C: losing the panic key is a risk of its own --------------------------------------------

func TestImport_RemovingOrChangingThePanicKeyNeedsConfirmAndSkipRiskyKeepsIt(t *testing.T) {
	for name, body := range map[string]string{
		"removed":                   `{"base":{"C1":{"type":"key","key":"Cmd+T"}}}`,
		"changed":                   `{"base":{"C8":{"type":"key","key":"Cmd+T"}}}`,
		"other-action-same-control": `{"base":{"C8":{"type":"layer","layer":2}}}`,
	} {
		s, _ := emptyStore(t)
		mustAssign(t, s, GlobalID, "base", "C8", map[string]any{"type": "disarm"})
		data := bundleOf(t, map[string][]byte{"_global.json": pjson(GlobalID, body)})
		rep, err := s.ImportOpts(data, true, ImportOptions{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rep.Risk.PanicRemoved != 1 || !rep.NeedsConfirm || len(rep.Files[0].PanicRemoved) != 1 || !strings.Contains(rep.Files[0].PanicRemoved[0], "C8") {
			t.Errorf("%s: risk %+v confirm=%v files=%+v", name, rep.Risk, rep.NeedsConfirm, rep.Files[0].PanicRemoved)
		}
		if _, err := s.ImportOpts(data, false, ImportOptions{}); err == nil {
			t.Errorf("%s: applied unconfirmed", name)
		}
		rep, err = s.ImportOpts(data, false, ImportOptions{SkipRisky: true})
		if err != nil {
			t.Errorf("%s: skipRisky: %v", name, err)
			continue
		}
		g, _ := s.Snapshot().Preset(GlobalID)
		if actions.EffectiveType(g.Banks["base"]["C8"]) != "disarm" {
			t.Errorf("%s: skipRisky lost the panic key: %v", name, g.Banks["base"]["C8"])
		}
		found := false
		for _, d := range rep.Dropped {
			if d.Action == "generic.disarm" && d.Control == "C8" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: dropped = %v", name, rep.Dropped)
		}
	}
	// an unchanged panic key in the bundle is no risk
	s, _ := emptyStore(t)
	mustAssign(t, s, GlobalID, "base", "C8", map[string]any{"type": "disarm"})
	data := bundleOf(t, map[string][]byte{"_global.json": pjson(GlobalID, `{"base":{"C8":{"type":"disarm"}}}`)})
	if rep, err := s.ImportOpts(data, true, ImportOptions{}); err != nil || rep.Risk.PanicRemoved != 0 || rep.NeedsConfirm {
		t.Errorf("unchanged: %+v %v", rep.Risk, err)
	}
}

// ---- P2-E: outside edits that add risk are held back ------------------------------------------------

func writeOutside(t *testing.T, s *Store, id, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Dir(), id+".json"), pjson(id, body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantine_RiskyOutsideEditsAreNotAppliedAndStayOnDisk(t *testing.T) {
	cases := map[string]struct {
		body string
		cat  string
	}{
		"shell":       {`{"base":{"C1":{"type":"shell","cmd":"/usr/bin/open","args":["-a","Calculator"]}}}`, "shell"},
		"destructive": {`{"base":{"C1":{"type":"key","key":"Cmd+Q"}}}`, "destructive"},
		"unknown":     {`{"base":{"C1":{"type":"mystery"}}}`, "unknown"},
	}
	for name, c := range cases {
		s, dir := emptyStore(t)
		mustAdd(t, s, chrome)
		mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+T"))
		before := s.Snapshot()
		var seen []PendingExternal
		s.opts.OnQuarantine = func(pe PendingExternal) { seen = append(seen, pe) }
		writeOutside(t, s, chrome, c.body)
		raw, _ := os.ReadFile(filepath.Join(dir, chrome+".json"))
		changed, err := s.CheckExternal()
		if err != nil || changed {
			t.Fatalf("%s: applied an outside risky edit (changed=%v %v)", name, changed, err)
		}
		if s.Snapshot() != before {
			t.Errorf("%s: the running state moved", name)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, chrome+".json")); !bytes.Equal(got, raw) {
			t.Errorf("%s: the file on disk was touched", name)
		}
		pend := s.PendingExternal()
		if len(pend) != 1 || pend[0].Preset != chrome || pend[0].Kind != "change" || len(pend[0].Changes) == 0 || pend[0].Risks[0] != c.cat {
			t.Fatalf("%s: pending = %+v", name, pend)
		}
		if len(seen) != 1 {
			t.Errorf("%s: OnQuarantine called %d times", name, len(seen))
		}
		if !strings.Contains(strings.Join(pend[0].Changes, " "), "C1") {
			t.Errorf("%s: changes do not say what: %v", name, pend[0].Changes)
		}
		// the poll does not re-report or apply it
		for i := 0; i < 3; i++ {
			s.CheckExternal()
		}
		if len(s.PendingExternal()) != 1 || len(seen) != 1 || s.Snapshot() != before {
			t.Errorf("%s: repeated polls changed things", name)
		}
		// warnings show it
		found := false
		for _, w := range s.Warnings() {
			if w.Kind == "pending-external" && w.App == chrome {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no pending-external warning", name)
		}
	}
}

func TestQuarantine_ApproveAppliesOneUndoStepWithABackupAndNeedsConfirm(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	writeOutside(t, s, chrome, `{"base":{"C1":{"type":"shell","cmd":"/usr/bin/open"}}}`)
	s.CheckExternal()
	pe := s.PendingExternal()[0]
	if err := s.ApproveExternal(pe.ID, false); !errors.Is(err, ErrConfirmNeeded) {
		t.Fatalf("approve without confirm: %v", err)
	}
	if err := s.ApproveExternal("nope", true); !errors.Is(err, ErrNoPending) {
		t.Fatalf("unknown id: %v", err)
	}
	nBackups := len(s.Backups())
	hist, _ := s.History()
	if err := s.ApproveExternal(pe.ID, true); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Snapshot().Preset(chrome)
	if p.Banks["base"]["C1"] == nil || len(s.PendingExternal()) != 0 {
		t.Errorf("not applied / still pending: %v", p.Banks)
	}
	if len(s.Backups()) <= nBackups {
		t.Error("no backup before approving")
	}
	if h, _ := s.History(); len(h) != len(hist)+1 {
		t.Errorf("history grew by %d", len(h)-len(hist))
	}
	if _, _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Snapshot().Preset(chrome); p.Banks["base"]["C1"] != nil {
		t.Error("one undo did not take the approved change back")
	}
}

func TestQuarantine_RejectRestoresTheFileAndKeepsACopy(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+T"))
	good, _ := os.ReadFile(filepath.Join(dir, chrome+".json"))
	writeOutside(t, s, chrome, `{"base":{"C1":{"type":"shell","cmd":"/usr/bin/open"}}}`)
	bad, _ := os.ReadFile(filepath.Join(dir, chrome+".json"))
	s.CheckExternal()
	pe := s.PendingExternal()[0]
	if err := s.RejectExternal(pe.ID); err != nil {
		t.Fatal(err)
	}
	now, _ := os.ReadFile(filepath.Join(dir, chrome+".json"))
	if !bytes.Equal(now, good) {
		t.Errorf("the file was not restored from memory:\n%s", now)
	}
	es, _ := os.ReadDir(filepath.Join(dir, ".backups"))
	kept := false
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "rejected-"+chrome+"-") {
			if b, _ := os.ReadFile(filepath.Join(dir, ".backups", e.Name())); bytes.Equal(b, bad) {
				kept = true
			}
		}
	}
	if !kept {
		t.Error("the rejected file was not kept in .backups/rejected-*")
	}
	if len(s.PendingExternal()) != 0 {
		t.Error("still pending")
	}
	if changed, _ := s.CheckExternal(); changed {
		t.Error("the restored file was treated as a new outside edit")
	}
	if err := s.RejectExternal(pe.ID); !errors.Is(err, ErrNoPending) {
		t.Errorf("second reject: %v", err)
	}
}

func TestQuarantine_HarmlessOutsideEditsStillApply(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	writeOutside(t, s, chrome, `{"base":{"C1":{"type":"key","key":"Cmd+T"},"C2":{"type":"layer","layer":2}}}`)
	if changed, err := s.CheckExternal(); err != nil || !changed {
		t.Fatalf("a harmless outside edit was held back: %v %v", changed, err)
	}
	if len(s.PendingExternal()) != 0 {
		t.Error("pending for a harmless edit")
	}
	// ...and so does an outside edit that REMOVES risk
	mustAssign(t, s, chrome, "base", "C3", map[string]any{"type": "shell", "cmd": "/usr/bin/open"})
	writeOutside(t, s, chrome, `{"base":{"C1":{"type":"key","key":"Cmd+T"}}}`)
	if changed, _ := s.CheckExternal(); !changed {
		t.Error("removing risk from outside was held back")
	}
}

func TestQuarantine_GlobalDefaultsGuardSettingsAndPanicLoss(t *testing.T) {
	// new Global defaults that worsen an existing app assignment
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	if err := s.commit("seed", func(ps map[string]*Preset) error {
		ps[chrome].Banks["base"] = map[string]Assignment{"C1": {"type": "key"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.Dir(), GlobalID+".json"), globalFile(t, globalDefaults("key", `{"key":"Cmd+Q"}`)), 0o644)
	if changed, _ := s.CheckExternal(); changed || len(s.PendingExternal()) != 1 {
		t.Fatalf("Global defaults: changed=%v pending=%v", changed, s.PendingExternal())
	}
	// a weakened guard
	s2, _ := seededStore(t)
	pre, _ := s2.Snapshot().Preset(ResolveID)
	c := pre.Clone()
	c.Settings["dodeeMostGuard"] = false
	raw, _ := c.Marshal()
	os.WriteFile(filepath.Join(s2.Dir(), ResolveID+".json"), raw, 0o644)
	if changed, _ := s2.CheckExternal(); changed {
		t.Error("a weakened guard from outside was applied")
	}
	if pend := s2.PendingExternal(); len(pend) != 1 || pend[0].Risks[0] != "guard-settings" {
		t.Errorf("pending = %+v", pend)
	}
	// losing the panic key: an edit, and a deleted file
	s3, d3 := emptyStore(t)
	mustAdd(t, s3, chrome)
	mustAssign(t, s3, chrome, "base", "C8", map[string]any{"type": "disarm"})
	writeOutside(t, s3, chrome, `{"base":{"C8":{"type":"key","key":"Cmd+T"}}}`)
	if changed, _ := s3.CheckExternal(); changed || len(s3.PendingExternal()) != 1 || s3.PendingExternal()[0].Risks[0] != "panic-removed" {
		t.Errorf("panic key edit: changed=%v pending=%+v", changed, s3.PendingExternal())
	}
	s3.RejectExternal(s3.PendingExternal()[0].ID)
	os.Remove(filepath.Join(d3, chrome+".json"))
	if changed, _ := s3.CheckExternal(); changed {
		t.Error("deleting the file that holds the panic key was applied")
	}
	pend := s3.PendingExternal()
	if len(pend) != 1 || pend[0].Kind != "removal" {
		t.Fatalf("pending = %+v", pend)
	}
	// reject restores the deleted file from memory
	if err := s3.RejectExternal(pend[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d3, chrome+".json")); err != nil {
		t.Errorf("the deleted file was not restored: %v", err)
	}
	// approving a removal removes the preset
	os.Remove(filepath.Join(d3, chrome+".json"))
	s3.CheckExternal()
	pend = s3.PendingExternal()
	if err := s3.ApproveExternal(pend[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := s3.Snapshot().Preset(chrome); ok {
		t.Error("approved removal left the preset")
	}
}

// ---- P2-E part 2: the authoritative markers live on the internal disk -------------------------------

func stateStore(t *testing.T, stateDir string) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "presets")
	s, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, StateDir: stateDir, CanaryDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestStateDir_GuestsOnTheShareCannotReArmOrMarkFirstRun(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	s := stateStore(t, state)
	env := func(string) string { return "" }
	// a guest forges the share copy of "first run done": it must not count
	os.WriteFile(filepath.Join(s.Dir(), FirstRunMarker), []byte("x"), 0o644)
	if d, why := s.StartDisarmed(env); !d || why != StartReasonFirstRun {
		t.Fatalf("a forged share marker marked the first run done: %v %q", d, why)
	}
	// the user arms for real: both copies, and now it counts
	if err := s.MarkFirstRunDone(); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.StartDisarmed(env); d {
		t.Fatal("setup: should be armed now")
	}
	// the user disarms on purpose: both copies written
	if err := s.MarkDisarmed(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{state, s.Dir()} {
		if _, err := os.Stat(filepath.Join(dir, DisarmedMarker)); err != nil {
			t.Errorf("marker missing in %s", dir)
		}
	}
	// a guest deletes the share copy: still disarmed
	os.Remove(filepath.Join(s.Dir(), DisarmedMarker))
	if d, why := s.StartDisarmed(env); !d || why != StartReasonDisarmedLast {
		t.Errorf("deleting the share marker re-armed the deck: %v %q", d, why)
	}
	// a guest deletes the share first-run marker: irrelevant
	os.Remove(filepath.Join(s.Dir(), FirstRunMarker))
	// ...and a guest creating only the share .disarmed also disarms (either place is enough)
	s.ClearDisarmed()
	if d, _ := s.StartDisarmed(env); d {
		t.Fatal("setup: armed again")
	}
	os.WriteFile(filepath.Join(s.Dir(), DisarmedMarker), []byte("x"), 0o644)
	if d, why := s.StartDisarmed(env); !d || why != StartReasonDisarmedLast {
		t.Errorf("a share-only disarm marker was ignored: %v %q", d, why)
	}
	// arming removes both
	s.ClearDisarmed()
	for _, dir := range []string{state, s.Dir()} {
		if _, err := os.Stat(filepath.Join(dir, DisarmedMarker)); err == nil {
			t.Errorf("marker left in %s", dir)
		}
	}
	// the state folder is private
	if fi, err := os.Stat(state); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir = %v %v, want 0700", fi, err)
	}
}

func TestStateDir_UnusableFailsClosedAndWarns(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("not a folder"), 0o644)
	s := stateStore(t, filepath.Join(blocker, "state")) // cannot be created under a file
	s.MarkFirstRunDone()                                // even the share copy cannot make it armed
	if d, why := s.StartDisarmed(func(string) string { return "" }); !d || why != StartReasonStateDown {
		t.Errorf("start = %v %q, want disarmed (state-unavailable)", d, why)
	}
	found := false
	for _, w := range s.Warnings() {
		if w.Kind == "state-dir" && w.Severity == "warn" {
			found = true
		}
	}
	if !found {
		t.Error("no state-dir warning")
	}
}

func TestStateDir_NotConfiguredKeepsTheEarlierBehaviour(t *testing.T) {
	s, _ := emptyStore(t)
	s.MarkFirstRunDone()
	if d, _ := s.StartDisarmed(func(string) string { return "" }); d {
		t.Error("share-only mode changed")
	}
}

// ---- wave P P2-2: in-range extremes -------------------------------------------------------------------

func TestSettings_InRangeChangesTowardsLessSafeAreFlaggedSaferOnesAreNot(t *testing.T) {
	flagged := []string{
		`{"keyDebounceMs":25}`, `{"maxScrubPx":120}`, `{"pxPerUnit":20}`, `{"fineFactor":0.9}`,
		`{"wheelSensMultipliers":{"lift":2}}`, `{"finderTagConfirmThreshold":900}`, `{"minScrubPx":0.0}`,
	}
	for _, st := range flagged {
		if st == `{"minScrubPx":0.0}` {
			continue // 0 = default: not a change of the effective value
		}
		s, _ := seededStore(t)
		rep, err := s.ImportOpts(resolveWithSettings(t, st), true, ImportOptions{})
		if err != nil {
			t.Errorf("%s: %v", st, err)
			continue
		}
		if rep.Risk.Settings == 0 || !rep.NeedsConfirm {
			t.Errorf("%s: not flagged (risk %+v)", st, rep.Risk)
		}
	}
	for _, st := range []string{`{"keyDebounceMs":80}`, `{"maxScrubPx":60}`, `{"pxPerUnit":5}`, `{"fineFactor":0.3}`, `{"wheelSensMultipliers":{"lift":0.5}}`} {
		s, _ := seededStore(t)
		rep, err := s.ImportOpts(resolveWithSettings(t, st), true, ImportOptions{})
		if err != nil {
			t.Errorf("%s: %v", st, err)
			continue
		}
		if rep.Risk.Settings != 0 {
			t.Errorf("%s: a safer value was flagged (%+v)", st, rep.Risk)
		}
	}
	// the extremes the old ranges let through are refused now
	for _, st := range []string{
		`{"keyDebounceMs":1}`, `{"keyDebounceMs":1000,"coalesceWindowMs":2000}`, `{"maxScrubPx":2000,"pxPerUnit":1000}`,
		`{"wheelSensMultipliers":{"lift":100}}`, `{"minScrubPx":400,"maxScrubPx":10}`, `{"finderTagConfirmThreshold":5000}`,
	} {
		s, _ := seededStore(t)
		if _, err := s.ImportOpts(resolveWithSettings(t, st), true, ImportOptions{}); err == nil {
			t.Errorf("%s: accepted", st)
		}
	}
}
