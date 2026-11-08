package coalesce

import "sync"

type Accumulator struct {
	mu      sync.Mutex
	pending map[string]int
}

func New() *Accumulator { return &Accumulator{pending: map[string]int{}} }

func (a *Accumulator) Add(key string, step int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending[key] += step
}

// Drain returns all non-zero pending deltas and clears them.
func (a *Accumulator) Drain() map[string]int {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]int{}
	for k, v := range a.pending {
		if v != 0 {
			out[k] = v
		}
	}
	a.pending = map[string]int{}
	return out
}
