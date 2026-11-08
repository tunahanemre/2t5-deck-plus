package editorapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

// Preflight (wave H, item 6): GET /api/preflight answers "is this Mac ready for
// the deck?" as an ordered list of checks. Every check is read-only. A check
// whose source is not wired here (the dev server without a fake, a test) is
// "unknown", never "ok".

// EnvProvider reports what the editor cannot learn from the store or the engine.
type EnvProvider interface {
	// WS01 says whether the external data drive is really mounted (device differs from /
	// and the sentinel file exists), with a short detail.
	WS01() (mounted bool, detail string)
	// LDResolveRunning says whether the old LD-RESOLVE app is running.
	LDResolveRunning() bool
}

// PreflightCheck is one line of the answer.
type PreflightCheck struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"` // ok | warn | fail | unknown
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// MountState is the read-only mount test for a volume: path must sit on another
// device than "/" and contain the sentinel file.
func MountState(path, sentinel string) (bool, string) {
	var root, vol syscall.Stat_t
	if err := syscall.Stat("/", &root); err != nil {
		return false, "cannot stat /: " + err.Error()
	}
	if err := syscall.Stat(path, &vol); err != nil {
		return false, path + " does not exist: not mounted"
	}
	if vol.Dev == root.Dev {
		return false, path + " is on the boot disk, not a mounted volume (sentinel not trusted)"
	}
	if _, err := os.Stat(filepath.Join(path, sentinel)); err != nil {
		return false, path + " is a separate device but the sentinel " + sentinel + " is missing"
	}
	return true, path + " is mounted (device differs from /, sentinel present)"
}

