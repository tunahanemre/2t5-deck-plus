package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"2t5-deck-plus/internal/cfgmerge"
	"2t5-deck-plus/internal/datadir"
)

// bundleMarker is the path fragment that identifies a running executable as
// living inside a standard macOS .app bundle
// (<name>.app/Contents/MacOS/<name>). os.Executable() returns an absolute,
// symlink-resolved path, so a simple substring check is reliable here — no
// need to walk bundle metadata.
const bundleMarker = ".app/Contents/MacOS/"

// seedConfigFiles are the config files a bundled .app seeds into the
// WS-01 data root's config/ directory on first run. Order doesn't
// matter; each is copied independently.
var seedConfigFiles = []string{"device-map.json", "bindings.json", "calibration.json"}

// mergeConfigFiles is the subset of seedConfigFiles that ALSO get an
// additive JSON merge on every startup (not just first run) once the user
// already has a copy — see seedOrMergeConfig / mergeConfigFileAdditive.
// calibration.json is deliberately excluded: it holds user-specific
// screen coordinates produced by --calibrate, not a meaningful "bundled
// default" to merge from, and touching it from the bundle would be
// actively wrong (clobbering/adding calibration points the user never
// asked for).
var mergeConfigFiles = map[string]bool{
	"device-map.json": true,
	"bindings.json":   true,
}

// executableFn resolves the running binary's absolute path. Overridden in
// tests so bundle-mode detection can be exercised without a real .app on
// disk (os.Executable() during `go test` always returns a path to a
// temporary test binary, never something matching bundleMarker).
var executableFn = os.Executable

// resolveAppDirs decides where NewApp should load engine config from
// (cfgDir) and where sidecar.Launch should look for sidecar/sidecar.py
// (resourcesDir).
//
// Bundle mode (executable path contains ".app/Contents/MacOS/", i.e. the app
// was launched as a double-clicked/installed .app — CWD is unspecified and
// must not be relied on):
//   - resourcesDir = <bundle>.app/Contents/Resources
//   - cfgDir       = datadir.Dir(datadir.Config) — i.e. <data root>/config,
//     WS-01 by default and overridable via DECKPLUS_DATA_DIR — seeded from
//     Contents/Resources/config/*.json on first run, and ADDITIVELY MERGED
//     against that same bundled default on every subsequent run — see
//     seedOrMergeConfig. A file the user already has is never clobbered;
//     only keys the user's copy is MISSING (e.g. a new bank shipped by an
//     update) get added.
//
// Dev mode (anything else — go run/go build invoked with CWD == repo root,
// including every `go test` binary): unchanged pre-Task-6 behavior,
// cfgDir="config", resourcesDir="." (both relative to the repo root) — no
// seed/merge call at all, since cfgDir and the bundled default are the
// same directory in dev mode.
func resolveAppDirs() (cfgDir, resourcesDir string, err error) {
	exe, err := executableFn()
	if err != nil {
		return "", "", fmt.Errorf("app: os.Executable: %w", err)
	}

	if !strings.Contains(exe, bundleMarker) {
		return "config", ".", nil
	}

	resourcesDir = bundleResourcesDir(exe)

	// The one place bundle-mode config location is decided. On a Mac that has
	// no WS-01 volume and never used it, datadir falls back to the per-user
	// folder (see internal/datadir). On a Mac that DID use WS-01, an unreachable
	// data root (volume not mounted) is returned as an error rather than
	// swallowed: NewApp logs it and falls back to the dev-mode literals,
	// which is the honest degradation — better than silently seeding a
	// fresh config tree onto the boot disk.
	cfgDir, err = datadir.Dir(datadir.Config)
	if err != nil {
		return "", "", fmt.Errorf("app: resolve config dir: %w", err)
	}

	// Seed/merge failure is logged, not fatal: engine.New(cfgDir) will
	// simply fail to find whichever files didn't get seeded and degrade
	// exactly like a fresh dev checkout with an empty config dir (NewApp's
	// existing a.eng == nil graceful-degradation path already handles
	// that). Running this unconditionally on every launch (not just when
	// cfgDir is freshly created) is what fixes the stale-config trap: an
	// update that ships a new top-level bank/control key must appear
	// automatically without wiping anything the user has already edited.
	if seedErr := seedOrMergeConfig(filepath.Join(resourcesDir, "config"), cfgDir); seedErr != nil {
		log.Printf("[app] seedOrMergeConfig(%q -> %q) failed: %v", resourcesDir, cfgDir, seedErr)
	}

	return cfgDir, resourcesDir, nil
}

// bundleResourcesDir derives Contents/Resources from the executable's full
// path (<bundle>.app/Contents/MacOS/<exe>) by walking up two directories
// (MacOS -> Contents) rather than string-slicing on bundleMarker — robust to
// however the bundle itself is named.
func bundleResourcesDir(exePath string) string {
	macOSDir := filepath.Dir(exePath)     // .../<bundle>.app/Contents/MacOS
	contentsDir := filepath.Dir(macOSDir) // .../<bundle>.app/Contents
	return filepath.Join(contentsDir, "Resources")
}

