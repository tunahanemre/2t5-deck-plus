package layer

import "testing"

func TestNewDefaultsToLayerOne(t *testing.T) {
	if got := New().Get(); got != 1 {
		t.Errorf("New().Get() = %d, want 1", got)
	}
}

func TestSetValidLayers(t *testing.T) {
	s := New()
	for _, n := range []int{2, 3, 1} {
		s.Set(n)
		if got := s.Get(); got != n {
			t.Errorf("after Set(%d), Get() = %d", n, got)
		}
	}
}

func TestSetIgnoresInvalid(t *testing.T) {
	s := New()
	s.Set(2)
	for _, n := range []int{0, 4, -1, 99} {
		s.Set(n)
		if got := s.Get(); got != 2 {
			t.Errorf("Set(%d) should be ignored, Get() = %d, want 2", n, got)
		}
	}
}

func TestReset(t *testing.T) {
	s := New()
	s.Set(3)
	s.Reset()
	if got := s.Get(); got != 1 {
		t.Errorf("after Reset(), Get() = %d, want 1", got)
	}
}
