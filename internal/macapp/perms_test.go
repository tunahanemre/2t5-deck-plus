package macapp

import "testing"

func TestFinderAutomationState(t *testing.T) {
	if FinderAutomation() != FinderAutomationUnknown {
		t.Fatalf("default = %q", FinderAutomation())
	}
	SetFinderAutomation(FinderAutomationGranted)
	if FinderAutomation() != "granted" {
		t.Error("granted not kept")
	}
	SetFinderAutomation(FinderAutomationDenied)
	if FinderAutomation() != "denied" {
		t.Error("denied not kept")
	}
	SetFinderAutomation("whatever")
	if FinderAutomation() != "unknown" {
		t.Errorf("bad state = %q", FinderAutomation())
	}
}

func TestReadPermissionsIsReadOnlyAndShaped(t *testing.T) {
	SetFinderAutomation("")
	p := ReadPermissions() // real, read-only TCC queries: no prompt, any answer is fine
	if p.AutomationFinder != FinderAutomationUnknown {
		t.Errorf("finder = %q", p.AutomationFinder)
	}
}
