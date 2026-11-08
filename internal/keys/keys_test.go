package keys

import (
	"os"
	"strings"
	"testing"
)

// TestDeckPlusArmedNeverSetDuringTests is the hard tripwire: it FAILS the
// suite outright if DECKPLUS_ARMED=1 is somehow present in the test
// environment. go test must never run with this variable set — if it is,
// Send() below would post real CGEvent keystrokes into whatever application
// has focus.
func TestDeckPlusArmedNeverSetDuringTests(t *testing.T) {
	if v := os.Getenv("DECKPLUS_ARMED") + os.Getenv("LD_DECK_ARMED"); strings.Contains(v, "1") {
		t.Fatalf("DECKPLUS_ARMED=1 is set in the test environment — refusing to continue. "+
			"This variable must NEVER be set for `go test`; real keystrokes would be posted "+
			"to the frontmost application. (got DECKPLUS_ARMED+LD_DECK_ARMED=%q)", v)
	}
}

// TestEnvArmedDetection unit-tests the env-var → bool mapping used by the
// arm guard, WITHOUT ever touching the real `armed` flag or calling Arm().
// envArmed() is a pure function (reads os.Getenv, returns a bool, no side
// effects), so this is safe to exercise directly with t.Setenv — it cannot
// arm this process and cannot cause Send() to post real input.
func TestEnvArmedDetection(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"true", false}, // only the exact string "1" arms — no truthy coercion
		{"TRUE", false},
		{"yes", false},
		{"1", true},
	}
	for _, tc := range cases {
		t.Run("DECKPLUS_ARMED="+tc.val, func(t *testing.T) {
			t.Setenv("DECKPLUS_ARMED", tc.val)
			if got := envArmed(); got != tc.want {
				t.Errorf("envArmed() with DECKPLUS_ARMED=%q = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// TestSendParsingErrors verifies that Send returns an error for invalid specs
// without calling CGEventCreateKeyboardEvent (which would require Accessibility).
// We can rely on the error path being reached before any CGO call.
func TestSendParsingErrors(t *testing.T) {
	cases := []struct {
		spec string
		want string // non-empty substring expected in error message
	}{
		{"", "empty spec"},
		{"Bogus+Z", "unknown modifier"},
		{"Cmd+", "unknown key"},
		{"Cmd+BadKey", "unknown key"},
		{"Shift+Bogus+Z", "unknown modifier"},
	}
	for _, tc := range cases {
		err := Send(tc.spec)
		if err == nil {
			t.Errorf("Send(%q) should have returned an error", tc.spec)
			continue
		}
		if tc.want != "" {
			got := err.Error()
			found := false
			for i := 0; i <= len(got)-len(tc.want); i++ {
				if got[i:i+len(tc.want)] == tc.want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Send(%q) error = %q, want substring %q", tc.spec, got, tc.want)
			}
		}
	}
}

// TestSendValidSpecsNoError verifies that well-formed specs parse without
// error AND — critically — that the CGEventPost call itself is a proven
// no-op, not an assumed one. This test used to call Send() with real specs
// (including Cmd+Z, Cmd+Shift+S, and bare arrow/Return/Tab/Escape keys) on
// the incorrect assumption that "CGEventPost is a no-op without
// Accessibility" (it is not — CGEventPost posts to the HID event tap
// regardless of this process's Accessibility trust, and the receiving app
// generally does react). That gap is exactly what caused real keystrokes
// ("0", "9", ...) to land in whatever application had focus during `go
// test`. The arm-guard in keys_darwin.go now makes this impossible
// (Send no-ops unless Armed(), and go test never arms), and this test
// explicitly asserts that no-op path was taken via NoopCalls() rather than
// silently hoping nothing happened.
func TestSendValidSpecsNoError(t *testing.T) {
	if Armed() {
		t.Fatal("keys.Armed() == true with DECKPLUS_ARMED unset — arm guard is broken; " +
			"refusing to exercise Send() in this state")
	}

	valid := []string{
		"Cmd+Z",
		"Ctrl+Shift+Z",
		"Alt+S",
		"Left",
		"Right",
		"Up",
		"Down",
		"Return",
		"Space",
		"Tab",
		"Escape",
		"Cmd+Shift+S",
		"f1",
		"F5",
		"0",
		"9",
	}

	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)

	for _, spec := range valid {
		if err := Send(spec); err != nil {
			t.Errorf("Send(%q) unexpected error: %v", spec, err)
		}
	}

	calls := NoopCalls()
	if len(calls) != len(valid) {
		t.Fatalf("NoopCalls() = %d entries %v, want %d (one per Send call above, proving none posted a real CGEvent)",
			len(calls), calls, len(valid))
	}
	for i, spec := range valid {
		if calls[i].Spec != spec {
			t.Errorf("NoopCalls()[%d].Spec = %q, want %q", i, calls[i].Spec, spec)
		}
		if calls[i].Kind != "down+up" {
			t.Errorf("NoopCalls()[%d].Kind = %q, want \"down+up\" (Send posts a combined down+up)", i, calls[i].Kind)
		}
	}
}

// ---------------------------------------------------------------------------
// Task 4 (2026-08-12): SendDown/SendUp split + modifier keycodes + flags in
// NoopCalls -- prep for held deck modifiers (Alt/Ctrl/Cmd/Shift). postKey
// used to post down+up as one atomic C call, so nothing could be *held*;
// NoopCalls used to record only the spec string, which made a future
// flag-merge test (Task 7) unwriteable.
// ---------------------------------------------------------------------------

// TestSendDown_RecordsNoopDownOnly proves SendDown posts (records, unarmed)
// exactly one no-op entry marked as a DOWN, with no matching key-up.
func TestSendDown_RecordsNoopDownOnly(t *testing.T) {
	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)

	if err := SendDown("Alt"); err != nil {
		t.Fatalf("SendDown(Alt) unexpected error: %v", err)
	}

	calls := NoopCalls()
	if len(calls) != 1 {
		t.Fatalf("NoopCalls() = %d entries %v, want 1", len(calls), calls)
	}
	if calls[0].Kind != "down" {
		t.Errorf("Kind = %q, want \"down\"", calls[0].Kind)
	}
	if calls[0].Spec != "Alt" {
		t.Errorf("Spec = %q, want \"Alt\"", calls[0].Spec)
	}
}

// TestSendUp_RecordsNoopUpOnly is SendDown's mirror for the release half.
func TestSendUp_RecordsNoopUpOnly(t *testing.T) {
	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)

	if err := SendUp("Alt"); err != nil {
		t.Fatalf("SendUp(Alt) unexpected error: %v", err)
	}

	calls := NoopCalls()
	if len(calls) != 1 {
		t.Fatalf("NoopCalls() = %d entries %v, want 1", len(calls), calls)
	}
	if calls[0].Kind != "up" {
		t.Errorf("Kind = %q, want \"up\"", calls[0].Kind)
	}
	if calls[0].Spec != "Alt" {
		t.Errorf("Spec = %q, want \"Alt\"", calls[0].Spec)
	}
}

// TestSendDown_UnknownKeyErrors mirrors Send's unknown-key error path.
func TestSendDown_UnknownKeyErrors(t *testing.T) {
	if err := SendDown("Bogus"); err == nil {
		t.Error("SendDown(\"Bogus\") should have returned an error")
	}
}

// TestSendUp_UnknownKeyErrors mirrors Send's unknown-key error path.
func TestSendUp_UnknownKeyErrors(t *testing.T) {
	if err := SendUp("Bogus"); err == nil {
		t.Error("SendUp(\"Bogus\") should have returned an error")
	}
}

// TestModifierKeycodesResolve confirms the four modifier keys resolve to
// their real macOS virtual keycodes (US layout, left-hand variants) via
// keyTable -- the SAME table Send's final "key name" segment already
// resolves through, now also reachable directly by SendDown/SendUp for a
// bare modifier name. Originally added for Task 6's held deck-modifier
// engine feature (2026-08-12), which was later reverted (see engine.go's
// git history) as an unacceptable stuck-modifier risk -- these keyTable
// entries and SendDown/SendUp themselves were KEPT, not reverted, because
// tools/flag_probe (a separate, deliberately human-only, double-gated
// diagnostic; see its own doc comment) genuinely depends on both.
func TestModifierKeycodesResolve(t *testing.T) {
	cases := map[string]cgKeyCode{
		"command": 55,
		"cmd":     55,
		"shift":   56,
		"option":  58,
		"alt":     58,
		"control": 59,
		"ctrl":    59,
	}
	for name, want := range cases {
		got, ok := keyTable[name]
		if !ok {
			t.Errorf("keyTable missing modifier key %q", name)
			continue
		}
		if got != want {
			t.Errorf("keyTable[%q] = %d, want %d", name, got, want)
		}
	}
}

// TestNoopCallFlags_ZeroForUnmodifiedSendDown confirms a plain SendDown
// (no flags merged yet -- that's Task 7) records Flags==0, not some
// uninitialised sentinel -- the field exists and is meaningfully zero.
func TestNoopCallFlags_ZeroForUnmodifiedSendDown(t *testing.T) {
	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)

	if err := SendDown("Alt"); err != nil {
		t.Fatalf("SendDown(Alt) unexpected error: %v", err)
	}
	calls := NoopCalls()
	if len(calls) != 1 {
		t.Fatalf("NoopCalls() = %d entries, want 1", len(calls))
	}
	if calls[0].Flags != 0 {
		t.Errorf("Flags = %#x, want 0 (no flag merge in this task)", calls[0].Flags)
	}
}

// TestNoopCallFlags_ComputedForModifiedSend confirms Send's existing
// modifier-prefix parsing ("Cmd+Z" etc.) is reflected in the NoopCall's
// Flags field -- this is what makes Task 7's flag-merge test writable.
func TestNoopCallFlags_ComputedForModifiedSend(t *testing.T) {
	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)

	if err := Send("Cmd+Z"); err != nil {
		t.Fatalf("Send(Cmd+Z) unexpected error: %v", err)
	}
	calls := NoopCalls()
	if len(calls) != 1 {
		t.Fatalf("NoopCalls() = %d entries, want 1", len(calls))
	}
	const kCGEventFlagMaskCommand = 0x100000
	if calls[0].Flags != kCGEventFlagMaskCommand {
		t.Errorf("Flags = %#x, want %#x (kCGEventFlagMaskCommand)", calls[0].Flags, kCGEventFlagMaskCommand)
	}

	if err := Send("Ctrl+Shift+Z"); err != nil {
		t.Fatalf("Send(Ctrl+Shift+Z) unexpected error: %v", err)
	}
	calls = NoopCalls()
	const kCGEventFlagMaskControl = 0x40000
	const kCGEventFlagMaskShift = 0x20000
	want := uint64(kCGEventFlagMaskControl | kCGEventFlagMaskShift)
	if calls[1].Flags != want {
		t.Errorf("Flags = %#x, want %#x (Control|Shift)", calls[1].Flags, want)
	}
}

