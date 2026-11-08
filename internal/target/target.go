// Package target provides a thread-safe holder for the currently-selected
// reset target — the thing the Col button resets.
//
// Formerly internal/activewheel, which held one of exactly four primary colour
// wheels. It was generalised when Col became a universal reset: selector
// bindings now name a target, and Col looks that target up in
// _config.resetTargets to decide how to reset it (a calibrated mouse click, a
// CDL param reset, or a script-API property reset).
//
// Deliberately NO validation against a fixed set: this package cannot know
// which targets the user's config defines, and hardcoding the four wheels here
// is exactly what stopped the concept generalising before. An unknown target
// is caught at dispatch time, where the target table is in scope, and logged
// as a SKIP. Only the empty string is refused, so Get never returns "".
//
// Written from the MIDI callback goroutine (selector press actions) and read
// in the drain/tick loop; both run concurrently, so all access is mutex-guarded.
package target

import "sync"

// Default is the target a new or reset State holds. Lift is the conventional
// starting point for a grade, and was activewheel's original default.
const Default = "lift"

// State holds the current reset target, guarded by a mutex.
type State struct {
	mu  sync.Mutex
	val string
}

// New returns a State initialised to Default.
func New() *State { return &State{val: Default} }

// Set updates the active target. The empty string is ignored so Get can never
// return "". An unrecognised but non-empty name is accepted here and rejected
// at dispatch, where the target table is available.
func (s *State) Set(name string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	s.val = name
	s.mu.Unlock()
}

// Get returns the current reset target.
func (s *State) Get() string {
	s.mu.Lock()
	v := s.val
	s.mu.Unlock()
	return v
}

// Reset returns to Default. Called whenever Resolve's page changes, so a
// target selected on one page can never fire against another — the failure
// three reviewers independently flagged, where a stale "crop" target turns a
// Col press on the Color page into a silent crop mutation on a real clip.
func (s *State) Reset() { s.Set(Default) }
