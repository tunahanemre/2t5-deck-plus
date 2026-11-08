//go:build !darwin

package macapp

import (
	"errors"
	"image"
)

// WindowRect is one on-screen window.
type WindowRect struct {
	X, Y, W, H float64
	Title      string
}

// DisplayInfo is one active display.
type DisplayInfo struct {
	ID         string
	X, Y, W, H float64
	Scale      float64
	Name       string
}

// AppWindows is not available off macOS.
func AppWindows(string) ([]WindowRect, bool) { return nil, false }

// Displays is not available off macOS.
func Displays() []DisplayInfo { return nil }

// GrabRect is not available off macOS.
func GrabRect(x, y, w, h float64) (image.Image, error) { return nil, errors.New("not macOS") }
