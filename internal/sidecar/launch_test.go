package sidecar

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// DiscoverPython3
// ---------------------------------------------------------------------------

// fakeExecutable creates an empty-but-executable file at dir/name and returns
// its path. Used to fake "a python3 binary exists here" without needing a
// real interpreter.
func fakeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("fakeExecutable: %v", err)
	}
	return p
}

func withCandidates(t *testing.T, paths []string) {
	t.Helper()
	orig := candidatesFn
	candidatesFn = func() []string { return paths }
	t.Cleanup(func() { candidatesFn = orig })
}

func withVerify(t *testing.T, fn func(string) error) {
	t.Helper()
	orig := verifyFn
	verifyFn = fn
	t.Cleanup(func() { verifyFn = orig })
}

func TestDiscoverPython3_NoCandidatesExist_ReturnsNotFound(t *testing.T) {
	dir := t.TempDir()
	withCandidates(t, []string{
		filepath.Join(dir, "no-such-python3"),
		filepath.Join(dir, "also-missing"),
	})

	_, err := DiscoverPython3()
	if !errors.Is(err, ErrPython3NotFound) {
		t.Fatalf("DiscoverPython3() err = %v, want ErrPython3NotFound", err)
	}
}

func TestDiscoverPython3_ExistsButImportFails_ReturnsImportFailureError(t *testing.T) {
	dir := t.TempDir()
	py := fakeExecutable(t, dir, "python3")
	withCandidates(t, []string{py})
	withVerify(t, func(string) error { return errors.New("ModuleNotFoundError: No module named 'DaVinciResolveScript'") })

	_, err := DiscoverPython3()
	var impErr *ImportFailureError
	if !errors.As(err, &impErr) {
		t.Fatalf("DiscoverPython3() err = %v (%T), want *ImportFailureError", err, err)
	}
	if impErr.Python != py {
		t.Errorf("ImportFailureError.Python = %q, want %q", impErr.Python, py)
	}
}

func TestDiscoverPython3_FirstWorkingCandidateWins(t *testing.T) {
	dir := t.TempDir()
	bad := fakeExecutable(t, dir, "python3-bad")
	good := fakeExecutable(t, dir, "python3-good")
	withCandidates(t, []string{bad, good})
	withVerify(t, func(p string) error {
		if p == good {
			return nil
		}
		return errors.New("import fails")
	})

	got, err := DiscoverPython3()
	if err != nil {
		t.Fatalf("DiscoverPython3() err = %v, want nil", err)
	}
	if got != good {
		t.Errorf("DiscoverPython3() = %q, want %q", got, good)
	}
}

func TestDiscoverPython3_SkipsMissingCandidatesInList(t *testing.T) {
	dir := t.TempDir()
	good := fakeExecutable(t, dir, "python3")
	withCandidates(t, []string{
		filepath.Join(dir, "missing-1"),
		filepath.Join(dir, "missing-2"),
		good,
	})
	withVerify(t, func(string) error { return nil })

	got, err := DiscoverPython3()
	if err != nil {
		t.Fatalf("DiscoverPython3() err = %v, want nil", err)
	}
	if got != good {
		t.Errorf("DiscoverPython3() = %q, want %q", got, good)
	}
}

func TestDiscoverPython3_RespectsProbeOrder_FirstExistingWins(t *testing.T) {
	// Even though both exist and both verify OK, the FIRST candidate in probe
	// order must win — this stands in for "$RESOLVE_PYTHON before
	// /opt/homebrew, framework pythons before /usr/bin/python3".
	dir := t.TempDir()
	first := fakeExecutable(t, dir, "first")
	second := fakeExecutable(t, dir, "second")
	withCandidates(t, []string{first, second})
	withVerify(t, func(string) error { return nil })

	got, err := DiscoverPython3()
	if err != nil {
		t.Fatalf("DiscoverPython3() err = %v, want nil", err)
	}
	if got != first {
		t.Errorf("DiscoverPython3() = %q, want %q (probe-order priority)", got, first)
	}
}

func TestDiscoverPython3_EmptyCandidateEntriesSkipped(t *testing.T) {
	dir := t.TempDir()
	good := fakeExecutable(t, dir, "python3")
	// Simulates $RESOLVE_PYTHON being unset (empty string in the slice).
	withCandidates(t, []string{"", good})
	withVerify(t, func(string) error { return nil })

	got, err := DiscoverPython3()
	if err != nil {
		t.Fatalf("DiscoverPython3() err = %v, want nil", err)
	}
	if got != good {
		t.Errorf("DiscoverPython3() = %q, want %q", got, good)
	}
}

