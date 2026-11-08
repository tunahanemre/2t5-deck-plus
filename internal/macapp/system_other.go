//go:build !darwin

package macapp

import "errors"

// MIDISourceNames is not available off macOS.
func MIDISourceNames() ([]string, error) { return nil, errors.New("not macOS") }

// OSVersion is empty off macOS.
func OSVersion() string { return "" }
