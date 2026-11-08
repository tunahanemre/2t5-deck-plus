package gate

// Allow returns true only when it is safe to post synthetic mouse drags
// to Resolve's color wheels.
//
// Conditions:
//   - resolveFrontmost: Resolve is the foreground application
//   - page: the current Resolve page (must be "color")
//
// Both conditions must hold; any other combination is suppressed.
func Allow(resolveFrontmost bool, page string) bool {
	return resolveFrontmost && page == "color"
}