func (s *server) preflight(w http.ResponseWriter, r *http.Request) {
	var cs []PreflightCheck
	add := func(id, label, status, detail, fix string) {
		cs = append(cs, PreflightCheck{id, label, status, detail, fix})
	}
	// permissions
	if s.d.Perms == nil {
		for _, c := range [][2]string{{"accessibility", "Accessibility permission"}, {"screenRecording", "Screen Recording permission"}, {"automationFinder", "Automation permission for Finder"}} {
			add(c[0], c[1], "unknown", "this host cannot read permissions", "")
		}
	} else {
		p := s.d.Perms.Permissions()
		if p.Accessibility {
			add("accessibility", "Accessibility permission", "ok", "granted", "")
		} else {
			add("accessibility", "Accessibility permission", "fail", "not granted: no key, click or scroll can be sent",
				"System Settings > Privacy & Security > Accessibility: switch 2T5 DECK+ on (off and on again if it already is), then restart the app")
		}
		if p.ScreenRecording {
			add("screenRecording", "Screen Recording permission", "ok", "granted", "")
		} else {
			add("screenRecording", "Screen Recording permission", "warn", "not granted: on-screen reads (HUD placement, calibration) degrade",
				"System Settings > Privacy & Security > Screen Recording: switch 2T5 DECK+ on, then restart the app")
		}
		switch p.AutomationFinder {
		case "granted":
			add("automationFinder", "Automation permission for Finder", "ok", "granted", "")
		case "denied":
			add("automationFinder", "Automation permission for Finder", "warn", "denied: the Finder tag keys cannot read the selection",
				"System Settings > Privacy & Security > Automation: allow 2T5 DECK+ to control Finder")
		default:
			add("automationFinder", "Automation permission for Finder", "unknown", "not asked yet: it is requested the first time a Finder tag key is used", "")
		}
	}
	// data drive (only on a Mac that keeps its data on an external drive: most Macs have none)
	if a, ok := s.d.Env.(interface{ WS01Applicable() bool }); ok && !a.WS01Applicable() {
		// no row
	} else if s.d.Env == nil {
		add("ws01", "Data drive mounted", "unknown", "this host cannot check the volume", "")
	} else if ok, d := s.d.Env.WS01(); ok {
		add("ws01", "Data drive mounted", "ok", d, "")
	} else {
		add("ws01", "Data drive mounted", "fail", d, "Mount the drive that holds the app's data, then restart 2T5 DECK+")
	}
	// presets dir
	ro, pid := s.d.Store.ReadOnly()
	switch {
	case ro:
		d := "another 2T5 DECK+ is editing the presets: this one is read-only"
		if pid > 0 {
			d = fmt.Sprintf("another 2T5 DECK+ (process %d) is editing the presets: this one is read-only", pid)
		}
		add("presetsDir", "Presets folder writable and locked by this app", "fail", d, "Quit the other 2T5 DECK+ (from its app menu, never kill it), then restart this one")
	case syscall.Access(s.d.Store.Dir(), 2) != nil: // W_OK
		add("presetsDir", "Presets folder writable and locked by this app", "fail", s.d.Store.Dir()+" is not writable", "Check the volume is mounted read-write and the folder's permissions")
	case !s.d.Store.LockHeld():
		add("presetsDir", "Presets folder writable and locked by this app", "warn", "writable, but this process does not hold the folder lock (a development or library store)", "")
	default:
		add("presetsDir", "Presets folder writable and locked by this app", "ok", "writable, and this process holds the folder lock", "")
	}
	// canary
	if c := s.d.Store.Canary(); c != nil {
		n := 0
		for _, l := range c.Lost {
			n += l.Before - l.After
		}
		add("canary", "No unexplained loss of assignments", "warn", fmt.Sprintf("%d preset(s) lost assignments with no change made in the editor (%d fewer in total)", len(c.Lost), n),
			"Open the warning, restore the suggested backup or dismiss it if the change was intentional")
	} else {
		add("canary", "No unexplained loss of assignments", "ok", "every preset still has the assignments it had at the last check", "")
	}
	// engine
	if s.d.Engine == nil {
		add("engineApplied", "Engine runs what is saved", "unknown", "this host has no engine", "")
	} else {
		st := s.d.Engine.EngineState()
		last := s.seq()
		switch {
		case st.ComposeError != "":
			add("engineApplied", "Engine runs what is saved", "fail", "the engine could not take the last change: "+st.ComposeError, "Fix or undo the last change in the editor")
		case st.AppliedSeq < last:
			add("engineApplied", "Engine runs what is saved", "warn", fmt.Sprintf("the engine is still on change %d, the editor saved %d", st.AppliedSeq, last), "Wait a moment and check again")
		default:
			add("engineApplied", "Engine runs what is saved", "ok", fmt.Sprintf("the engine runs change %d", st.AppliedSeq), "")
		}
	}
	// sidecar
	if s.d.Live == nil {
		add("sidecar", "Resolve sidecar", "unknown", "this host has no engine", "")
	} else {
		switch ls := s.d.Live.Live(); ls.Sidecar {
		case "retrying":
			add("sidecar", "Resolve sidecar", "warn", "Resolve is up but the sidecar cannot connect yet (retrying)", "In Resolve: Preferences > System > General > External scripting using: Local")
		case "connected":
			add("sidecar", "Resolve sidecar", "ok", "connected", "")
		default:
			add("sidecar", "Resolve sidecar", "ok", "waiting for Resolve (it connects by itself when Resolve is in front)", "")
		}
	}
	// the old app
	if s.d.Env == nil {
		add("ldResolve", "LD-RESOLVE not running", "unknown", "this host cannot check", "")
	} else if s.d.Env.LDResolveRunning() {
		add("ldResolve", "LD-RESOLVE not running", "warn", "LD-RESOLVE is running: two apps contend for one MIDI port and would both fire", "Quit LD-RESOLVE from its app menu (never kill it: that can wedge CoreMIDI)")
	} else {
		add("ldResolve", "LD-RESOLVE not running", "ok", "not running", "")
	}
	// armed
	if s.d.Arm == nil {
		add("armed", "Arm state", "unknown", "this host has no arm switch", "")
	} else if s.d.Arm.Armed() {
		add("armed", "Arm state", "ok", "armed: the deck will fire", "")
	} else {
		add("armed", "Arm state", "ok", "disarmed: nothing fires", "")
	}
	// the disarmed state on disk
	switch {
	case s.d.ArmMark == nil:
		add("disarmedState", "Disarmed state saved", "unknown", "this host does not track it", "")
	case s.d.ArmMark.LastError() != "":
		add("disarmedState", "Disarmed state saved", "warn", "could not save the disarmed state ("+s.d.ArmMark.LastError()+"): the next launch may come up armed",
			"Make the presets folder writable (mounted read-write, enough space); the app keeps retrying")
	default:
		add("disarmedState", "Disarmed state saved", "ok", "the next launch will match the current arm state", "")
	}
	if n := len(s.d.Store.PendingExternal()); n > 0 {
		add("pendingExternal", "No outside changes waiting", "warn", fmt.Sprintf("%d outside change(s) to the presets were held back because they add risk (a program, a destructive key, a weaker guard, a lost panic key)", n),
			"Open Pending changes and approve or reject each one")
	} else {
		add("pendingExternal", "No outside changes waiting", "ok", "nothing is waiting for approval", "")
	}
	counts := map[string]int{"ok": 0, "warn": 0, "fail": 0, "unknown": 0}
	for _, c := range cs {
		counts[c.Status]++
	}
	writeJSON(w, 200, map[string]any{"ok": counts["fail"] == 0, "checks": cs, "counts": counts})
}
