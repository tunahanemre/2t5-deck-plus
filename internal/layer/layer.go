// Package layer holds the console's active layer (1, 2 or 3).
//
// Shaped as a shared-pointer object rather than a closure, mirroring
// internal/target, for two reasons:
//
//   - dispatchNoteOn can only mutate state that is passed to it as a shared
//     pointer. That is precisely why the existing hard-wired "Col" case lives
//     OUTSIDE dispatchNoteOn -- it needed to write closure-scoped bank state.
//   - The seven direct MIDI-callback intercepts (isAddMarkerControl,
//     isSelectClipVert, and friends) also need to read the active layer, and
//     they are ordinary functions, not closures over Start's scope.
//
// Set clamps to [Min, Max] and silently ignores anything else, so a malformed
// bindings.json can never strand the console on a layer that has no bank.
package layer

import "sync"

// Min and Max bound the valid layer range.
const (
	Min = 1
	Max = 3
)

// State is the active layer. The zero value is not usable; call New.
type State struct {
	mu sync.Mutex
	n  int
}

// New returns a State on layer Min.
func New() *State { return &State{n: Min} }

// Set changes the active layer. Values outside [Min, Max] are ignored.
func (s *State) Set(n int) {
	if n < Min || n > Max {
		return
	}
	s.mu.Lock()
	s.n = n
	s.mu.Unlock()
}

// Get returns the active layer.
func (s *State) Get() int {
	s.mu.Lock()
	n := s.n
	s.mu.Unlock()
	return n
}

// Reset returns to layer Min. Called whenever Resolve's page changes, so the
// user can never be surprised by a stale layer after switching pages.
func (s *State) Reset() { s.Set(Min) }
