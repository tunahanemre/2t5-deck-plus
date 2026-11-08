package macapp

import "sync/atomic"

// Permission readiness (wave D, GET /api/live "permissions"). Everything here
// is a read-only query: nothing triggers a system prompt.

// Finder automation states.
const (
	FinderAutomationUnknown = "unknown"
	FinderAutomationGranted = "granted"
	FinderAutomationDenied  = "denied"
)

// Permissions is the cheap snapshot.
type Permissions struct {
	Accessibility   bool
	ScreenRecording bool
	// AutomationFinder is unknown until the app has actually sent Finder an
	// Apple Event (the only way to learn it; doing that from a poll could pop
	// the system dialog), then granted or denied: see SetFinderAutomation.
	AutomationFinder string
}

var finderAutomation atomic.Value // string

// SetFinderAutomation records the outcome of a real Finder Apple Event
// (finder.Probe at engine start). Anything but granted / denied is unknown.
func SetFinderAutomation(state string) {
	switch state {
	case FinderAutomationGranted, FinderAutomationDenied:
		finderAutomation.Store(state)
	default:
		finderAutomation.Store(FinderAutomationUnknown)
	}
}

// FinderAutomation returns the remembered Finder automation state.
func FinderAutomation() string {
	if s, ok := finderAutomation.Load().(string); ok && s != "" {
		return s
	}
	return FinderAutomationUnknown
}
