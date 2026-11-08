package target

import (
	"sync"
	"testing"
)

func TestNewDefault(t *testing.T) {
	s := New()
	if got := s.Get(); got != "lift" {
		t.Errorf("New().Get() = %q, want %q", got, "lift")
	}
}

func TestSetGet(t *testing.T) {
	s := New()
	for _, wheel := range []string{"gamma", "gain", "offset", "lift"} {
		s.Set(wheel)
		if got := s.Get(); got != wheel {
			t.Errorf("after Set(%q) Get() = %q", wheel, got)
		}
	}
}

// TestSetAcceptsArbitraryTargets is the generalisation: this package no longer
// knows the four colour wheels. Whether a target is meaningful is decided at
// dispatch, against _config.resetTargets.
func TestSetAcceptsArbitraryTargets(t *testing.T) {
	s := New()
	for _, name := range []string{"crop", "transform", "sat", "anything-else"} {
		s.Set(name)
		if got := s.Get(); got != name {
			t.Errorf("after Set(%q) Get() = %q — arbitrary targets must be accepted", name, got)
		}
	}
}

func TestSetIgnoresEmpty(t *testing.T) {
	s := New()
	s.Set("gamma")
	s.Set("")
	if got := s.Get(); got != "gamma" {
		t.Errorf("Set(\"\") changed state to %q, want gamma", got)
	}
}

func TestReset(t *testing.T) {
	s := New()
	s.Set("crop")
	s.Reset()
	if got := s.Get(); got != Default {
		t.Errorf("after Reset() Get() = %q, want %q", got, Default)
	}
}

func TestConcurrentSetGet(t *testing.T) {
	s := New()
	const goroutines = 100
	const iters = 200

	var wg sync.WaitGroup
	wheels := []string{"lift", "gamma", "gain", "offset", "crop"}
	allowed := map[string]bool{}
	for _, w := range wheels {
		allowed[w] = true
	}

	// Writers: set rapidly.
	for i := 0; i < goroutines/2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				s.Set(wheels[(i+j)%len(wheels)])
			}
		}()
	}

	// Readers: read rapidly and verify values are always valid.
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				// The package no longer owns a `valid` set; the test owns
				// the set it writes and asserts nothing else appears.
				v := s.Get()
				if !allowed[v] {
					t.Errorf("Get() returned unexpected target %q", v)
				}
			}
		}()
	}

	wg.Wait()
}
