// Package deps answers "does this Mac have everything 2T5 DECK+ needs?".
//
// docs/dependencies.json is the one list (the editor, the README and the
// website all read it). Evaluate turns facts about the Mac into one status per
// dependency: ok, missing or unknown. It is pure: every fact is handed in, so
// the tests never touch MIDI, a process, or System Settings. A fact that could
// not be read is nil, and a nil fact is "unknown", never "ok".
package deps

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Statuses.
const (
	OK      = "ok"
	Missing = "missing"
	Unknown = "unknown"
)

// MinMacOS is the build target (LSMinimumSystemVersion). ScreenReadsMacOS is
// when ScreenCaptureKit's rectangle read exists (internal/macapp/windows_darwin.m).
var (
	MinMacOS         = Version{11, 0, 0}
	ScreenReadsMacOS = Version{15, 2, 0}
)

// Version is a dotted macOS version.
type Version struct{ Major, Minor, Patch int }

// AtLeast reports v >= o.
func (v Version) AtLeast(o Version) bool {
	if v.Major != o.Major {
		return v.Major > o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor > o.Minor
	}
	return v.Patch >= o.Patch
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// ParseVersion reads "15", "15.3" or "15.3.1".
func ParseVersion(s string) (Version, bool) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return Version{}, false
		}
		n[i] = v
	}
	return Version{n[0], n[1], n[2]}, true
}

// Perms is what macOS says about the three permissions.
type Perms struct {
	Accessibility, ScreenRecording bool
	// AutomationFinder is granted | denied | unknown (not asked yet).
	AutomationFinder string
}

// PythonFacts is what one probe of the Python the Resolve helper would use found.
type PythonFacts struct {
	// Err is set when the probe itself failed (timeout, cannot run): the answer is unknown.
	Err string
	// Found: an interpreter exists and runs.
	Found         bool
	Path, Version string
	// VersionOK: new enough for Resolve's own scripting module.
	VersionOK bool
	// StdlibOK: every module the helper imports from Python's standard library loads.
	StdlibOK bool
	// ResolveModule: it can import DaVinciResolveScript (needs Resolve installed).
	ResolveModule bool
}

// MIDIFacts is the Mac's MIDI source list (read only, nothing is opened).
type MIDIFacts struct {
	Sources []string
	Err     string
}

// Inputs are all the facts. nil = could not be read.
type Inputs struct {
	OS               *Version
	Perms            *Perms
	Python           *PythonFacts
	ResolveInstalled *bool
	ResolveRunning   *bool
	// Sidecar is the engine's sidecar state: waiting | connecting | connected | retrying | "".
	Sidecar string
	MIDI    *MIDIFacts
	// OtherApps are the names of running apps that also drive a deck.
	OtherApps *[]string
}

// Result is one check's answer.
type Result struct {
	ID     string `json:"id"`
	Status string `json:"status"` // ok | missing | unknown
	Detail string `json:"detail"`
}

// checkOrder is every dependency id that has a check. The catalog marks the
// rest "informational"; a test keeps the two lists in step.
var checkOrder = []string{
	"macos-version", "loupedeck", "no-other-deck-app", "accessibility", "screen-recording", "automation-finder",
	"macos-screen-reads", "python3", "resolve-install", "resolve-scripting",
}

// CheckedIDs is the set of ids Evaluate answers.
func CheckedIDs() map[string]struct{} {
	m := make(map[string]struct{}, len(checkOrder))
	for _, id := range checkOrder {
		m[id] = struct{}{}
	}
	return m
}

