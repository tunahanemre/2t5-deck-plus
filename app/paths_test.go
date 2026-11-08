package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"2t5-deck-plus/internal/datadir"
)

// --- seedOrMergeConfig ----------------------------------------------

func TestSeedOrMergeConfig_CopiesAllMissingFiles(t *testing.T) {
	bundled := t.TempDir()
	cfgDir := filepath.Join(t.TempDir(), "2t5-deck-plus") // doesn't exist yet — must be created

	for _, name := range seedConfigFiles {
		writeFile(t, filepath.Join(bundled, name), "bundled:"+name)
	}

	if err := seedOrMergeConfig(bundled, cfgDir); err != nil {
		t.Fatalf("seedOrMergeConfig() error = %v, want nil", err)
	}

	for _, name := range seedConfigFiles {
		got := readFile(t, filepath.Join(cfgDir, name))
		want := "bundled:" + name
		if got != want {
			t.Errorf("seeded %s = %q, want %q", name, got, want)
		}
	}
}

func TestSeedOrMergeConfig_MissingBundledDefault_SkippedNotError(t *testing.T) {
	bundled := t.TempDir() // empty — no bundled defaults at all
	cfgDir := t.TempDir()

	if err := seedOrMergeConfig(bundled, cfgDir); err != nil {
		t.Fatalf("seedOrMergeConfig() with no bundled defaults error = %v, want nil", err)
	}
	for _, name := range seedConfigFiles {
		if _, err := os.Stat(filepath.Join(cfgDir, name)); !os.IsNotExist(err) {
			t.Errorf("seedOrMergeConfig() unexpectedly created %s from a nonexistent bundled default", name)
		}
	}
}

func TestSeedOrMergeConfig_CreatesCfgDirIfMissing(t *testing.T) {
	bundled := t.TempDir()
	cfgDir := filepath.Join(t.TempDir(), "nested", "2t5-deck-plus")

	if err := seedOrMergeConfig(bundled, cfgDir); err != nil {
		t.Fatalf("seedOrMergeConfig() error = %v, want nil", err)
	}
	if info, err := os.Stat(cfgDir); err != nil || !info.IsDir() {
		t.Fatalf("seedOrMergeConfig() did not create cfgDir %s", cfgDir)
	}
}

// calibration.json is deliberately excluded from the additive-merge
// contract (see mergeConfigFiles) — it holds user-specific screen
// coordinates produced by --calibrate, not a meaningful "bundled default"
// to merge from. An existing calibration.json must NEVER be touched, even
// if the bundled copy differs (or isn't even valid JSON).
func TestSeedOrMergeConfig_CalibrationNeverTouchedIfAlreadyExists(t *testing.T) {
	bundled := t.TempDir()
	cfgDir := t.TempDir()
	writeFile(t, filepath.Join(bundled, "calibration.json"), `{"points":{"new_point":{"x":1,"y":2,"axis":"h"}}}`)
	writeFile(t, filepath.Join(cfgDir, "calibration.json"), "user-calibration-content")

	if err := seedOrMergeConfig(bundled, cfgDir); err != nil {
		t.Fatalf("seedOrMergeConfig() error = %v, want nil", err)
	}
	if got := readFile(t, filepath.Join(cfgDir, "calibration.json")); got != "user-calibration-content" {
		t.Fatalf("calibration.json must never be touched once it exists: got %q", got)
	}
}

