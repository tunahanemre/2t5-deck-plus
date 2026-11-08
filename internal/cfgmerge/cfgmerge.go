// Package cfgmerge is the additive merge of a bundled default config into the
// user's copy (used by the app at every start): keys the user lacks are added,
// keys the user has are never touched. The engine's JSON decoder matches field
// names case-insensitively, so "has the key" must too: a legacy "Sens" in the
// user's file plus the bundled "sens" would otherwise end up as BOTH spellings.
package cfgmerge

import "strings"

// Additive adds into dst every key of src that dst lacks, recursing into objects
// present in both. A dst key matches a src key when they are equal ignoring case
// (an exact match is preferred). It never modifies a value dst already has and
// reports whether dst changed.
func Additive(src, dst map[string]any) bool {
	changed := false
	for k, srcVal := range src {
		dk, exists := match(dst, k)
		if !exists {
			dst[k] = srcVal
			changed = true
			continue
		}
		srcObj, srcIsObj := srcVal.(map[string]any)
		dstObj, dstIsObj := dst[dk].(map[string]any)
		if srcIsObj && dstIsObj && Additive(srcObj, dstObj) {
			changed = true
		}
	}
	return changed
}

func match(m map[string]any, k string) (string, bool) {
	if _, ok := m[k]; ok {
		return k, true
	}
	for dk := range m {
		if strings.EqualFold(dk, k) {
			return dk, true
		}
	}
	return "", false
}
