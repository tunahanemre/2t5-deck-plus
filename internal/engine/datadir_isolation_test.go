package engine

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"2t5-deck-plus/internal/datadir"
)

// TestMain redirects this package's ENTIRE data root at a throwaway temp
// directory before a single test runs.
//
// This closes the coupling documented on stubStateFile in marker_test.go and
// flagged there as "pre-existing condition of the whole test suite ... not
// introduced by this feature; flagged rather than fixed suite-wide". The
// reason it could not be fixed suite-wide before is that the fix had to be
// applied per-test, and the vast majority of engine tests call Start()/Stop()
// without stubbing anything. Routing the path through datadir made a single
// package-wide override possible instead.
//
// It matters more now than it did then: stateFilePath's default moved from
// $HOME (local disk, private) to the external data root, which is SMB-shared and
// mirrored to another machine. An unisolated test run would no longer just
// clobber the user's live app state, it would generate cross-device write
// churn on every `go test`.
//
// Order note: package-level vars initialize BEFORE TestMain is called, so
// stateFilePath has already been computed from the pre-override environment
// by the time we get here and MUST be recomputed.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "2t5-deck-plus-engine-testdata-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine tests: cannot create temp data root: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv(datadir.EnvVar, tmp); err != nil {
		fmt.Fprintf(os.Stderr, "engine tests: cannot set %s: %v\n", datadir.EnvVar, err)
		os.Exit(1)
	}
	stateFilePath = datadir.Path(datadir.State, "2t5-deck-plus-state.json")

	// Lazy sidecar (2026-11-03): the supervisor asks the OS whether Resolve
	// is running/frontmost. Tests must never depend on the real desktop, so
	// the package default is "Resolve is up and in front" (the pre-lazy
	// behaviour every older test assumes); the lazy-sidecar tests inject
	// their own state. Poll and dial timings are shrunk so the supervisor's
	// first (immediate) attempt and any retry resolve in test time.
	resolveStateFn = func() (bool, bool) { return true, true }
	startWaitsForFirstSidecarAttempt = true
	sidecarSupervisorPoll = 20 * time.Millisecond
	sidecarConnectBackoffBase = 10 * time.Millisecond
	sidecarConnectBackoffCap = 40 * time.Millisecond
	sidecarDialDelay = 10 * time.Millisecond

	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// TestStateFileIsIsolatedFromProduction is a tripwire, mirroring the
// arm-guard tripwires in internal/keys and internal/mouse: it does not test a
// feature, it refuses to let the suite run in a configuration that would
// write somewhere real. If TestMain above is ever removed, reordered, or
// silently fails to take effect, this fails loudly instead of a test run
// quietly rewriting the user's live state file.
func TestStateFileIsIsolatedFromProduction(t *testing.T) {
	if stateFilePath == "" {
		t.Fatal("stateFilePath is empty — TestMain did not run or did not recompute it")
	}
	if strings.HasPrefix(stateFilePath, datadir.DefaultRoot) {
		t.Fatalf("stateFilePath = %q, which is inside the PRODUCTION data root %q. "+
			"Tests must never write there (SMB-shared, mirrored to another machine). "+
			"TestMain's %s override is not in effect.", stateFilePath, datadir.DefaultRoot, datadir.EnvVar)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(stateFilePath, home) {
		t.Fatalf("stateFilePath = %q, which is inside the user's home directory %q — "+
			"this is the old $HOME/.2t5-deck-plus-state.json coupling regressing.", stateFilePath, home)
	}
	if !strings.HasPrefix(stateFilePath, datadir.Root()) {
		t.Fatalf("stateFilePath = %q is not under the overridden data root %q", stateFilePath, datadir.Root())
	}
}

// TestSaveSentinelWritesUnderTheOverriddenRoot proves the redirect is real
// end-to-end: saveSentinel must create the state directory under the temp
// root and put the file there, with nothing left in $HOME.
func TestSaveSentinelWritesUnderTheOverriddenRoot(t *testing.T) {
	saveSentinel("", "")

	if _, err := os.Stat(stateFilePath); err != nil {
		t.Fatalf("saveSentinel() did not create %s: %v", stateFilePath, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir to check")
	}
	if _, err := os.Stat(home + "/.2t5-deck-plus-state.json"); err == nil {
		// Pre-existing file from a build BEFORE this migration is fine; what
		// must be true is that OUR write did not go there.
		t.Logf("note: a legacy %s/.2t5-deck-plus-state.json still exists on this machine (left by a pre-migration build); "+
			"this test only asserts the new write landed under %s", home, datadir.Root())
	}
}