// TestSeedOrMergeConfig_BindingsJSON_AddsNewBankWithoutClobberingExisting
// is the core FIX-2 scenario: the exact stale-config trap that shipped a
// new "edit" bank while the user's app-support bindings.json (from a
// prior install) had none. The new bank must appear; the user's existing
// "col" bank values (already customized) must survive byte-for-byte.
func TestSeedOrMergeConfig_BindingsJSON_AddsNewBankWithoutClobberingExisting(t *testing.T) {
	bundled := t.TempDir()
	cfgDir := t.TempDir()

	writeFile(t, filepath.Join(bundled, "bindings.json"), `{
		"_config": {"coalesceWindowMs": 999},
		"col": {"Exposure": {"type": "wheel", "wheel": "lift_master", "sens": 15}},
		"edit": {"ControlDial": {"type": "jog", "maxDelta": 30}}
	}`)
	writeFile(t, filepath.Join(cfgDir, "bindings.json"), `{
		"_config": {"coalesceWindowMs": 120},
		"col": {"Exposure": {"type": "wheel", "wheel": "lift_master", "sens": 42}}
	}`)

	if err := seedOrMergeConfig(bundled, cfgDir); err != nil {
		t.Fatalf("seedOrMergeConfig() error = %v, want nil", err)
	}

	merged := readMergedJSON(t, filepath.Join(cfgDir, "bindings.json"))

	col := merged["col"].(map[string]any)
	exposure := col["Exposure"].(map[string]any)
	if sens := exposure["sens"].(float64); sens != 42 {
		t.Errorf("col.Exposure.sens = %v, want 42 (user's existing value must survive)", sens)
	}
	cfgBlock := merged["_config"].(map[string]any)
	if ms := cfgBlock["coalesceWindowMs"].(float64); ms != 120 {
		t.Errorf("_config.coalesceWindowMs = %v, want 120 (user's existing value must survive)", ms)
	}
	editBank, ok := merged["edit"].(map[string]any)
	if !ok {
		t.Fatalf("merged bindings.json missing the new \"edit\" bank entirely: %v", merged)
	}
	if _, ok := editBank["ControlDial"]; !ok {
		t.Errorf("merged edit bank missing ControlDial: %v", editBank)
	}
}

func TestSeedOrMergeConfig_BindingsJSON_AddsNewControlKeyWithinExistingBank(t *testing.T) {
	bundled := t.TempDir()
	cfgDir := t.TempDir()

	writeFile(t, filepath.Join(bundled, "bindings.json"), `{
		"col": {
			"Exposure":  {"type": "wheel", "wheel": "lift_master", "sens": 15},
			"NewButton": {"type": "key", "key": "Cmd+K"}
		}
	}`)
	writeFile(t, filepath.Join(cfgDir, "bindings.json"), `{
		"col": {"Exposure": {"type": "wheel", "wheel": "lift_master", "sens": 42}}
	}`)

	if err := seedOrMergeConfig(bundled, cfgDir); err != nil {
		t.Fatalf("seedOrMergeConfig() error = %v, want nil", err)
	}

	merged := readMergedJSON(t, filepath.Join(cfgDir, "bindings.json"))
	col := merged["col"].(map[string]any)
	if _, ok := col["NewButton"]; !ok {
		t.Errorf("merged col bank missing new control key NewButton: %v", col)
	}
	exposure := col["Exposure"].(map[string]any)
	if sens := exposure["sens"].(float64); sens != 42 {
		t.Errorf("existing control's sens = %v, want 42 (must survive the merge)", sens)
	}
}

func TestSeedOrMergeConfig_DeviceMapJSON_AddsNewKeyWithoutClobberingExisting(t *testing.T) {
	bundled := t.TempDir()
	cfgDir := t.TempDir()
	writeFile(t, filepath.Join(bundled, "device-map.json"), `{"knobs": {"33": "Exposure", "48": "ControlDial"}}`)
	writeFile(t, filepath.Join(cfgDir, "device-map.json"), `{"knobs": {"33": "CustomExposureName"}}`)

	if err := seedOrMergeConfig(bundled, cfgDir); err != nil {
		t.Fatalf("seedOrMergeConfig() error = %v, want nil", err)
	}

	merged := readMergedJSON(t, filepath.Join(cfgDir, "device-map.json"))
	knobs := merged["knobs"].(map[string]any)
	if knobs["33"] != "CustomExposureName" {
		t.Errorf("existing knob mapping overwritten: got %v, want CustomExposureName", knobs["33"])
	}
	if knobs["48"] != "ControlDial" {
		t.Errorf("new knob mapping not added: %v", knobs)
	}
}

// An unparseable existing file (corrupt JSON, or a leftover from a
// pre-Phase-2 build that predates this merge logic entirely) must never
// crash or error the whole seed pass — merging is a pure enhancement, so
// a file we can't safely merge is left exactly as the user has it.
func TestSeedOrMergeConfig_UnparseableExistingFile_LeftUntouchedNoError(t *testing.T) {
	bundled := t.TempDir()
	cfgDir := t.TempDir()
	writeFile(t, filepath.Join(bundled, "bindings.json"), `{"edit": {"ControlDial": {"type": "jog"}}}`)
	writeFile(t, filepath.Join(cfgDir, "bindings.json"), "not-valid-json{{{")

	if err := seedOrMergeConfig(bundled, cfgDir); err != nil {
		t.Fatalf("seedOrMergeConfig() error = %v, want nil (a merge failure must be non-fatal)", err)
	}
	if got := readFile(t, filepath.Join(cfgDir, "bindings.json")); got != "not-valid-json{{{" {
		t.Errorf("unparseable existing file was modified: got %q", got)
	}
}

