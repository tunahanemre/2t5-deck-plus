package preset

import (
	"errors"
	"fmt"
	"strings"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/config"
)

// One validator for every way a preset id can enter the store (wave E, F1/F2):
// the HTTP add-app route, AddApp, import bundles, restore, external edits found
// by the poll, files read at Open and the seeds. An id that passes ValidID but
// not this one (col, edit, default, global ...) used to be accepted everywhere
// except the HTTP route, and made ComposeConfig fail for the whole deck.

// ErrWouldNotCompose is returned by an edit that was refused because the deck
// could not build its configuration from the result. Nothing was written.
var ErrWouldNotCompose = errors.New("refused: the deck could not build its configuration from this change")

// reservedRuntimeBanks are runtime bank names the engine already owns: the
// Resolve banks' roots and the Global bank family.
var reservedRuntimeBanks = func() map[string]bool {
	m := map[string]bool{"global": true, GlobalID: true}
	for _, root := range []string{"col", "edit", "default"} {
		m[root] = true
		m[root+"-2"] = true
		m[root+"-3"] = true
		for _, mod := range modifiers {
			m[root+"-"+mod] = true
		}
		for _, pm := range config.PModes {
			m[config.ModeBankOf(root, pm)] = true
		}
	}
	return m
}()

var logicalBanksOfApps = append(append([]string{"base", "layer-2", "layer-3"}, modifiers...), "pm-hue", "pm-sat", "pm-lum")

// CheckAppID checks an id for a NEW app preset: a reverse-DNS bundle id that is
// not Global's and not a name whose runtime banks would
// collide with the engine's own.
func CheckAppID(id string) error {
	bad := func(msg string) error { return &actions.FieldError{Field: "id", Msg: msg} }
	if !ValidID(id) {
		return bad(fmt.Sprintf("%q is not a usable app id", id))
	}
	if id == GlobalID {
		return bad(fmt.Sprintf("%q is built in and cannot be added", id))
	}
	if !ValidBundleID(id) {
		return bad(fmt.Sprintf("%q is not a bundle id: use the reverse-DNS form, like com.company.App", id))
	}
	low := strings.ToLower(id)
	if reservedRuntimeBanks[low] {
		return bad(fmt.Sprintf("%q is a reserved name", id))
	}
	if id != ResolveID && strings.EqualFold(id, ResolveID) {
		return bad(fmt.Sprintf("%q differs from the DaVinci Resolve preset only in case", id))
	}
	for _, b := range logicalBanksOfApps {
		if rb := RuntimeBank(id, b); reservedRuntimeBanks[strings.ToLower(rb)] {
			return bad(fmt.Sprintf("%q would produce the runtime bank %q, which the deck already uses", id, rb))
		}
	}
	return nil
}

// CheckStoredID is CheckAppID for an id that may also be Global or Resolve:
// the check applied to every preset that arrives from a file or a bundle.
func CheckStoredID(id string) error {
	if id == GlobalID || id == ResolveID {
		return nil
	}
	return CheckAppID(id)
}

// foldConflict returns the id in ids that differs from id only in letter case
// ("" when there is none). Preset files live on a case-insensitive volume, so
// two such ids would be two presets in memory and one file on disk.
func foldConflict[V any](ids map[string]V, id string) string {
	for other := range ids {
		if other != id && strings.EqualFold(other, id) {
			return other
		}
	}
	return ""
}

// composeCheck is the dry-run of ComposeBindingsJSON: can the engine build its
// configuration from these presets?
//
// It also runs the engine's own typed decode of the composed document
// (config.CheckBindings: every assignment into config.Binding), so a value the
// engine's load would reject (a string where a number is expected) is refused
// here, before anything is written, and not discovered later as a silent
// "the engine kept the old config".
func composeCheck(ps map[string]*Preset) error {
	raw, err := newSnapshot(0, ps).ComposeBindingsJSON()
	if err != nil {
		return err
	}
	if err := config.CheckBindings(raw); err != nil {
		return fmt.Errorf("the engine could not read this configuration: %w", err)
	}
	return nil
}

// bankClash says whether adding preset id would give one of its runtime banks
// the same name as a runtime bank of an existing preset (com.foo.app-2's base
// bank is com.foo.app's layer-2 bank), and names the clash. Such a pair can be
// created empty but the first assignment fails to compose: refuse it at add time.
func bankClash(ps map[string]*Preset, id string) error {
	mine := map[string]string{} // lower-cased runtime bank -> logical bank
	for _, b := range logicalBanksOfApps {
		mine[strings.ToLower(RuntimeBank(id, b))] = b
	}
	for oid := range ps {
		if oid == ResolveID {
			continue
		}
		for _, ob := range logicalBanksOfApps {
			rb := RuntimeBank(oid, ob)
			if mb, ok := mine[strings.ToLower(rb)]; ok {
				return &actions.FieldError{Field: "id", Msg: fmt.Sprintf(
					"%q cannot be added next to %q: the deck would name two banks %q (the %s bank of %s and the %s bank of %s). Use a different bundle id",
					id, oid, rb, mb, id, ob, oid)}
			}
		}
	}
	return nil
}
