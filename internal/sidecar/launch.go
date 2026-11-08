// Package sidecar spawns and supervises the Python IPC sidecar
// (sidecar/sidecar.py) that talks to DaVinciResolveScript. It replaces the
// dev-only `scripts/run.sh` shell spawn with a Go-native launch so a future
// Wails app (which is not started from a shell) can self-launch the sidecar
// on OnStartup.
//
// Dev-compat is load-bearing: scripts/run.sh still starts sidecar.py itself
// for local development and waits for /tmp/2t5-deck-plus.sock before running
// ./2t5-deck-plus. Launch must detect that live socket and REUSE it rather than
// spawning a second sidecar process — see Launch's doc comment.
package sidecar

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"2t5-deck-plus/internal/datadir"
)

// DefaultSockPath is the fixed unix-domain-socket path the Python sidecar
// binds to (SOCK in sidecar/sidecar.py), matching scripts/run.sh.
const DefaultSockPath = "/tmp/2t5-deck-plus.sock"

// SockPath is the socket path Launch targets. A package var (not the bare
// const) so tests can redirect it to a throwaway temp socket instead of the
// real system-wide /tmp/2t5-deck-plus.sock.
var SockPath = DefaultSockPath

// liveDialTimeout bounds a single "is anything listening" probe.
var liveDialTimeout = 300 * time.Millisecond

// launchWaitTimeout bounds how long Launch waits for a freshly spawned
// sidecar to create its socket file (~15s per the phase-1 plan).
var launchWaitTimeout = 15 * time.Second

// socketPollInterval is how often Launch re-checks for the socket file while
// waiting for a freshly spawned sidecar to come up.
var socketPollInterval = 100 * time.Millisecond

// ErrPython3NotFound is returned when none of the probed candidate paths
// exist as executables at all. Distinct from an import failure — the Wails
// UI shows "install Python 3" guidance for this case.
var ErrPython3NotFound = errors.New("sidecar: no python3 interpreter found on this Mac")

// ImportFailureError is returned when at least one candidate python3
// interpreter exists and runs, but none of them can
// `import DaVinciResolveScript`. The Wails UI shows different guidance for
// this case ("check your DaVinci Resolve install / Python version") than for
// ErrPython3NotFound.
type ImportFailureError struct {
	// Python is the last candidate interpreter tried.
	Python string
	// Err is the underlying command failure (non-zero exit + combined output).
	Err error
}

func (e *ImportFailureError) Error() string {
	return fmt.Sprintf("sidecar: %s cannot import DaVinciResolveScript: %v", e.Python, e.Err)
}

func (e *ImportFailureError) Unwrap() error { return e.Err }

// candidatesFn returns the ordered list of python3 paths DiscoverPython3
// probes. A package var so tests can inject a fake list (e.g. executables
// under a temp dir) instead of real system paths.
var candidatesFn = defaultCandidates

// defaultCandidates is the real probe order:
//  1. $RESOLVE_PYTHON (explicit user/operator override, if set)
//  2. /opt/homebrew/bin/python3   (Homebrew, Apple Silicon)
//  3. /usr/local/bin/python3      (Homebrew, Intel / manual install)
//  4. framework python3.13/3.12/3.11/3.10 (python.org installer)
//  5. /usr/bin/python3            (Apple Command-Line-Tools stub — LAST:
//     invoking it on a machine without CLT installed can trigger an
//     interactive "install Command Line Tools" system prompt)
func defaultCandidates() []string {
	var c []string
	if p := os.Getenv("RESOLVE_PYTHON"); p != "" {
		c = append(c, p)
	}
	c = append(c,
		"/opt/homebrew/bin/python3",
		"/usr/local/bin/python3",
		"/Library/Frameworks/Python.framework/Versions/3.13/bin/python3",
		"/Library/Frameworks/Python.framework/Versions/3.12/bin/python3",
		"/Library/Frameworks/Python.framework/Versions/3.11/bin/python3",
		"/Library/Frameworks/Python.framework/Versions/3.10/bin/python3",
		"/usr/bin/python3",
	)
	return c
}

// verifyFn checks that a candidate python3 can import DaVinciResolveScript
// (the sidecar's hard runtime dependency). A package var so tests can fake
// verification without a real Resolve install.
var verifyFn = verifyDaVinciResolveScript

// resolveModulesDir is where DaVinci Resolve installs DaVinciResolveScript.py
// on macOS. It is NOT on Python's default path, so a bare `import
// DaVinciResolveScript` always fails — we must add this dir (the same thing the
// sidecar's resolve_driver.py does via sys.path.insert) or discovery wrongly
// rejects every python3 and the sidecar never launches.
const resolveModulesDir = "/Library/Application Support/Blackmagic Design/DaVinci Resolve/Developer/Scripting/Modules"