// ---------------------------------------------------------------------------
// ParseModifierFlags (2026-08-13) — extracted from Send's own modifier-
// prefix parsing so internal/engine's "scrollDelta" binding type can build
// a flags bitmask with no trailing key name to parse. Send is refactored to
// call this too (see its doc comment), so these tests also pin Send's own
// modifier parsing by construction — one parser, one set of tests.
// ---------------------------------------------------------------------------

// TestParseModifierFlags_Empty_ZeroNoError proves a modifier-only spec of ""
// is a valid "no flags" answer, distinct from Send's own empty-spec
// rejection (Send additionally requires a trailing key name).
func TestParseModifierFlags_Empty_ZeroNoError(t *testing.T) {
	got, err := ParseModifierFlags("")
	if err != nil {
		t.Fatalf("ParseModifierFlags(\"\") error = %v, want nil", err)
	}
	if got != 0 {
		t.Errorf("ParseModifierFlags(\"\") = %#x, want 0", got)
	}
}

// TestParseModifierFlags_SingleModifiers confirms every recognised modifier
// name (including the Option/Alt and Command/Cmd/Control/Ctrl aliases)
// resolves to its real CGEventFlags mask — the SAME constants
// TestNoopCallFlags_ComputedForModifiedSend already pins for Send.
func TestParseModifierFlags_SingleModifiers(t *testing.T) {
	const (
		kCGEventFlagMaskCommand   = 0x100000
		kCGEventFlagMaskControl   = 0x40000
		kCGEventFlagMaskAlternate = 0x80000
		kCGEventFlagMaskShift     = 0x20000
	)
	cases := map[string]uint64{
		"Cmd":     kCGEventFlagMaskCommand,
		"Command": kCGEventFlagMaskCommand,
		"Ctrl":    kCGEventFlagMaskControl,
		"Control": kCGEventFlagMaskControl,
		"Alt":     kCGEventFlagMaskAlternate,
		"Option":  kCGEventFlagMaskAlternate,
		"Shift":   kCGEventFlagMaskShift,
		"alt":     kCGEventFlagMaskAlternate, // case-insensitive
		" Shift ": kCGEventFlagMaskShift,     // whitespace trimmed
	}
	for spec, want := range cases {
		got, err := ParseModifierFlags(spec)
		if err != nil {
			t.Errorf("ParseModifierFlags(%q) error = %v, want nil", spec, err)
			continue
		}
		if got != want {
			t.Errorf("ParseModifierFlags(%q) = %#x, want %#x", spec, got, want)
		}
	}
}

