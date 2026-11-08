package preset

import (
	_ "embed"
	"encoding/json"
	"sort"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/config"
)

// The Resolve STANDARD slider-mode mapping: what P1-P8 do in Sat and Lum mode.
// Hue mode is the Resolve preset's ordinary col bank ("Colour balance toward a
// hue"), so it needs no entry.
//
// It ships with the Resolve STANDARD preset: a Resolve preset seeded from the
// bundled default has it. A preset seeded from a user's live ld-resolve
// bindings does NOT (their slots stay empty), and nothing ever writes it into an
// existing preset by itself: they opt in with Store.ApplyStandardModes (the
// editor's "Use the standard Sat / Lum mapping"). A preset with no per-mode
// banks behaves exactly as before.

//go:embed standard/resolve-slider-modes.json
var standardModesJSON []byte

// StandardModeBanks returns the Resolve standard mode banks (bank -> control ->
// assignment), freshly decoded each call so callers may keep them.
func StandardModeBanks() map[string]map[string]Assignment {
	var f struct {
		Banks map[string]map[string]map[string]any `json:"banks"`
	}
	if err := json.Unmarshal(standardModesJSON, &f); err != nil {
		return nil
	}
	out := map[string]map[string]Assignment{}
	for bn, bm := range f.Banks {
		out[bn] = map[string]Assignment{}
		for cn, a := range bm {
			out[bn][cn] = a
		}
	}
	return out
}

// StandardModeBankNames lists the banks of StandardModeBanks, sorted.
func StandardModeBankNames() []string {
	m := StandardModeBanks()
	n := make([]string, 0, len(m))
	for k := range m {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// withStandardModes adds the standard mode banks to a Resolve seed preset,
// leaving any bank the seed already has alone.
func withStandardModes(p *Preset) *Preset {
	if p == nil {
		return p
	}
	for bn, bm := range StandardModeBanks() {
		if _, ok := p.Banks[bn]; ok {
			continue
		}
		if p.Banks == nil {
			p.Banks = map[string]map[string]Assignment{}
		}
		p.Banks[bn] = bm
	}
	return p
}

// ApplyStandardModes copies the standard mode banks into the Resolve preset: the
// opt-in. mode is "sat", "lum" or "" for both; a bank that already exists is
// REPLACED (the caller asked for the standard), every other bank is untouched.
// One undo step. Returns the banks written.
func (s *Store) ApplyStandardModes(mode string) ([]string, error) {
	var banks []string
	std := StandardModeBanks()
	for _, bn := range StandardModeBankNames() {
		if m, ok := config.ParseModeBank(bn); ok && (mode == "" || mode == m) {
			banks = append(banks, bn)
		}
	}
	if len(banks) == 0 {
		return nil, &actions.FieldError{Field: "mode", Msg: `no standard mapping for that mode (only "sat" and "lum"; Hue mode is the ordinary Colour balance bindings)`}
	}
	err := s.commit("Use the standard slider-mode mapping", func(ps map[string]*Preset) error {
		p, err := getPreset(ps, ResolveID)
		if err != nil {
			return err
		}
		for _, bn := range banks {
			bm := map[string]Assignment{}
			for cn, a := range std[bn] {
				a2, err := s.reg.Normalize(a)
				if err != nil {
					return err
				}
				if err := s.reg.Validate(a2, true); err != nil {
					return err
				}
				bm[cn] = a2
			}
			p.Banks[bn] = bm
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return banks, nil
}
