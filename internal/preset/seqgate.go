package preset

import "sync"

// SeqGate lets a consumer of Store snapshots refuse an older snapshot than
// one it already took: Do runs fn only when seq is newer than every seq seen
// before (serialised, so two goroutines cannot interleave their fn calls).
type SeqGate struct {
	mu   sync.Mutex
	last int
	seen bool
}

// Do runs fn when seq is newer than the last accepted one and reports whether
// it ran. A stale snapshot is dropped.
func (g *SeqGate) Do(seq int, fn func()) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen && seq <= g.last {
		return false
	}
	g.seen, g.last = true, seq
	fn()
	return true
}