func TestDefaultCandidates_UsesResolvePythonEnvFirst_AppleStubLast(t *testing.T) {
	t.Setenv("RESOLVE_PYTHON", "/custom/python3")
	c := defaultCandidates()
	if len(c) == 0 || c[0] != "/custom/python3" {
		t.Fatalf("defaultCandidates()[0] = %v, want /custom/python3 ($RESOLVE_PYTHON first)", c)
	}
	if c[len(c)-1] != "/usr/bin/python3" {
		t.Errorf("defaultCandidates() last entry = %q, want /usr/bin/python3 (Apple stub last)", c[len(c)-1])
	}
}

func TestDefaultCandidates_NoEnv_SkipsEmptyFirstEntry(t *testing.T) {
	t.Setenv("RESOLVE_PYTHON", "")
	c := defaultCandidates()
	if len(c) == 0 || c[0] == "" {
		t.Fatalf("defaultCandidates()[0] = %q, want a non-empty real path when $RESOLVE_PYTHON is unset", c[0])
	}
	if c[len(c)-1] != "/usr/bin/python3" {
		t.Errorf("defaultCandidates() last entry = %q, want /usr/bin/python3 (Apple stub last)", c[len(c)-1])
	}
}

// ---------------------------------------------------------------------------
// Launch — socket-already-live reuse path (dev-compat)
// ---------------------------------------------------------------------------

// tempSockPath returns a socket path directly under os.TempDir() (NOT
// t.TempDir(), whose nested subtest-name path can exceed macOS's ~104-byte
// sun_path limit and fail bind() with EINVAL).
func tempSockPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-sidecar-launch-*.sock")
	if err != nil {
		t.Fatalf("tempSockPath: %v", err)
	}
	p := f.Name()
	f.Close()
	os.Remove(p) // net.Listen requires the path not to pre-exist
	t.Cleanup(func() { os.Remove(p) })
	return p
}

func withSockPath(t *testing.T, path string) {
	t.Helper()
	orig := SockPath
	SockPath = path
	t.Cleanup(func() { SockPath = orig })
}

func TestLaunch_SocketAlreadyLive_ReusesNoSpawn(t *testing.T) {
	sock := tempSockPath(t)
	withSockPath(t, sock)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	spawnCalled := false
	origExec := execCommandFn
	execCommandFn = func(pythonPath, sidecarDir string) *exec.Cmd {
		spawnCalled = true
		return origExec(pythonPath, sidecarDir)
	}
	t.Cleanup(func() { execCommandFn = origExec })

	stop, gotSock, err := Launch(".")
	if err != nil {
		t.Fatalf("Launch() err = %v, want nil (live socket should be reused)", err)
	}
	if gotSock != sock {
		t.Errorf("Launch() sockPath = %q, want %q", gotSock, sock)
	}
	if spawnCalled {
		t.Error("Launch() spawned a new process even though the socket was already live — dev-compat violated")
	}
	if stop == nil {
		t.Fatal("Launch() stop = nil, want a (no-op) func")
	}
	stop() // must not panic — no-op for a reused socket
}

// ---------------------------------------------------------------------------
// Launch — no live socket: spawn path
// ---------------------------------------------------------------------------

func TestLaunch_NoLiveSocket_PythonNotFound_ReturnsError(t *testing.T) {
	sock := tempSockPath(t)
	withSockPath(t, sock)
	// Nothing listens on sock — dialLiveFn will report not-live.

	dir := t.TempDir()
	withCandidates(t, []string{filepath.Join(dir, "no-python-here")})

	stop, gotSock, err := Launch(".")
	if err == nil {
		t.Fatal("Launch() err = nil, want ErrPython3NotFound-wrapping error")
	}
	if !errors.Is(err, ErrPython3NotFound) {
		t.Errorf("Launch() err = %v, want ErrPython3NotFound", err)
	}
	if stop != nil {
		t.Error("Launch() stop != nil on failure, want nil")
	}
	if gotSock != "" {
		t.Errorf("Launch() sockPath = %q on failure, want empty", gotSock)
	}
}

