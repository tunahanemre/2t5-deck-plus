package deps

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"2t5-deck-plus/docs"
)

func bp(b bool) *bool { return &b }

func byID(rs []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range rs {
		m[r.ID] = r
	}
	return m
}

func goodInputs() Inputs {
	return Inputs{
		OS:               &Version{15, 3, 1},
		Perms:            &Perms{Accessibility: true, ScreenRecording: true, AutomationFinder: "granted"},
		Python:           &PythonFacts{Found: true, Path: "/opt/homebrew/bin/python3", Version: "3.12.4", VersionOK: true, StdlibOK: true, ResolveModule: true},
		ResolveInstalled: bp(true),
		ResolveRunning:   bp(true),
		Sidecar:          "connected",
		MIDI:             &MIDIFacts{Sources: []string{"Loupedeck Loupedeck MIDI", "IAC Driver Bus 1"}},
		OtherApps:        &[]string{},
	}
}

func TestEvaluate_AllGood(t *testing.T) {
	m := byID(Evaluate(goodInputs()))
	for id := range CheckedIDs() {
		if m[id].Status != OK {
			t.Errorf("%s = %s (%s), want ok", id, m[id].Status, m[id].Detail)
		}
	}
	if len(m) != len(CheckedIDs()) {
		t.Errorf("Evaluate returned %d results, CheckedIDs has %d", len(m), len(CheckedIDs()))
	}
}

func TestEvaluate_NothingKnownIsUnknownNeverOK(t *testing.T) {
	m := byID(Evaluate(Inputs{}))
	for id := range CheckedIDs() {
		if m[id].Status != Unknown {
			t.Errorf("%s = %s with no facts, want unknown", id, m[id].Status)
		}
	}
}

func TestEvaluate_MacOSVersion(t *testing.T) {
	cases := []struct {
		v          Version
		base, read string
	}{
		{Version{10, 15, 7}, Missing, Missing},
		{Version{11, 0, 0}, OK, Missing},
		{Version{12, 7, 6}, OK, Missing},
		{Version{15, 1, 0}, OK, Missing},
		{Version{15, 2, 0}, OK, OK},
		{Version{26, 0, 1}, OK, OK},
	}
	for _, c := range cases {
		in := goodInputs()
		v := c.v
		in.OS = &v
		m := byID(Evaluate(in))
		if m["macos-version"].Status != c.base || m["macos-screen-reads"].Status != c.read {
			t.Errorf("macOS %v: version=%s screen-reads=%s, want %s/%s", c.v, m["macos-version"].Status, m["macos-screen-reads"].Status, c.base, c.read)
		}
	}
}

