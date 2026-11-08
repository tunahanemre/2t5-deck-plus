package calib

import (
	"encoding/json"
	"os"
	"sync/atomic"
)

// Point is a calibrated screen coordinate with axis direction.
type Point struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Axis string  `json:"axis"` // "v" (vertical drag), "h" (horizontal drag) or "click"
}

type file struct {
	Points map[string]Point `json:"points"`
}

// Resolver turns a calibration key into the point to use NOW (the mouse
// calibration service, internal/mousecal: window-relative, checked against the
// screen). ok=false means "do not click": the key is not calibrated for the
// current window / layout. The resolver owns that decision and its reporting.
type Resolver func(key string) (x, y float64, axis string, ok bool)

// Calib holds the calibrated screen points for each wheel.
type Calib struct {
	pts      map[string]Point
	resolver atomic.Pointer[Resolver]
}

// Empty returns a Calib with no file points (a resolver can still serve it).
func Empty() *Calib { return &Calib{pts: map[string]Point{}} }

// Load reads a calibration.json file and returns a Calib.
func Load(path string) (*Calib, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return &Calib{pts: f.Points}, nil
}

// SetResolver installs (or, with nil, removes) the resolver. While set, Point
// asks it and never reads the file's absolute points directly: the resolver may
// hand them back (Legacy) while a target has not been calibrated the new way.
func (c *Calib) SetResolver(r Resolver) {
	if r == nil {
		c.resolver.Store(nil)
		return
	}
	c.resolver.Store(&r)
}

// Point returns the calibration point for the given key (e.g. "lift_master").
// Returns false if the key is not present, or, with a resolver, not usable now.
func (c *Calib) Point(key string) (Point, bool) {
	if r := c.resolver.Load(); r != nil {
		x, y, axis, ok := (*r)(key)
		return Point{X: x, Y: y, Axis: axis}, ok
	}
	p, ok := c.pts[key]
	return p, ok
}

// Legacy returns the absolute point stored in calibration.json for key,
// bypassing the resolver (the old --calibrate output).
func (c *Calib) Legacy(key string) (Point, bool) {
	if c == nil {
		return Point{}, false
	}
	p, ok := c.pts[key]
	return p, ok
}

// Points returns a copy of every point of the loaded file (nil = none).
func (c *Calib) Points() map[string]Point {
	if c == nil || len(c.pts) == 0 {
		return nil
	}
	out := make(map[string]Point, len(c.pts))
	for k, p := range c.pts {
		out[k] = p
	}
	return out
}