// Additive merge must be idempotent: running it twice in a row with the
// same bundled default produces byte-identical output the second time
// (nothing left to add).
func TestSeedOrMergeConfig_Idempotent_SecondRunNoChange(t *testing.T) {
	bundled := t.TempDir()
	cfgDir := t.TempDir()
	writeFile(t, filepath.Join(bundled, "bindings.json"), `{"col": {"A": {"type": "key"}}, "edit": {"B": {"type": "jog"}}}`)
	writeFile(t, filepath.Join(cfgDir, "bindings.json"), `{"col": {"A": {"type": "key", "custom": true}}}`)

	if err := seedOrMergeConfig(bundled, cfgDir); err != nil {
		t.Fatalf("first seedOrMergeConfig() error = %v, want nil", err)
	}
	first := readFile(t, filepath.Join(cfgDir, "bindings.json"))

	if err := seedOrMergeConfig(bundled, cfgDir); err != nil {
		t.Fatalf("second seedOrMergeConfig() error = %v, want nil", err)
	}
	second := readFile(t, filepath.Join(cfgDir, "bindings.json"))

	if first != second {
		t.Errorf("second merge run changed the file:\nfirst:  %s\nsecond: %s", first, second)
	}
}

// readMergedJSON reads path and unmarshals it as a generic JSON object,
// failing the test with the raw content on any parse error (easier to
// debug than a bare json error).
func readMergedJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw := readFile(t, path)
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("merged JSON at %s is invalid: %v\n%s", path, err, raw)
	}
	return m
}

// --- resolveAppDirs -----------------------------------------------------

func TestResolveAppDirs_DevMode_UnchangedDefaults(t *testing.T) {
	stubExecutable(t, "/Users/dev/repo/app/app.test") // no ".app/Contents/MacOS/" — dev/test binary

	cfgDir, resourcesDir, err := resolveAppDirs()
	if err != nil {
		t.Fatalf("resolveAppDirs() error = %v, want nil", err)
	}
	if cfgDir != "config" || resourcesDir != "." {
		t.Fatalf("resolveAppDirs() dev mode = (%q, %q), want (\"config\", \".\")", cfgDir, resourcesDir)
	}
}

func TestResolveAppDirs_BundleMode_SeedsAndPointsAtAppSupport(t *testing.T) {
	bundleRoot := t.TempDir()
	exePath := filepath.Join(bundleRoot, "2t5-deck-plus.app", "Contents", "MacOS", "2t5-deck-plus")
	resourcesConfigDir := filepath.Join(bundleRoot, "2t5-deck-plus.app", "Contents", "Resources", "config")
	for _, name := range seedConfigFiles {
		writeFile(t, filepath.Join(resourcesConfigDir, name), "bundled:"+name)
	}

	dataRoot := stubDataRoot(t)
	stubExecutable(t, exePath)

	cfgDir, resourcesDir, err := resolveAppDirs()
	if err != nil {
		t.Fatalf("resolveAppDirs() error = %v, want nil", err)
	}

	wantCfgDir := filepath.Join(dataRoot, "config")
	if cfgDir != wantCfgDir {
		t.Errorf("resolveAppDirs() cfgDir = %q, want %q", cfgDir, wantCfgDir)
	}
	wantResourcesDir := filepath.Join(bundleRoot, "2t5-deck-plus.app", "Contents", "Resources")
	if resourcesDir != wantResourcesDir {
		t.Errorf("resolveAppDirs() resourcesDir = %q, want %q", resourcesDir, wantResourcesDir)
	}

	// Seeding must have actually run as a side effect.
	for _, name := range seedConfigFiles {
		got := readFile(t, filepath.Join(wantCfgDir, name))
		want := "bundled:" + name
		if got != want {
			t.Errorf("resolveAppDirs() did not seed %s: got %q, want %q", name, got, want)
		}
	}
}

