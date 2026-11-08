//go:build !darwin

package macapp

// App is a running GUI application.
type App struct {
	ID   string
	Name string
}

// RunningApps is empty off macOS.
func RunningApps() []App { return nil }

// AppName is empty off macOS.
func AppName(string) string { return "" }

// SelfBundleID is empty off macOS.
func SelfBundleID() string { return "" }