func verifyDaVinciResolveScript(pythonPath string) error {
	cmd := exec.Command(pythonPath, "-c", "import DaVinciResolveScript")
	cmd.Env = append(os.Environ(),
		"PYTHONPATH="+resolveModulesDir+string(os.PathListSeparator)+os.Getenv("PYTHONPATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("%w: %s", err, string(out))
		}
		return err
	}
	return nil
}

// DiscoverPython3 probes candidate python3 interpreters in a fixed order
// (see defaultCandidates) and returns the first one that both exists AND
// successfully `import DaVinciResolveScript`.
//
// Returns ErrPython3NotFound if none of the candidates exist on disk at all.
// Returns an *ImportFailureError (wrapping the last candidate's failure) if
// at least one candidate existed but every existing candidate failed the
// import check. These are distinct, typed failures because the Wails UI
// shows different remediation text for each ("install Python 3" vs "check
// your DaVinci Resolve installation").
func DiscoverPython3() (string, error) {
	candidates := candidatesFn()

	var sawExisting bool
	var lastPath string
	var lastErr error

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if fi, err := os.Stat(c); err != nil || fi.IsDir() {
			continue
		}
		// Apple's /usr/bin/python3 is a stub on a Mac without the Command Line
		// Tools: running it pops a system "install developer tools" window.
		if skipCandidate(c) {
			continue
		}
		sawExisting = true
		if err := verifyFn(c); err != nil {
			lastPath, lastErr = c, err
			continue
		}
		return c, nil
	}

	if !sawExisting {
		return "", ErrPython3NotFound
	}
	return "", &ImportFailureError{Python: lastPath, Err: lastErr}
}

// dialLiveFn probes whether something is already accepting connections on
// SockPath. A package var so tests can fake liveness without depending on
// the real /tmp/2t5-deck-plus.sock.
var dialLiveFn = func(path string) bool {
	conn, err := net.DialTimeout("unix", path, liveDialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// execCommandFn builds the *exec.Cmd used to launch the sidecar subprocess.
// A package var so tests can intercept process creation entirely (e.g.
// substitute a trivial always-exits-0 helper instead of a real python3 +
// sidecar.py).
var execCommandFn = func(pythonPath, sidecarDir string) *exec.Cmd {
	cmd := exec.Command(pythonPath, "sidecar.py")
	cmd.Dir = sidecarDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Pass the RESOLVED data root down explicitly rather than relying on
	// inheritance. resolve_driver.py computes the same default from the same
	// env var, so plain inheritance would agree today -- but only by
	// coincidence. Setting it means the Python child can never disagree with
	// the Go parent about where state/2t5-deck-plus-highlight.json lives, including
	// under a test override.
	cmd.Env = append(os.Environ(), datadir.EnvVar+"="+datadir.Root())
	return cmd
}

// waitForSocketFn polls until a connection to SockPath can actually be
// ESTABLISHED -- not merely that a file exists at that path -- for up to
// launchWaitTimeout. A package var so tests can shrink/fake the wait.
//
// 2026-08-13 fix ("dial-too-soon race" live incident): this used to just
// os.Stat the path for a socket-typed file, which is satisfied the instant
// sidecar.py's bind() runs -- BEFORE its very next line, listen(), makes
// the socket actually acceptER-ready. A caller that dialed immediately
// after Launch() returned (engine.go's recoverSidecar, via
// ipc.Client.Reconnect -- a single dial attempt with no retry of its own)
// could land in that gap and get "connection refused," burning a retry
// cycle even on a perfectly healthy, about-to-be-ready respawn. Polling via
// an actual connect attempt (dialLiveFn, the SAME primitive Launch's own
// initial liveness probe uses) closes the gap completely: by the time this
// returns success, a connection has genuinely been accepted, not just
// inferred from a filesystem entry existing.
var waitForSocketFn = waitForSocket

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if dialLiveFn(path) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sidecar: timed out after %s waiting for a live connection at %s", timeout, path)
		}
		time.Sleep(socketPollInterval)
	}
}

// Launch ensures the Python sidecar is reachable at SockPath and returns a
// stop() to tear down whatever it started, plus the socket path that was
// (re)used.
//
// Dev-compat (load-bearing): scripts/run.sh already starts sidecar.py via
// shell for local development, and waits for the socket before running
// ./2t5-deck-plus. Launch FIRST dials SockPath — if something is already
// listening there (run.sh's sidecar, or a sidecar left running from a prior
// app launch), it is REUSED and stop() is a no-op. Launch only execs a new
// python3 child process when no live socket is found. This means both
// `./scripts/run.sh` (dev) and a future .app self-launch (production) work
// without ever double-spawning the sidecar.
//
// On spawn, resourcesDir/sidecar becomes the child's working directory
// (cmd.Dir), matching sidecar.py's own relative-import expectations. For the
// CLI, resourcesDir is "." (repo root); for the bundled .app (a later task)
// it will be Contents/Resources.
func Launch(resourcesDir string) (stop func(), sockPath string, err error) {
	sockPath = SockPath

	if dialLiveFn(sockPath) {
		return func() {}, sockPath, nil
	}

	pythonPath, err := DiscoverPython3()
	if err != nil {
		return nil, "", err
	}

	sidecarDir := filepath.Join(resourcesDir, "sidecar")
	cmd := execCommandFn(pythonPath, sidecarDir)
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("sidecar: failed to start %s in %s: %w", pythonPath, sidecarDir, err)
	}

	if err := waitForSocketFn(sockPath, launchWaitTimeout); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // reap; also populates cmd.ProcessState
		return nil, "", err
	}

	stop = func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // reap; also populates cmd.ProcessState
	}
	return stop, sockPath, nil
}