func TestLaunch_NoLiveSocket_SpawnsAndWaitsThenStopKills(t *testing.T) {
	sock := tempSockPath(t)
	withSockPath(t, sock)

	dir := t.TempDir()
	good := fakeExecutable(t, dir, "python3")
	withCandidates(t, []string{good})
	withVerify(t, func(string) error { return nil })

	// Fake the actual spawn: run a real, harmless long-lived process (sleep)
	// instead of python3 sidecar.py — Launch doesn't care what the process
	// is, only that Start()/Kill() work. To mimic a spawned sidecar taking a
	// brief moment to bind its socket (and to prove Launch's initial
	// liveness probe correctly sees "not live yet" and takes the spawn
	// path), the fake socket is created asynchronously, shortly AFTER
	// execCommandFn is invoked.
	origExec := execCommandFn
	var spawnedCmd *exec.Cmd
	lnCh := make(chan net.Listener, 1)
	execCommandFn = func(pythonPath, sidecarDir string) *exec.Cmd {
		spawnedCmd = exec.Command("sleep", "30")
		go func() {
			time.Sleep(20 * time.Millisecond)
			ln, err := net.Listen("unix", sock)
			if err != nil {
				close(lnCh)
				return
			}
			lnCh <- ln
		}()
		return spawnedCmd
	}
	t.Cleanup(func() { execCommandFn = origExec })

	// Exercise the REAL waitForSocketFn (default) against a fast poll
	// interval so the test doesn't wait out the full 15s production timeout.
	origPoll := socketPollInterval
	socketPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { socketPollInterval = origPoll })

	stop, gotSock, err := Launch(".")
	if ln, ok := <-lnCh; ok && ln != nil {
		defer ln.Close()
	}
	if err != nil {
		t.Fatalf("Launch() err = %v, want nil", err)
	}
	if gotSock != sock {
		t.Errorf("Launch() sockPath = %q, want %q", gotSock, sock)
	}
	if spawnedCmd == nil {
		t.Fatal("execCommandFn was never invoked — Launch should have spawned when no live socket existed at call time")
	}
	if stop == nil {
		t.Fatal("Launch() stop = nil, want a func that kills the spawned process")
	}

	stop()
	// spawnedCmd.Wait() was already called inside stop(); a second Wait call
	// is invalid, so assert exit via ProcessState instead.
	if spawnedCmd.ProcessState == nil {
		t.Error("stop() did not reap the spawned process (ProcessState is nil)")
	} else if spawnedCmd.ProcessState.Success() {
		t.Error("expected the spawned sleep process to be killed (non-success exit), got success")
	}
}

// ---------------------------------------------------------------------------
// waitForSocket — real polling behaviour, no dependency on Launch
// ---------------------------------------------------------------------------

func TestWaitForSocket_TimesOutWhenNeverCreated(t *testing.T) {
	sock := tempSockPath(t)
	err := waitForSocket(sock, 150*time.Millisecond)
	if err == nil {
		t.Fatal("waitForSocket() err = nil, want timeout error")
	}
}

func TestWaitForSocket_SucceedsOnceSocketExists(t *testing.T) {
	sock := tempSockPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	if err := waitForSocket(sock, 2*time.Second); err != nil {
		t.Fatalf("waitForSocket() err = %v, want nil", err)
	}
}

func TestWaitForSocket_IgnoresRegularFileAtPath(t *testing.T) {
	// A plain file at the target path (not a real socket) must NOT satisfy
	// the wait — mirrors run.sh's `[ -S ... ]` type check.
	dir := t.TempDir()
	p := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(p, []byte("junk"), 0644); err != nil {
		t.Fatal(err)
	}
	err := waitForSocket(p, 150*time.Millisecond)
	if err == nil {
		t.Fatal("waitForSocket() err = nil for a regular file, want timeout error")
	}
}

// TestWaitForSocket_BoundButNotListening_DoesNotSatisfyWait is the
// "dial-too-soon race" regression (2026-08-13 fix): a socket FILE existing
// (satisfied the moment sidecar.py's bind() runs) must NOT be enough --
// waitForSocket must keep polling until a connection can actually be
// ESTABLISHED, which only happens once listen() has also run. Before this
// fix, waitForSocket used os.Stat + ModeSocket, which is satisfied by
// bind() alone; a caller dialing immediately afterward (engine.go's
// recoverSidecar, via a single-shot ipc.Client.Reconnect) could land in
// that gap and get "connection refused" even though the process was about
// to become ready a moment later.
func TestWaitForSocket_BoundButNotListening_DoesNotSatisfyWait(t *testing.T) {
	sock := tempSockPath(t)

	// Raw bind() WITHOUT listen() -- net.ListenUnix always calls listen(2)
	// internally, so reproducing the exact gap between sidecar.py main()'s
	// srv.bind(SOCK) and srv.listen(1) needs the raw syscalls directly.
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("syscall.Socket: %v", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: sock}); err != nil {
		t.Fatalf("syscall.Bind: %v", err)
	}
	// Deliberately never call syscall.Listen.

	err = waitForSocket(sock, 150*time.Millisecond)
	if err == nil {
		t.Fatal("waitForSocket() err = nil for a bound-but-not-listening socket, want timeout error")
	}
}

// ---------------------------------------------------------------------------
// ReapOrphans / pgrepPIDs (2026-08-13 fix)
// ---------------------------------------------------------------------------