// TestParseModifierFlags_Combo proves multiple "+"-joined modifiers OR
// together, matching Send's existing "Ctrl+Shift+Z" behaviour for its
// modifier-prefix portion.
func TestParseModifierFlags_Combo(t *testing.T) {
	const kCGEventFlagMaskControl = 0x40000
	const kCGEventFlagMaskShift = 0x20000
	got, err := ParseModifierFlags("Ctrl+Shift")
	if err != nil {
		t.Fatalf("ParseModifierFlags(\"Ctrl+Shift\") error = %v, want nil", err)
	}
	want := uint64(kCGEventFlagMaskControl | kCGEventFlagMaskShift)
	if got != want {
		t.Errorf("ParseModifierFlags(\"Ctrl+Shift\") = %#x, want %#x", got, want)
	}
}

// TestParseModifierFlags_UnknownModifier_Error proves an unrecognised
// modifier name in ANY position errors rather than silently dropping it —
// a config typo (e.g. bindings.json's scrollDelta "flags" field) must be
// visible, not silently produce 0 flags.
func TestParseModifierFlags_UnknownModifier_Error(t *testing.T) {
	for _, spec := range []string{"Bogus", "Alt+Bogus", "Bogus+Shift"} {
		if _, err := ParseModifierFlags(spec); err == nil {
			t.Errorf("ParseModifierFlags(%q) should have returned an error", spec)
		}
	}
}

