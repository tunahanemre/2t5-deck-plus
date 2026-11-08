package macapp

// ReadPermissions reads Accessibility (AXIsProcessTrusted) and Screen
// Recording (CGPreflightScreenCaptureAccess, never prompts) plus the
// remembered Finder automation state.
func ReadPermissions() Permissions {
	return Permissions{
		Accessibility:    AccessibilityTrusted(),
		ScreenRecording:  ScreenRecordingTrusted(),
		AutomationFinder: FinderAutomation(),
	}
}