func withPIDSeams(t *testing.T, list func() ([]int, error), kill func(int) error) {
	t.Helper()
	origList := listSidecarPIDsFn
	origKill := killPIDFn
	t.Cleanup(func() {
		listSidecarPIDsFn = origList
		killPIDFn = origKill
	})
	if list != nil {
		listSidecarPIDsFn = list
	}
	if kill != nil {
		killPIDFn = kill
	}
}

func TestReapOrphans_KillsListedPIDsExceptExcluded(t *testing.T) {
	var killedPIDs []int
	withPIDSeams(t,
		func() ([]int, error) { return []int{111, 222, 333}, nil },
		func(pid int) error { killedPIDs = append(killedPIDs, pid); return nil },
	)

	got := ReapOrphans(222)
	sort.Ints(got)
	sort.Ints(killedPIDs)
	want := []int{111, 333}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReapOrphans(222) = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(killedPIDs, want) {
		t.Errorf("killed PIDs = %v, want %v -- the excluded PID must never be killed", killedPIDs, want)
	}
}

func TestReapOrphans_ExcludeZero_KillsEverything(t *testing.T) {
	withPIDSeams(t,
		func() ([]int, error) { return []int{111, 222}, nil },
		func(pid int) error { return nil },
	)

	got := ReapOrphans(0)
	sort.Ints(got)
	want := []int{111, 222}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReapOrphans(0) = %v, want %v (excludePID=0 excludes nothing)", got, want)
	}
}

func TestReapOrphans_ListingFailure_ReturnsNilNotPanic(t *testing.T) {
	withPIDSeams(t,
		func() ([]int, error) { return nil, errors.New("pgrep failed") },
		nil,
	)

	got := ReapOrphans(0)
	if got != nil {
		t.Errorf("ReapOrphans() = %v, want nil on a listing failure", got)
	}
}

func TestReapOrphans_KillFailure_OmittedFromKilledList(t *testing.T) {
	withPIDSeams(t,
		func() ([]int, error) { return []int{111, 222}, nil },
		func(pid int) error {
			if pid == 222 {
				return errors.New("already gone")
			}
			return nil
		},
	)

	got := ReapOrphans(0)
	if !reflect.DeepEqual(got, []int{111}) {
		t.Errorf("ReapOrphans() = %v, want [111] -- 222's kill failed and must not be reported as killed", got)
	}
}

// TestPgrepPIDs_RealPgrep_NoMatchReturnsEmptyNotError exercises the REAL
// pgrep binary (not mocked) against a pattern virtually guaranteed to match
// no process on the test machine -- proves pgrep's exit-code-1 "no
// processes matched" convention is treated as success (empty list, nil
// error), not a Go error, without ever risking a real match.
func TestPgrepPIDs_RealPgrep_NoMatchReturnsEmptyNotError(t *testing.T) {
	pids, err := pgrepPIDs("zzz-2t5-deck-plus-test-pattern-that-will-never-match-anything-9f3a1c")
	if err != nil {
		t.Fatalf("pgrepPIDs: %v", err)
	}
	if len(pids) != 0 {
		t.Errorf("pgrepPIDs() = %v, want empty", pids)
	}
}

// TestPgrepPIDs_RealPgrep_FindsASpawnedMarkerProcess exercises the REAL
// pgrep binary end to end against a real, deliberately spawned throwaway
// process (killed at the end of the test) carrying a unique marker string
// in its command line -- proves the actual subprocess + pgrep wiring
// works, not just the parsing logic.
func TestPgrepPIDs_RealPgrep_FindsASpawnedMarkerProcess(t *testing.T) {
	// The marker IS the sleep duration itself (a virtually-impossible-to-
	// collide fractional value) rather than a shell comment appended after
	// the command -- `sh -c "sleep 30 # marker"` looked appealing but a
	// shell strips `#...` as a comment BEFORE exec, so the marker never
	// actually reaches the process's real argv that ps/pgrep -f see.
	marker := "9137.246813"
	cmd := exec.Command("sleep", marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn marker process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// pgrep needs a moment to see a just-started process on a loaded box;
	// poll briefly rather than a fixed sleep.
	deadline := time.Now().Add(2 * time.Second)
	var pids []int
	var err error
	for time.Now().Before(deadline) {
		pids, err = pgrepPIDs(marker)
		if err == nil && len(pids) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("pgrepPIDs: %v", err)
	}
	found := false
	for _, p := range pids {
		if p == cmd.Process.Pid {
			found = true
		}
	}
	if !found {
		t.Errorf("pgrepPIDs(%q) = %v, want to include the spawned PID %d", marker, pids, cmd.Process.Pid)
	}
}