func TestVersionParse(t *testing.T) {
	for in, want := range map[string]Version{"15.3.1": {15, 3, 1}, "11.7": {11, 7, 0}, "26": {26, 0, 0}} {
		got, ok := ParseVersion(in)
		if !ok || got != want {
			t.Errorf("ParseVersion(%q) = %v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "x", "1.x", "1..2"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("ParseVersion(%q) accepted", bad)
		}
	}
}

func TestEvaluate_Permissions(t *testing.T) {
	in := goodInputs()
	in.Perms = &Perms{Accessibility: false, ScreenRecording: false, AutomationFinder: "denied"}
	m := byID(Evaluate(in))
	for _, id := range []string{"accessibility", "screen-recording", "automation-finder"} {
		if m[id].Status != Missing {
			t.Errorf("%s = %s, want missing", id, m[id].Status)
		}
	}
	in.Perms = &Perms{Accessibility: true, ScreenRecording: true, AutomationFinder: "unknown"}
	m = byID(Evaluate(in))
	if m["automation-finder"].Status != Unknown {
		t.Errorf("not-asked Finder automation = %s, want unknown", m["automation-finder"].Status)
	}
}

func TestEvaluate_Python(t *testing.T) {
	cases := map[string]struct {
		p    PythonFacts
		want string
	}{
		"none found":        {PythonFacts{}, Missing},
		"too old":           {PythonFacts{Found: true, Path: "/usr/local/bin/python3", Version: "3.5.2", VersionOK: false, StdlibOK: true}, Missing},
		"broken stdlib":     {PythonFacts{Found: true, Path: "/x/python3", Version: "3.12.0", VersionOK: true, StdlibOK: false}, Missing},
		"works, no Resolve": {PythonFacts{Found: true, Path: "/x/python3", Version: "3.12.0", VersionOK: true, StdlibOK: true}, OK},
	}
	for name, c := range cases {
		in := goodInputs()
		p := c.p
		in.Python = &p
		r := byID(Evaluate(in))["python3"]
		if r.Status != c.want {
			t.Errorf("%s: python3 = %s (%s), want %s", name, r.Status, r.Detail, c.want)
		}
	}
	in := goodInputs()
	in.Python = &PythonFacts{Found: true, Path: "/opt/homebrew/bin/python3", Version: "3.12.4", VersionOK: true, StdlibOK: true}
	r := byID(Evaluate(in))["python3"]
	if !strings.Contains(r.Detail, "3.12.4") || !strings.Contains(r.Detail, "/opt/homebrew/bin/python3") {
		t.Errorf("detail should name the interpreter: %q", r.Detail)
	}
	in.Python = &PythonFacts{Err: "probe timed out"}
	if got := byID(Evaluate(in))["python3"]; got.Status != Unknown {
		t.Errorf("a probe error is unknown, got %s", got.Status)
	}
}

func TestEvaluate_ResolveInstallAndScripting(t *testing.T) {
	in := goodInputs()
	in.ResolveInstalled = bp(false)
	if r := byID(Evaluate(in))["resolve-install"]; r.Status != Missing {
		t.Errorf("resolve-install without the scripting folder = %s", r.Status)
	}
	// installed but the helper's Python cannot import the module: install is ok, the detail says so
	in = goodInputs()
	in.Python.ResolveModule = false
	r := byID(Evaluate(in))["resolve-install"]
	if r.Status != OK || !strings.Contains(strings.ToLower(r.Detail), "python") {
		t.Errorf("installed but not importable: %s %q", r.Status, r.Detail)
	}

	for _, c := range []struct {
		name      string
		running   *bool
		sidecar   string
		pyFound   bool
		want      string
		detailHas string
	}{
		{"resolve closed", bp(false), "waiting", true, Unknown, "not running"},
		{"connected", bp(true), "connected", true, OK, ""},
		{"retrying", bp(true), "retrying", true, Missing, "External scripting"},
		{"connecting", bp(true), "connecting", true, Unknown, ""},
		{"waiting", bp(true), "waiting", true, Unknown, ""},
		{"no python", bp(true), "waiting", false, Unknown, "Python"},
		{"running unknown", nil, "retrying", true, Unknown, ""},
	} {
		in := goodInputs()
		in.ResolveRunning, in.Sidecar = c.running, c.sidecar
		in.Python.Found = c.pyFound
		r := byID(Evaluate(in))["resolve-scripting"]
		if r.Status != c.want {
			t.Errorf("%s: %s (%s), want %s", c.name, r.Status, r.Detail, c.want)
		}
		if c.detailHas != "" && !strings.Contains(r.Detail, c.detailHas) {
			t.Errorf("%s: detail %q lacks %q", c.name, r.Detail, c.detailHas)
		}
	}
}

func TestEvaluate_Loupedeck(t *testing.T) {
	in := goodInputs()
	in.MIDI = &MIDIFacts{Sources: []string{"IAC Driver Bus 1"}}
	if r := byID(Evaluate(in))["loupedeck"]; r.Status != Missing {
		t.Errorf("no Loupedeck source = %s", r.Status)
	}
	in.MIDI = &MIDIFacts{Sources: nil}
	if r := byID(Evaluate(in))["loupedeck"]; r.Status != Missing {
		t.Errorf("empty source list = %s", r.Status)
	}
	in.MIDI = &MIDIFacts{Sources: []string{"LOUPEDECK"}}
	if r := byID(Evaluate(in))["loupedeck"]; r.Status != OK {
		t.Errorf("case-insensitive match = %s", r.Status)
	}
	in.MIDI = &MIDIFacts{Err: "CoreMIDI not available"}
	if r := byID(Evaluate(in))["loupedeck"]; r.Status != Unknown {
		t.Errorf("a failed listing is unknown, got %s", r.Status)
	}
}

func TestEvaluate_OtherDeckApp(t *testing.T) {
	in := goodInputs()
	names := []string{"Loupedeck", "LD-RESOLVE"}
	in.OtherApps = &names
	r := byID(Evaluate(in))["no-other-deck-app"]
	if r.Status != Missing || !strings.Contains(r.Detail, "Loupedeck") || !strings.Contains(r.Detail, "LD-RESOLVE") {
		t.Errorf("other apps: %s %q", r.Status, r.Detail)
	}
}

func TestFindConflicts(t *testing.T) {
	apps := []App{
		{ID: "com.apple.finder", Name: "Finder"},
		{ID: "com.loupedeck.loupedeckApp", Name: "Loupedeck"},
		{ID: "com.example.mapper", Name: "Bome MIDI Translator Pro"},
		{ID: "com.example.mapper2", Name: "MidiKey2Key"},
		{ID: "com.2t5.deck-plus", Name: "2T5 DECK+"},
		{ID: "com.apple.Music", Name: "Music"},
	}
	got := FindConflicts(apps, func(string) bool { return false })
	sort.Strings(got)
	want := []string{"Bome MIDI Translator Pro", "Loupedeck", "MidiKey2Key"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("FindConflicts = %v, want %v", got, want)
	}
	// a background app the Dock list cannot show is found by bundle id
	got = FindConflicts(nil, func(id string) bool { return id == "com.2t5.ld-resolve" })
	if len(got) != 1 || got[0] != "LD-RESOLVE" {
		t.Errorf("by bundle id = %v", got)
	}
	// the same app seen both ways is listed once
	got = FindConflicts([]App{{ID: "com.loupedeck.loupedeckApp", Name: "Loupedeck"}}, func(id string) bool { return id == "com.loupedeck.loupedeckApp" })
	if len(got) != 1 {
		t.Errorf("duplicate listing: %v", got)
	}
	if got := FindConflicts([]App{{ID: "com.apple.finder", Name: "Finder"}}, func(string) bool { return false }); len(got) != 0 {
		t.Errorf("false positive: %v", got)
	}
}

// ---- the system collector: stubbed probes, TTL cache -------------------------

func TestSystem_FactsUseOnlyTheProbesAndCache(t *testing.T) {
	var py, midi, apps int
	now := time.Unix(1000, 0)
	s := NewSystem(Probes{
		OSVersion: func() (Version, bool) { return Version{15, 3, 0}, true },
		Python: func() PythonFacts {
			py++
			return PythonFacts{Found: true, Version: "3.12.0", VersionOK: true, StdlibOK: true}
		},
		ResolveInstalled: func() bool { return true },
		ResolveRunning:   func() bool { return false },
		MIDISources:      func() ([]string, error) { midi++; return []string{"Loupedeck"}, nil },
		RunningApps:      func() []App { apps++; return []App{{ID: "com.apple.finder", Name: "Finder"}} },
		AppRunning:       func(string) bool { return false },
	})
	s.now = func() time.Time { return now }
	in := s.Facts(false)
	if in.OS == nil || in.Python == nil || in.MIDI == nil || in.OtherApps == nil || in.ResolveInstalled == nil || in.ResolveRunning == nil {
		t.Fatalf("a probe was dropped: %+v", in)
	}
	if len(*in.OtherApps) != 0 {
		t.Errorf("Finder is not a conflict: %v", *in.OtherApps)
	}
	// within the TTL the slow probe (python) is not run again, the cheap ones are
	now = now.Add(5 * time.Second)
	s.Facts(false)
	if py != 1 {
		t.Errorf("python ran %d times inside the TTL", py)
	}
	if midi != 2 || apps != 2 {
		t.Errorf("cheap probes should run every call: midi=%d apps=%d", midi, apps)
	}
	// fresh=true forces the slow probe
	s.Facts(true)
	if py != 2 {
		t.Errorf("fresh did not re-run python (%d)", py)
	}
	// after the TTL it runs again by itself
	now = now.Add(time.Minute)
	s.Facts(false)
	if py != 3 {
		t.Errorf("python did not refresh after the TTL (%d)", py)
	}
}

func TestSystem_MissingProbesAreUnknown(t *testing.T) {
	s := NewSystem(Probes{})
	in := s.Facts(true)
	if in.OS != nil || in.Python != nil || in.MIDI != nil || in.OtherApps != nil || in.ResolveInstalled != nil || in.ResolveRunning != nil {
		t.Errorf("unwired probes must leave facts nil: %+v", in)
	}
}

func TestSystem_ProbeFailureIsCarriedNotHidden(t *testing.T) {
	s := NewSystem(Probes{MIDISources: func() ([]string, error) { return nil, os.ErrPermission }})
	in := s.Facts(true)
	if in.MIDI == nil || in.MIDI.Err == "" {
		t.Errorf("a MIDI listing error must be carried: %+v", in.MIDI)
	}
}

// ---- the catalog (docs/dependencies.json) ---------------------------------------

func TestCatalog_ParsesAndIsWellFormed(t *testing.T) {
	cat, err := LoadCatalog(docs.DependenciesJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat) < 8 {
		t.Fatalf("only %d entries", len(cat))
	}
	seen := map[string]bool{}
	kinds := map[string]bool{"runtime": true, "permission": true, "setting": true, "hardware": true, "os": true}
	for _, e := range cat {
		if seen[e.ID] {
			t.Errorf("duplicate id %s", e.ID)
		}
		seen[e.ID] = true
		if !kinds[e.Kind] {
			t.Errorf("%s: kind %q", e.ID, e.Kind)
		}
		for name, v := range map[string]string{"name": e.Name, "feature": e.Feature, "howToGet": e.HowToGet, "without": e.Without, "howToCheck": e.HowToCheck} {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s: empty %s", e.ID, name)
			}
		}
		if e.FixAction != "" && !FixActions[e.FixAction] {
			t.Errorf("%s: unknown fixAction %q", e.ID, e.FixAction)
		}
		if e.FixAction == "copy-text" && e.CopyText == "" {
			t.Errorf("%s: copy-text without copyText", e.ID)
		}
	}
}