func TestResolveAppDirs_BundleMode_DoesNotClobberExistingUserConfig(t *testing.T) {
	bundleRoot := t.TempDir()
	exePath := filepath.Join(bundleRoot, "2t5-deck-plus.app", "Contents", "MacOS", "2t5-deck-plus")
	resourcesConfigDir := filepath.Join(bundleRoot, "2t5-deck-plus.app", "Contents", "Resources", "config")
	for _, name := range seedConfigFiles {
		writeFile(t, filepath.Join(resourcesConfigDir, name), "bundled-default")
	}

	dataRoot := stubDataRoot(t)
	appSupportDir := filepath.Join(dataRoot, "config")
	writeFile(t, filepath.Join(appSupportDir, "bindings.json"), "user-edited-content")

	stubExecutable(t, exePath)

	if _, _, err := resolveAppDirs(); err != nil {
		t.Fatalf("resolveAppDirs() error = %v, want nil", err)
	}

	if got := readFile(t, filepath.Join(appSupportDir, "bindings.json")); got != "user-edited-content" {
		t.Fatalf("resolveAppDirs() clobbered existing user bindings.json: got %q", got)
	}
}

// TestResolveAppDirs_BundleMode_MergesNewBankIntoExistingBindings
// reproduces the exact stale-config bug end-to-end through the real
// startup path (resolveAppDirs, not just seedOrMergeConfig directly): a
// Phase-1-era app-support bindings.json (has "col", no "edit" bank at
// all) plus a Phase-2 bundled default (has both) must, after ONE call to
// resolveAppDirs — i.e. one app launch — leave the app-support file with
// the "edit" bank present AND the user's customized "col" value intact.
// Before FIX 2 this test fails: seedConfigIfMissing skipped the whole
// file because it already existed, so the new bank was never visible.
func TestResolveAppDirs_BundleMode_MergesNewBankIntoExistingBindings(t *testing.T) {
	bundleRoot := t.TempDir()
	exePath := filepath.Join(bundleRoot, "2t5-deck-plus.app", "Contents", "MacOS", "2t5-deck-plus")
	resourcesConfigDir := filepath.Join(bundleRoot, "2t5-deck-plus.app", "Contents", "Resources", "config")
	writeFile(t, filepath.Join(resourcesConfigDir, "bindings.json"), `{
		"col":  {"Exposure": {"type": "wheel", "wheel": "lift_master", "sens": 15}},
		"edit": {"ControlDial": {"type": "jog", "maxDelta": 30}}
	}`)

	dataRoot := stubDataRoot(t)
	appSupportDir := filepath.Join(dataRoot, "config")
	// A Phase-1-era user config: has "col" (customized sens), no "edit"
	// bank at all — this is exactly the stale-config trap.
	writeFile(t, filepath.Join(appSupportDir, "bindings.json"), `{
		"col": {"Exposure": {"type": "wheel", "wheel": "lift_master", "sens": 99}}
	}`)

	stubExecutable(t, exePath)

	if _, _, err := resolveAppDirs(); err != nil {
		t.Fatalf("resolveAppDirs() error = %v, want nil", err)
	}

	merged := readMergedJSON(t, filepath.Join(appSupportDir, "bindings.json"))
	if _, ok := merged["edit"]; !ok {
		t.Fatalf("resolveAppDirs() did not merge in the new \"edit\" bank — the exact stale-config bug: %v", merged)
	}
	col := merged["col"].(map[string]any)
	exposure := col["Exposure"].(map[string]any)
	if sens := exposure["sens"].(float64); sens != 99 {
		t.Errorf("user's existing col.Exposure.sens = %v, want 99 (must survive the merge)", sens)
	}
}

// --- test helpers ---------------------------------------------------------

func stubExecutable(t *testing.T, path string) {
	t.Helper()
	orig := executableFn
	executableFn = func() (string, error) { return path, nil }
	t.Cleanup(func() { executableFn = orig })
}

// stubDataRoot points datadir at a per-test temp directory and returns it.
// Every test that exercises bundle mode MUST use it: without the override
// resolveAppDirs would resolve, create and seed the REAL external data root —
// an SMB-shared volume mirrored to another machine, so a forgotten stub is
// not merely untidy, it is cross-device write churn.
func stubDataRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(datadir.EnvVar, root)
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(data)
}
