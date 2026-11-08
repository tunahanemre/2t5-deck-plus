package actions

import (
	"strings"

	"2t5-deck-plus/internal/config"
)

// Risk classes (wave D). What an assignment can do to the machine when it
// fires, so the editor can badge it and the arm switch can reason about it:
//
//	safe        nothing is posted to other apps (Resolve verbs, scroll, layers ...)
//	keys        posts key presses (key, keyDelta, a knob press that sends a key)
//	shell       runs a program
//	destructive posts a key press that closes, quits or deletes
const (
	RiskSafe        = "safe"
	RiskKeys        = "keys"
	RiskShell       = "shell"
	RiskDestructive = "destructive"
	// RiskUnknown: the runtime type is not one this editor knows, so what it
	// would execute cannot be judged. Treated as dangerous by imports.
	RiskUnknown = "unknown"
)

// KeyIsDestructive reports whether a key spec quits, closes or deletes:
// anything with Cmd and Q or W (Cmd+Q, Cmd+W, Cmd+Shift+W ...), any spec whose
// key is Delete or Forwarddelete (Delete, Cmd+Delete, Cmd+Shift+Delete), Ctrl+D
// (forward delete in text fields), Ctrl+H / Ctrl+K (backspace, kill line), Cmd+X
// (cut) and Cmd+Option+Escape (Force Quit); Cmd+Backspace / Cmd+Delete are
// caught by the key itself. Modifier
// names are case-insensitive and "command" = "cmd", "option" = "alt". Order of
// the modifiers does not matter.
func KeyIsDestructive(spec string) bool {
	parts := strings.Split(spec, "+")
	if len(parts) == 0 {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))
	var cmd, alt, ctrl bool
	for _, p := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "cmd", "command":
			cmd = true
		case "alt", "option":
			alt = true
		case "ctrl", "control":
			ctrl = true
		}
	}
	switch key {
	case "delete", "forwarddelete", "backspace":
		return true
	case "q", "w":
		return cmd
	case "d", "h", "k": // Ctrl+D forward delete, Ctrl+H backspace, Ctrl+K kill to end of line
		return ctrl
	case "x":
		return cmd // cut
	case "escape", "esc":
		return cmd && alt
	}
	return false
}

// keySpecsOf lists every key spec an assignment can send, read from the
// binding the engine would decode (never from raw map keys, whose spelling the
// engine's decoder does not care about).
func keySpecsOf(typ string, b config.Binding) []string {
	var out []string
	add := func(v string) {
		if v != "" {
			out = append(out, v)
		}
	}
	switch typ {
	case "key":
		add(b.Key)
	case "keyDelta":
		add(b.KeyCW)
		add(b.KeyCCW)
	}
	if b.Press != nil && b.Press.Type == "key" {
		add(b.Press.Key)
	}
	return out
}

// RiskOf classifies an assignment (its effective raw values) by the binding
// type it compiles to.
func RiskOf(typ string, raw map[string]any) string {
	b, err := config.DecodeBinding(raw)
	if err != nil {
		return RiskUnknown // the engine could not read it: what it would do cannot be judged
	}
	if typ == "shell" {
		return RiskShell
	}
	specs := keySpecsOf(typ, b)
	if len(specs) == 0 {
		return RiskSafe
	}
	for _, s := range specs {
		if KeyIsDestructive(s) {
			return RiskDestructive
		}
	}
	return RiskKeys
}

// RiskOfRaw is RiskOf for a raw assignment, naming its type through the registry.
//
// It classifies by the type the RUNTIME executes (EffectiveType: type, else
// method), never by the editor's "action" alias, and a type the registry does
// not know is RiskUnknown, not safe.
func (r *Registry) RiskOfRaw(raw map[string]any) string {
	t := EffectiveType(raw)
	if _, ok := r.ForType(t); !ok {
		return RiskUnknown
	}
	return RiskOf(t, raw)
}

// actionRisk is the static risk of a registry action: shell runs a program,
// key and keyDelta post keys (destructive when a fixed spec is), the rest are
// safe. generic.key / generic.keyDelta without fixed specs are "keys": the
// per-assignment risk (RiskOfRaw) is what says if the chosen shortcut is
// destructive.
func actionRisk(a Action) string {
	raw := map[string]any{}
	for k, v := range a.Fixed {
		raw[k] = v
	}
	rk := RiskOf(a.Type, raw)
	if rk == RiskSafe && (a.Type == "key" || a.Type == "keyDelta") {
		return RiskKeys
	}
	return rk
}