func TestCatalog_EveryIDHasACheckOrIsInformationalAndViceVersa(t *testing.T) {
	cat, err := LoadCatalog(docs.DependenciesJSON)
	if err != nil {
		t.Fatal(err)
	}
	checked := CheckedIDs()
	inCat := map[string]bool{}
	for _, e := range cat {
		inCat[e.ID] = true
		_, has := checked[e.ID]
		switch {
		case e.Informational && has:
			t.Errorf("%s is marked informational but has a check", e.ID)
		case !e.Informational && !has:
			t.Errorf("%s has no check and is not marked informational", e.ID)
		}
	}
	for id := range checked {
		if !inCat[id] {
			t.Errorf("check %q has no entry in docs/dependencies.json", id)
		}
	}
}

func TestCatalog_RejectsBrokenJSON(t *testing.T) {
	for _, bad := range []string{``, `{}`, `[{"id":""}]`, `[{"id":"a","name":"x","kind":"nope","feature":"f","howToGet":"h","without":"w","howToCheck":"c"}]`} {
		if _, err := LoadCatalog([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// ---- README table ---------------------------------------------------------------

func TestReadmeTableMatchesTheCatalog(t *testing.T) {
	cat, err := LoadCatalog(docs.DependenciesJSON)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "README.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const start, end = "<!-- deps-table:start -->", "<!-- deps-table:end -->"
	s := string(raw)
	i, j := strings.Index(s, start), strings.Index(s, end)
	if i < 0 || j < i {
		t.Fatalf("README.md needs the %s ... %s markers", start, end)
	}
	want := start + "\n" + MarkdownTable(cat) + end
	have := s[i : j+len(end)]
	if have == want {
		return
	}
	if os.Getenv("UPDATE_README") == "1" {
		if err := os.WriteFile(path, []byte(s[:i]+want+s[j+len(end):]), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Errorf("the README requirements table is out of date: run UPDATE_README=1 go test ./internal/deps -run ReadmeTable")
}