// Evaluate returns one Result per checked id, in checkOrder.
func Evaluate(in Inputs) []Result {
	out := make([]Result, 0, len(checkOrder))
	add := func(id, status, detail string) { out = append(out, Result{id, status, detail}) }

	// macOS
	switch {
	case in.OS == nil:
		add("macos-version", Unknown, "could not read the macOS version")
		add("macos-screen-reads", Unknown, "could not read the macOS version")
	default:
		if in.OS.AtLeast(MinMacOS) {
			add("macos-version", OK, "macOS "+in.OS.String())
		} else {
			add("macos-version", Missing, "macOS "+in.OS.String()+" is older than macOS 11")
		}
		if in.OS.AtLeast(ScreenReadsMacOS) {
			add("macos-screen-reads", OK, "macOS "+in.OS.String())
		} else {
			add("macos-screen-reads", Missing, "macOS "+in.OS.String()+" is older than macOS 15.2")
		}
	}

	// hardware
	add("loupedeck", loupedeck(in.MIDI), loupedeckDetail(in.MIDI))
	switch {
	case in.OtherApps == nil:
		add("no-other-deck-app", Unknown, "could not list the running apps")
	case len(*in.OtherApps) == 0:
		add("no-other-deck-app", OK, "no other deck app is running")
	default:
		add("no-other-deck-app", Missing, strings.Join(*in.OtherApps, ", ")+" is running and may answer the same deck")
	}

	// permissions
	if in.Perms == nil {
		for _, id := range []string{"accessibility", "screen-recording", "automation-finder"} {
			add(id, Unknown, "this host cannot read permissions")
		}
	} else {
		if in.Perms.Accessibility {
			add("accessibility", OK, "granted")
		} else {
			add("accessibility", Missing, "not granted")
		}
		if in.Perms.ScreenRecording {
			add("screen-recording", OK, "granted")
		} else {
			add("screen-recording", Missing, "not granted")
		}
		switch in.Perms.AutomationFinder {
		case "granted":
			add("automation-finder", OK, "granted")
		case "denied":
			add("automation-finder", Missing, "denied")
		default:
			add("automation-finder", Unknown, "not asked yet: macOS asks the first time a Finder tag key is used")
		}
	}

	// the order above is by group; put it back into checkOrder
	out = reorder(out)

	// python, resolve
	py, install, scripting := pythonCheck(in), installCheck(in), scriptingCheck(in)
	out = append(out, py, install, scripting)
	return out
}

func reorder(rs []Result) []Result {
	by := map[string]Result{}
	for _, r := range rs {
		by[r.ID] = r
	}
	var out []Result
	for _, id := range checkOrder {
		if r, ok := by[id]; ok {
			out = append(out, r)
		}
	}
	return out
}

func loupedeck(m *MIDIFacts) string {
	switch {
	case m == nil || m.Err != "":
		return Unknown
	case hasLoupedeck(m.Sources):
		return OK
	}
	return Missing
}

func hasLoupedeck(sources []string) bool {
	for _, s := range sources {
		if strings.Contains(strings.ToLower(s), "loupedeck") {
			return true
		}
	}
	return false
}

func loupedeckDetail(m *MIDIFacts) string {
	switch {
	case m == nil:
		return "could not list the MIDI sources"
	case m.Err != "":
		return "could not list the MIDI sources: " + m.Err
	case hasLoupedeck(m.Sources):
		return "a Loupedeck MIDI source is present"
	}
	return fmt.Sprintf("no Loupedeck among the Mac's %d MIDI source(s)", len(m.Sources))
}

func pythonCheck(in Inputs) Result {
	p := in.Python
	switch {
	case p == nil:
		return Result{"python3", Unknown, "could not look for Python"}
	case p.Err != "":
		return Result{"python3", Unknown, "could not run the Python check: " + p.Err}
	case !p.Found:
		return Result{"python3", Missing, "no Python 3 found (looked in Homebrew and python.org)"}
	case !p.VersionOK:
		return Result{"python3", Missing, "Python " + p.Version + " at " + p.Path + " is too old"}
	case !p.StdlibOK:
		return Result{"python3", Missing, "Python " + p.Version + " at " + p.Path + " is missing parts of its standard library"}
	}
	return Result{"python3", OK, "Python " + p.Version + " at " + p.Path}
}

func installCheck(in Inputs) Result {
	switch {
	case in.ResolveInstalled == nil:
		return Result{"resolve-install", Unknown, "could not look for DaVinci Resolve"}
	case !*in.ResolveInstalled:
		return Result{"resolve-install", Missing, "Resolve's scripting folder was not found"}
	case in.Python != nil && in.Python.Found && !in.Python.ResolveModule:
		return Result{"resolve-install", OK, "Resolve is installed, but Python cannot load its scripting module yet"}
	}
	return Result{"resolve-install", OK, "Resolve's scripting files are installed"}
}

func scriptingCheck(in Inputs) Result {
	switch {
	case in.ResolveRunning == nil:
		return Result{"resolve-scripting", Unknown, "could not tell whether Resolve is running"}
	case !*in.ResolveRunning:
		return Result{"resolve-scripting", Unknown, "Resolve is not running: open it and this checks itself"}
	case in.Python == nil || !in.Python.Found:
		return Result{"resolve-scripting", Unknown, "needs Python 3 first"}
	}
	switch in.Sidecar {
	case "connected":
		return Result{"resolve-scripting", OK, "connected to Resolve"}
	case "retrying":
		return Result{"resolve-scripting", Missing, "Resolve is open but the deck cannot connect: check External scripting using: Local"}
	}
	return Result{"resolve-scripting", Unknown, "connecting to Resolve"}
}

