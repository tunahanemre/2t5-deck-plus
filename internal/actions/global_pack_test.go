package actions

import (
	"strings"
	"testing"
)

// The Global pack: every fixed shortcut must pass the same validator the
// editor and the engine use, every action must normalise + validate as an
// assignment, and the pack is offered to every app.
func TestGlobalPack(t *testing.T) {
	r := Default()
	if !r.PackAlways("global") {
		t.Fatal("global pack must be Always")
	}
	if r.PackAlways("generic") || r.PackAlways("resolve") {
		t.Fatal("only the global pack is always-on")
	}
	n := 0
	for _, p := range r.Packs() {
		if p.ID != "global" {
			continue
		}
		for _, a := range p.Actions {
			n++
			for _, k := range []string{"key", "keyCW", "keyCCW"} {
				if v, ok := a.Fixed[k].(string); ok {
					if err := ValidateKeySpec(v); err != nil {
						t.Errorf("%s: %v", a.ID, err)
					}
				}
			}
			if a.Type == "shell" {
				cmd, _ := a.Fixed["cmd"].(string)
				if !strings.HasPrefix(cmd, "/") {
					t.Errorf("%s: shell cmd %q must be absolute", a.ID, cmd)
				}
				if _, ok := a.Fixed["args"].([]any); !ok {
					t.Errorf("%s: args must be []any for the stringList validator", a.ID)
				}
			}
			if !strings.Contains(a.Help, ". ") {
				t.Errorf("%s: help must lead with its category", a.ID)
			}
			raw, err := r.Normalize(map[string]any{"action": a.ID})
			if err != nil {
				t.Errorf("%s: normalize: %v", a.ID, err)
				continue
			}
			if err := r.Validate(raw, true); err != nil {
				t.Errorf("%s: validate: %v", a.ID, err)
			}
			if got := r.Infer(raw); got.ID != a.ID {
				t.Errorf("%s: infers as %s", a.ID, got.ID)
			}
		}
	}
	if n < 30 {
		t.Errorf("global pack has %d actions, want at least 30", n)
	}
	// Undo is Cmd+Z, Redo Shift+Cmd+Z (Logi's own).
	if u, _ := r.Get("global.undo"); u.Fixed["key"] != "Cmd+Z" {
		t.Errorf("undo = %v", u.Fixed["key"])
	}
	if u, _ := r.Get("global.redo"); u.Fixed["key"] != "Cmd+Shift+Z" {
		t.Errorf("redo = %v", u.Fixed["key"])
	}
}