// TestSendFlags_ConsistentWithParseModifierFlags proves Send's own
// modifier-prefix parsing and ParseModifierFlags agree exactly — they now
// share the same implementation (Send calls ParseModifierFlags directly),
// so this pins that refactor didn't silently diverge the two call sites.
func TestSendFlags_ConsistentWithParseModifierFlags(t *testing.T) {
	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)

	if err := Send("Ctrl+Shift+Z"); err != nil {
		t.Fatalf("Send(Ctrl+Shift+Z) unexpected error: %v", err)
	}
	want, err := ParseModifierFlags("Ctrl+Shift")
	if err != nil {
		t.Fatalf("ParseModifierFlags(Ctrl+Shift) unexpected error: %v", err)
	}
	calls := NoopCalls()
	if len(calls) != 1 {
		t.Fatalf("NoopCalls() = %d entries, want 1", len(calls))
	}
	if calls[0].Flags != want {
		t.Errorf("Send(Ctrl+Shift+Z) recorded Flags = %#x, want %#x (ParseModifierFlags(Ctrl+Shift))", calls[0].Flags, want)
	}
}

// TestKeyTableCompleteness spot-checks that all A-Z letters are in the table.
func TestKeyTableCompleteness(t *testing.T) {
	for c := 'a'; c <= 'z'; c++ {
		name := string(c)
		if _, ok := keyTable[name]; !ok {
			t.Errorf("keyTable missing letter %q", name)
		}
	}
	for _, arrow := range []string{"left", "right", "up", "down"} {
		if _, ok := keyTable[arrow]; !ok {
			t.Errorf("keyTable missing arrow key %q", arrow)
		}
	}
}

