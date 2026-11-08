package deps

import (
	"sync"
	"time"
)

// PythonTTL is how long the Python probe is reused. It starts a process, so it
// is not run on every poll; the editor's Recheck button asks for a fresh one.
const PythonTTL = 30 * time.Second

// Probes are the Mac-facing reads. Each one only reads: none opens a MIDI port,
// starts or stops an app, or asks macOS for a permission. A nil probe means
// "this host cannot tell", and its fact stays nil (unknown).
type Probes struct {
	OSVersion        func() (Version, bool)
	Python           func() PythonFacts
	ResolveInstalled func() bool
	ResolveRunning   func() bool
	MIDISources      func() ([]string, error)
	RunningApps      func() []App
	AppRunning       func(bundleID string) bool
}

// System collects the facts the permission and engine providers do not hold.
type System struct {
	p   Probes
	now func() time.Time

	mu       sync.Mutex
	py       *PythonFacts
	pyAt     time.Time
	os       *Version
	osLooked bool
}

// NewSystem wraps the probes.
func NewSystem(p Probes) *System { return &System{p: p, now: time.Now} }

// Facts reads the Mac. fresh forces the slow Python probe to run again.
func (s *System) Facts(fresh bool) Inputs {
	var in Inputs
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.osLooked && s.p.OSVersion != nil {
		s.osLooked = true
		if v, ok := s.p.OSVersion(); ok {
			s.os = &v
		}
	}
	in.OS = s.os

	if s.p.Python != nil {
		if fresh || s.py == nil || s.now().Sub(s.pyAt) >= PythonTTL {
			f := s.p.Python()
			s.py, s.pyAt = &f, s.now()
		}
		cp := *s.py
		in.Python = &cp
	}
	if s.p.ResolveInstalled != nil {
		b := s.p.ResolveInstalled()
		in.ResolveInstalled = &b
	}
	if s.p.ResolveRunning != nil {
		b := s.p.ResolveRunning()
		in.ResolveRunning = &b
	}
	if s.p.MIDISources != nil {
		names, err := s.p.MIDISources()
		m := &MIDIFacts{Sources: names}
		if err != nil {
			m.Err = err.Error()
		}
		in.MIDI = m
	}
	if s.p.RunningApps != nil {
		names := FindConflicts(s.p.RunningApps(), s.p.AppRunning)
		if names == nil {
			names = []string{}
		}
		in.OtherApps = &names
	}
	return in
}