// ---- other deck apps ----------------------------------------------------------

// App is a running application.
type App struct{ ID, Name string }

type conflict struct {
	name     string
	ids      []string
	contains []string // lower case, matched against the app's name
}

// conflicts are apps known to answer a Loupedeck's MIDI. A different mapper
// that is not listed here is still a problem; the README says to quit it too.
var conflicts = []conflict{
	{"Loupedeck", []string{"com.loupedeck.loupedeckApp"}, []string{"loupedeck"}},
	{"LD-RESOLVE", []string{"com.2t5.ld-resolve"}, nil},
	{"Bome MIDI Translator", nil, []string{"midi translator", "bome"}},
	{"MidiKey2Key", nil, []string{"midikey2key"}},
}

// FindConflicts names the running apps that drive a deck. apps is the Dock
// list; isRunning is a Launch Services lookup that also sees background apps.
// It only reads.
func FindConflicts(apps []App, isRunning func(bundleID string) bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, c := range conflicts {
		hit := false
		for _, a := range apps {
			if matches(c, a) {
				hit = true
				add(a.Name)
			}
		}
		if hit || isRunning == nil {
			continue
		}
		for _, id := range c.ids {
			if isRunning(id) {
				add(c.name)
				break
			}
		}
	}
	return out
}

func matches(c conflict, a App) bool {
	for _, id := range c.ids {
		if a.ID == id {
			return true
		}
	}
	n := strings.ToLower(a.Name)
	for _, s := range c.contains {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// ---- the catalog --------------------------------------------------------------

// Entry is one row of docs/dependencies.json.
type Entry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Required      bool   `json:"required"`
	Feature       string `json:"feature"`
	HowToGet      string `json:"howToGet"`
	HowToCheck    string `json:"howToCheck"`
	FixAction     string `json:"fixAction"`
	CopyText      string `json:"copyText,omitempty"`
	Without       string `json:"without"`
	Informational bool   `json:"informational"`
	// Features tags the optional feature(s) this item switches on (resolve, finder-tags, calibration), so the editor can show "Needs X" where the feature is used.
	Features []string `json:"features,omitempty"`
}

// FixActions are the in-app buttons a dependency may offer.
var FixActions = map[string]bool{
	"open-accessibility-settings":    true,
	"open-screen-recording-settings": true,
	"open-automation-settings":       true,
	"open-software-update":           true,
	"copy-text":                      true,
}

// LoadCatalog parses and validates docs/dependencies.json.
func LoadCatalog(raw []byte) ([]Entry, error) {
	var cat []Entry
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, fmt.Errorf("dependencies.json: %w", err)
	}
	if len(cat) == 0 {
		return nil, errors.New("dependencies.json: empty")
	}
	kinds := map[string]bool{"runtime": true, "permission": true, "setting": true, "hardware": true, "os": true}
	seen := map[string]bool{}
	for i, e := range cat {
		switch {
		case e.ID == "":
			return nil, fmt.Errorf("dependencies.json: entry %d has no id", i)
		case seen[e.ID]:
			return nil, fmt.Errorf("dependencies.json: duplicate id %q", e.ID)
		case !kinds[e.Kind]:
			return nil, fmt.Errorf("dependencies.json: %s: bad kind %q", e.ID, e.Kind)
		case e.Name == "" || e.Feature == "" || e.HowToGet == "" || e.Without == "" || e.HowToCheck == "":
			return nil, fmt.Errorf("dependencies.json: %s: name, feature, howToGet, howToCheck and without are all required", e.ID)
		case e.FixAction != "" && !FixActions[e.FixAction]:
			return nil, fmt.Errorf("dependencies.json: %s: unknown fixAction %q", e.ID, e.FixAction)
		}
		seen[e.ID] = true
	}
	return cat, nil
}

// MarkdownTable renders the compact requirements table the README carries.
func MarkdownTable(cat []Entry) string {
	var b strings.Builder
	b.WriteString("| Needs | | For | How to get it |\n|---|---|---|---|\n")
	esc := func(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
	for _, e := range cat {
		need := "Optional"
		if e.Required {
			need = "**Required**"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", esc(e.Name), need, esc(e.Feature), esc(e.HowToGet))
	}
	return b.String()
}
