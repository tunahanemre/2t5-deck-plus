//go:build !darwin

package macapp

// ReadPermissions reports nothing granted off macOS.
func ReadPermissions() Permissions {
	return Permissions{AutomationFinder: FinderAutomation()}
}
