// Command flag_probe answers the single empirical question Task 5 of the
// 2026-08-12 keyboard-replacement plan exists to settle: when a modifier
// key-DOWN is posted via CGEventPost as its OWN separate event, does a
// SUBSEQUENT, independently-posted key event — whose own CGEventFlags is
// explicitly 0 — get treated by the receiving application as modified
// anyway (i.e. does macOS track "this modifier is currently held" as
// system-wide state that implicitly attaches to every later event), or is
// each posted event evaluated purely on its own explicit flags field?
//
// The entire flag-merge design in Tasks 7 and 8 of that plan (merging
// held deck-modifier flags into every subsequently posted key AND mouse
// event) rests on the assumption that the answer is NO — that a held
// modifier must be merged in EXPLICITLY on every later post, because
// CGEventPost does not carry implicit cross-event modifier state. Nobody
// in this repo has ever tested that assumption against a real receiving
// application. If the answer turns out to be YES, Tasks 7/8 shrink to a
// documentation note — see the plan's Task 7/8 gate.
//
// # Standalone tool — NOT part of `go test`, NOT wired into the 2t5-deck-plus
// app, NOT built or run by any CI/build step. Build it explicitly:
//
//	go build -o /tmp/flag_probe ./tools/flag_probe
//
// # Safety — READ BEFORE RUNNING
//
//   - *** NEVER run this against DaVinci Resolve. *** It posts real
//     keyboard input system-wide to whatever application is frontmost.
//     Run it only against a disposable scratch target (TextEdit, a
//     throwaway Notes.app note, a scratch terminal prompt) — something
//     where a stray character or an accidental app shortcut (e.g.
//     Cmd+A "select all") costs nothing.
//   - *** NEVER run this from an automated/agentic session. *** This
//     tool must be run BY THE USER, interactively, watching the target
//     field, because the result can only be read by a human looking at
//     what actually landed. An agent cannot observe the effect and must
//     not attempt to.
//   - Double-gated, mirroring internal/keys/internal/mouse's own
//     Arm()/Armed() split from the env var — two independent switches,
//     neither sufficient alone:
//     1. DECKPLUS_ARMED=1 must be set in the environment (the SAME
//     guard keys.Send/SendDown/SendUp already honour internally —
//     this tool adds no new arm mechanism of its own, it only adds a
//     second, tool-level gate on top).
//     2. The explicit -run flag must also be passed on the command line.
//     Without BOTH, the tool prints what it would do and exits having
//     posted nothing — the same safe-by-default posture as `go test`
//     itself (which never sets DECKPLUS_ARMED, so keys.Send always
//     no-ops there regardless of this tool).
//
// # Usage
//
//	# Dry run (always safe — prints the plan, posts nothing):
//	go run ./tools/flag_probe
//
//	# Real run (only after clicking into a scratch text field):
//	DECKPLUS_ARMED=1 go run ./tools/flag_probe -run
//
// After running, record the observed result in
// docs/notes/2026-08-12-cgevent-flag-inheritance.md and re-read the
// plan's Task 7/8 gate note before implementing either task.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"2t5-deck-plus/internal/keys"
)

func main() {
	run := flag.Bool("run", false, "actually post real CGEvent input (also requires DECKPLUS_ARMED=1 in the environment)")
	modifier := flag.String("modifier", "Alt", "modifier key to probe as the held key: Alt, Shift, Cmd, or Ctrl")
	probeKey := flag.String("key", "a", "plain key posted BETWEEN the modifier down/up, with its own CGEventFlags explicitly 0 (a single unmodified key, e.g. \"a\")")
	flag.Parse()

	fmt.Println("=== 2t5-deck-plus CGEvent flag-inheritance probe (Task 5, 2026-08-12 plan) ===")
	fmt.Println()
	fmt.Println("Question: does a SEPARATELY posted modifier key-DOWN get reflected in a")
	fmt.Println("SUBSEQUENT, independently-posted key event whose own CGEventFlags is")
	fmt.Println("explicitly 0? This determines whether Tasks 7/8's flag-merge design (merging")
	fmt.Println("held deck-modifier flags into every posted key/mouse event) is necessary as")
	fmt.Println("specified, or shrinks to a documentation note.")
	fmt.Println()
	fmt.Printf("Sequence: SendDown(%q)  ->  Send(%q) [flags=0]  ->  SendUp(%q)\n", *modifier, *probeKey, *modifier)
	fmt.Println()

	if !keys.Armed() || !*run {
		fmt.Println("NOT ARMED — nothing will be posted. This is the safe default.")
		fmt.Printf("  keys.Armed() [needs DECKPLUS_ARMED=1] = %v\n", keys.Armed())
		fmt.Printf("  -run flag                                = %v\n", *run)
		fmt.Println()
		fmt.Println("To actually run the probe, read the safety notes in this file's header,")
		fmt.Println("click into a DISPOSABLE scratch text field (never Resolve), then:")
		fmt.Println()
		fmt.Println("  DECKPLUS_ARMED=1 go run ./tools/flag_probe -run")
		fmt.Println()
		return
	}

	fmt.Println("!!! ARMED — this WILL post real keyboard input system-wide. !!!")
	fmt.Println()
	fmt.Println("*** Make sure DaVinci Resolve is NOT frontmost. ***")
	fmt.Println("Click into a DISPOSABLE scratch text field now (TextEdit / a throwaway")
	fmt.Println("Notes.app note / a scratch terminal prompt) — not anything that matters if")
	fmt.Println("a stray character or an app shortcut lands in it.")
	fmt.Println()
	fmt.Println("Posting in 5 seconds — switch focus now...")
	for i := 5; i > 0; i-- {
		fmt.Printf("  %d...\n", i)
		time.Sleep(1 * time.Second)
	}

	fmt.Printf("-> SendDown(%q)\n", *modifier)
	if err := keys.SendDown(*modifier); err != nil {
		fmt.Fprintln(os.Stderr, "SendDown error:", err)
		os.Exit(1)
	}
	time.Sleep(300 * time.Millisecond)

	fmt.Printf("-> Send(%q)  [posted independently, with its own flags=0]\n", *probeKey)
	if err := keys.Send(*probeKey); err != nil {
		fmt.Fprintln(os.Stderr, "Send error:", err)
		// Fall through to release the held modifier regardless of this
		// error — never leave a modifier stuck down because the probe key
		// itself failed to parse/post.
	}
	time.Sleep(300 * time.Millisecond)

	fmt.Printf("-> SendUp(%q)\n", *modifier)
	if err := keys.SendUp(*modifier); err != nil {
		fmt.Fprintln(os.Stderr, "SendUp error:", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Done. Look at the scratch text field now:")
	fmt.Println()
	fmt.Printf("  - A PLAIN %q landed, nothing else happened\n", *probeKey)
	fmt.Println("      -> flags are NOT inherited across separately posted events.")
	fmt.Println("         Tasks 7/8's explicit flag-merge design is CONFIRMED necessary,")
	fmt.Println("         exactly as specified in the plan.")
	fmt.Println()
	fmt.Printf("  - A MODIFIED result landed instead (e.g. an Option-shifted/accented\n")
	fmt.Println("    character, or an app-level shortcut fired — Cmd+A select-all, etc.)")
	fmt.Println("      -> flags ARE inherited from the separately-posted modifier down.")
	fmt.Println("         Tasks 7/8 shrink dramatically — re-read their gate note in the")
	fmt.Println("         plan before implementing either one.")
	fmt.Println()
	fmt.Println("Record the observed result in:")
	fmt.Println("  docs/notes/2026-08-12-cgevent-flag-inheritance.md")
}