// ---------------------------------------------------------------------------
// ReapOrphans (2026-08-13 fix, "duplicate sidecar feedback loop" live
// incident): a one-shot startup sweep for stray sidecar.py processes left
// behind by a PREVIOUS run.
// ---------------------------------------------------------------------------

// listSidecarPIDsFn lists every OS process whose command line matches
// "sidecar.py" (ours or a stray one from a prior run). A package var so
// tests can fake process discovery instead of shelling out to the real
// `pgrep` against the real system -- see ReapOrphans's doc comment for why
// this must NEVER run unmocked under `go test` (a matched real PID is
// genuinely killed).
var listSidecarPIDsFn = listSidecarPIDsViaPgrep

func listSidecarPIDsViaPgrep() ([]int, error) {
	return pgrepPIDs("sidecar.py")
}

// pgrepPIDs shells out to the real `pgrep -f pattern` and parses its
// output. Split out from listSidecarPIDsViaPgrep (which hardcodes the
// "sidecar.py" pattern this package actually cares about) so a test can
// exercise the REAL pgrep binary + real subprocess wiring end to end
// against a harmless, guaranteed-unique marker pattern instead of either
// mocking pgrep away entirely or risking a match against a real sidecar.py
// process on the test machine.
func pgrepPIDs(pattern string) ([]int, error) {
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil // pgrep exit code 1 == "no processes matched", not a failure
		}
		return nil, err
	}
	var pids []int
	for _, line := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(line); err == nil {
			pids = append(pids, n)
		}
	}
	return pids, nil
}

// killPIDFn kills a single PID. A package var (same rationale as
// listSidecarPIDsFn) so tests never touch a real process.
var killPIDFn = func(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// ReapOrphans kills every live "sidecar.py" process except excludePID (0 =
// exclude nothing) and returns the PIDs it actually killed. Intended to run
// EXACTLY ONCE, at real app startup, before this run's own engine.Start()
// ever calls Launch -- the only production call site is app/main.go's
// func main(), which (like hud.Arm()/mouse.Arm()/keys.Arm() before it) is
// never reached by `go test`. DO NOT call this from Launch/recoverSidecar
// or any other test-covered path -- see below.
//
// Why this exists: a bug in recoverSidecar (fixed the same day, see
// engine.go) let repeated recovery attempts pile up ADDITIONAL sidecar.py
// processes instead of replacing the previous one -- each one silently
// stealing SOCK out from under the last via sidecar.py's own then-
// unconditional os.unlink+bind (also fixed the same day, see sidecar.py's
// main()) and orphaning it: alive, still holding a DaVinci Resolve script-
// connection slot, but unreachable via the socket. That bug is fixed at
// the source now, but a crash, a force-quit, or any future bug of the same
// SHAPE could still leave orphans behind from a PREVIOUS run -- reaping
// them before this run starts its own sidecar means a stale process from
// last time never competes with this run for one of Resolve's limited
// script-connection slots.
//
// Deliberately NOT wired into Launch/recoverSidecar itself: those already
// track and kill the SPECIFIC process they spawned (see engine.go's
// recoverSidecar, "kill before respawn" step) -- a blanket sweep there
// would risk racing a process that call is in the middle of spawning, and
// wiring a real-process-killing sweep into a function `go test` regularly
// exercises (Launch is called by dozens of engine/sidecar tests) would risk
// a test run killing a developer's own live sidecar. This is a one-shot
// startup sweep for whatever was left behind by a PAST run, called from
// the one place (func main()) `go test` structurally cannot reach.
//
// excludePID lets a future caller that already knows its own child's PID
// protect it; today's only caller (app/main.go) passes 0 (nothing to
// exclude -- that run has not spawned a sidecar yet when main() calls
// this). Best-effort: a listing failure returns nil (nothing reported
// killed, nothing attempted); an individual kill failure (already gone,
// permission denied) is silently skipped rather than treated as fatal --
// the caller only logs whatever this returns.
func ReapOrphans(excludePID int) []int {
	pids, err := listSidecarPIDsFn()
	if err != nil {
		return nil
	}
	var killed []int
	for _, pid := range pids {
		if pid <= 0 || pid == excludePID {
			continue
		}
		if killPIDFn(pid) == nil {
			killed = append(killed, pid)
		}
	}
	return killed
}