// Wave E (F7): the in-app pause switch. Never arms the package.
func TestShouldPostTruthTable(t *testing.T) {
	for _, c := range []struct{ armed, paused, want bool }{
		{false, false, false}, {false, true, false}, {true, false, true}, {true, true, false},
	} {
		if got := shouldPost(c.armed, c.paused); got != c.want {
			t.Errorf("shouldPost(armed=%v, paused=%v) = %v, want %v", c.armed, c.paused, got, c.want)
		}
	}
}

func TestPausedSendIsARecordedNoop(t *testing.T) {
	if Armed() {
		t.Fatal("keys.Armed() in a test")
	}
	SetPaused(true)
	t.Cleanup(func() { SetPaused(false); ResetNoopCalls() })
	if !Paused() {
		t.Fatal("Paused() = false after SetPaused(true)")
	}
	ResetNoopCalls()
	if err := Send("Cmd+T"); err != nil {
		t.Fatal(err)
	}
	if err := SendDown("a"); err != nil {
		t.Fatal(err)
	}
	if err := SendUp("a"); err != nil {
		t.Fatal(err)
	}
	if n := len(NoopCalls()); n != 3 {
		t.Errorf("recorded %d no-ops, want 3", n)
	}
	// validation errors are unchanged by the pause
	if err := Send("Bogus+Z"); err == nil {
		t.Error("an invalid spec was accepted while paused")
	}
}

func TestMediaKeyEncoding(t *testing.T) {
	cases := []struct {
		spec string
		code int
	}{
		{"media:play", 16}, {"media:next", 17}, {"media:prev", 18},
		{"media:brightnessUp", 2}, {"MEDIA:BrightnessDown", 3},
	}
	for _, c := range cases {
		got, err := ParseMediaSpec(c.spec)
		if err != nil || got != c.code {
			t.Errorf("%s: code %d err %v, want %d", c.spec, got, err, c.code)
		}
	}
	// data1 = (keyCode<<16) | (state<<8), 0xA down, 0xB up.
	if got := MediaData1(16, true); got != 0x100A00 {
		t.Errorf("play down data1 = %#x", got)
	}
	if got := MediaData1(16, false); got != 0x100B00 {
		t.Errorf("play up data1 = %#x", got)
	}
	if got := MediaData1(2, true); got != 0x020A00 {
		t.Errorf("brightness up down data1 = %#x", got)
	}
	if MediaModifierFlags(true) != 0xA00 || MediaModifierFlags(false) != 0xB00 {
		t.Error("modifier flags")
	}
	for _, bad := range []string{"media:", "media:stop", "Cmd+media:play", "play"} {
		if _, err := ParseMediaSpec(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

// Unarmed, a media key is a recorded no-op and posts nothing.
func TestMediaKeyUnarmedIsNoop(t *testing.T) {
	if Armed() {
		t.Skip("armed process")
	}
	ResetNoopCalls()
	if err := Send("media:play"); err != nil {
		t.Fatal(err)
	}
	n := NoopCalls()
	if len(n) != 1 || n[0].Spec != "media:play" || n[0].Kind != "media" {
		t.Fatalf("noop log = %+v", n)
	}
	if err := Send("media:bogus"); err == nil {
		t.Error("unknown media key must error")
	}
}