// seedOrMergeConfig ensures every file in seedConfigFiles exists in cfgDir,
// seeded from bundledConfigDir:
//
//   - Destination missing entirely: copied byte-for-byte from the bundled
//     default (first-run contract, unchanged from the original
//     seed-if-missing behavior). A missing bundled default (e.g.
//     calibration.json — optional even in dev mode, see engine.New's doc
//     comment) is skipped, not an error.
//   - Destination already exists AND the file is in mergeConfigFiles
//     (bindings.json, device-map.json): ADDITIVELY merged against the
//     bundled default — see mergeConfigFileAdditive. This is the fix for
//     the stale-config trap: a shipped update's new top-level bank or
//     control key (e.g. Phase 2's "edit" bank) now appears automatically
//     in an existing user file, while every key the user already has, at
//     any depth, is left completely untouched.
//   - Destination already exists AND the file is NOT in mergeConfigFiles
//     (calibration.json): left alone, exactly as before — never merged,
//     never clobbered.
//
// cfgDir is created if it doesn't exist yet. Safe to call on every app
// startup (not just first run) — the merge is additive and idempotent, so
// a run with nothing new to add is a no-op.
func seedOrMergeConfig(bundledConfigDir, cfgDir string) error {
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return fmt.Errorf("mkdir cfgDir %s: %w", cfgDir, err)
	}

	for _, name := range seedConfigFiles {
		dst := filepath.Join(cfgDir, name)
		src := filepath.Join(bundledConfigDir, name)

		_, statErr := os.Stat(dst)
		if statErr != nil {
			if !os.IsNotExist(statErr) {
				return fmt.Errorf("stat %s: %w", dst, statErr)
			}
			// First run for this file: full copy, exactly as before.
			data, readErr := os.ReadFile(src)
			if readErr != nil {
				if os.IsNotExist(readErr) {
					continue // no bundled default for this file — skip
				}
				return fmt.Errorf("read bundled %s: %w", src, readErr)
			}
			if writeErr := os.WriteFile(dst, data, 0o644); writeErr != nil {
				return fmt.Errorf("write %s: %w", dst, writeErr)
			}
			continue
		}

		// Destination already exists.
		if !mergeConfigFiles[name] {
			continue // e.g. calibration.json — never touched once present
		}
		if mergeErr := mergeConfigFileAdditive(src, dst); mergeErr != nil {
			// Non-fatal: merging is a pure enhancement, never a
			// precondition for the app to run. Worst case the user keeps
			// whatever config they already had — same as pre-FIX-2.
			log.Printf("[app] additive merge of %s failed (leaving existing file untouched): %v", dst, mergeErr)
		}
	}

	return nil
}

// mergeConfigFileAdditive additively merges the bundled default JSON
// object at src into the existing user JSON object at dst (see
// mergeMapsAdditive) and, if anything was actually added, rewrites dst
// with the merged result. A missing src (no bundled default for this
// file) is a no-op. A src or dst that fails to parse as a JSON object is
// also a no-op — merging is best-effort, never fatal, and an unparseable
// existing file must be left exactly as the user has it rather than
// risking a bad rewrite.
func mergeConfigFileAdditive(src, dst string) error {
	bundled, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read bundled %s: %w", src, err)
	}
	user, err := os.ReadFile(dst)
	if err != nil {
		return fmt.Errorf("read user %s: %w", dst, err)
	}

	var bundledMap, userMap map[string]any
	if err := json.Unmarshal(bundled, &bundledMap); err != nil {
		return fmt.Errorf("parse bundled %s: %w", src, err)
	}
	if err := json.Unmarshal(user, &userMap); err != nil {
		return fmt.Errorf("parse existing %s (leaving untouched): %w", dst, err)
	}

	if !mergeMapsAdditive(bundledMap, userMap) {
		return nil // nothing new to add
	}

	merged, err := json.MarshalIndent(userMap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged %s: %w", dst, err)
	}
	if err := os.WriteFile(dst, merged, 0o644); err != nil {
		return fmt.Errorf("write merged %s: %w", dst, err)
	}
	log.Printf("[app] merged new keys from bundled default into %s", dst)
	return nil
}

// mergeMapsAdditive recursively adds into dst any key present in src that
// dst lacks, descending into nested JSON objects present in BOTH src and
// dst under the same key (so a brand-new top-level bank like "edit" is
// added whole, while a control key added to an EXISTING bank like "col"
// is added individually without touching its siblings). A key dst already
// has — at any depth, of any type — is NEVER modified, even if the
// bundled value differs; user edits (sens/gamma tuning, calibration
// remaps, etc.) always survive. Returns whether dst was actually changed,
// so callers can skip an unnecessary rewrite.
func mergeMapsAdditive(src, dst map[string]any) bool { return cfgmerge.Additive(src, dst) }
