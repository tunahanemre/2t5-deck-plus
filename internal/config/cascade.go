package config

import "fmt"

// LayerBank returns the bank name for a page at a given layer.
//
// Layer 1 (and any out-of-range value) maps to the bare page name. That is
// what keeps layer-1 resolution byte-identical to pre-layer behaviour: the
// layer rungs of Cascade are skipped entirely rather than resolving to the
// same names by coincidence.
func LayerBank(page string, n int) string {
	if n <= 1 {
		return page
	}
	return fmt.Sprintf("%s-%d", page, n)
}

// Cascade resolves a control to a binding, walking layer banks before layer-1
// banks and the page bank before the col bank:
//
//	{page}-{layer}   (skipped when layer <= 1, or when page == "col")
//	col-{layer}      (skipped when layer <= 1)
//	{page}           (skipped when page == "col")
//	col
//
// It returns the binding, the bank it came from, and whether anything matched.
//
// The user-facing contract this implements is simply: if you mapped a control
// in the active layer it wins, and if you didn't, its layer-1 binding fires.
// That is why an unfinished layer is always usable and never leaves a control
// dead.
//
// guard, when non-nil, is applied ONLY to the page banks — never to col or
// col-{layer}. It carries resolveNoteBinding's pre-existing property/jog/key
// whitelist, which is a legacy artifact of the col/edit split rather than a
// principle: the col bank holds buttons (Copy, Paste, Undo, C1, P1-8…) that
// have no edit-bank counterpart at all, so guarding col would silently drop
// every one of them. Turn resolution passes nil — it has never had a
// whitelist.
func (c *Config) Cascade(page string, layer int, control string, guard func(Binding) bool) (Binding, string, bool) {
	try := func(bank string, applyGuard bool) (Binding, string, bool) {
		b, ok := c.Binding(bank, control)
		if !ok {
			return Binding{}, "", false
		}
		if applyGuard && guard != nil && !guard(b) {
			return Binding{}, "", false
		}
		return b, bank, true
	}

	if layer > 1 {
		if page != "col" {
			if b, bank, ok := try(LayerBank(page, layer), true); ok {
				return b, bank, true
			}
		}
		if b, bank, ok := try(LayerBank("col", layer), false); ok {
			return b, bank, true
		}
	}
	if page != "col" {
		if b, bank, ok := try(page, true); ok {
			return b, bank, true
		}
	}
	return try("col", false)
}
