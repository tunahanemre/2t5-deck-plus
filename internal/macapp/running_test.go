package macapp

import "testing"

// TestIsAppRunning_EmptyAndAbsentBundleIDs pins the two negative cases: an
// empty string short-circuits before CGO, and a syntactically valid but
// nonexistent bundle id reads as not running rather than erroring.
func TestIsAppRunning_EmptyAndAbsentBundleIDs(t *testing.T) {
	if IsAppRunning("") {
		t.Error(`IsAppRunning("") = true, want false`)
	}
	if IsAppRunning("com.2t5.definitely-not-a-real-app-9f3a1c") {
		t.Error("IsAppRunning(<nonexistent bundle id>) = true, want false")
	}
}

// TestIsAppRunning_Finder proves the CGO path actually answers YES for
// something, not just NO for everything -- a check that always returns false
// would satisfy every other test here while being completely broken.
// Finder is running in any normal macOS login session.
func TestIsAppRunning_Finder(t *testing.T) {
	if !IsAppRunning("com.apple.finder") {
		t.Error("IsAppRunning(\"com.apple.finder\") = false, want true. " +
			"Finder should be running in any normal login session; if this fails, " +
			"the NSRunningApplication lookup itself is broken, not just Resolve detection.")
	}
}

// TestIsResolveRunning_UsesTheResolveBundleID keeps IsResolveRunning wired to
// the same constant FrontmostBundleID's helpers use. It asserts agreement, not
// a fixed value -- whether Resolve happens to be running when the suite runs is
// not something a test may depend on.
func TestIsResolveRunning_UsesTheResolveBundleID(t *testing.T) {
	if got, want := IsResolveRunning(), IsAppRunning(resolveBundleID); got != want {
		t.Errorf("IsResolveRunning() = %v, IsAppRunning(resolveBundleID) = %v — must agree", got, want)
	}
	t.Logf("(informational) DaVinci Resolve running right now: %v", IsResolveRunning())
}
