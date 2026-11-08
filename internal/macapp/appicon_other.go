//go:build !darwin

package macapp

// AppIconPNG is nil off macOS.
func AppIconPNG(string) []byte { return nil }
