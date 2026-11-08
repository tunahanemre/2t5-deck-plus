package editorapi

import (
	"log"

	"2t5-deck-plus/internal/preset"
)

// ArmStart says how the app came up: whether the deck was armed at start and,
// when it was not, why (preset.StartReason*). It is fixed at start; arming
// later does not change it. GET /api/live reports it as armedAtStart /
// startReason so the UI can say "started disarmed (first run)".
type ArmStart struct {
	Armed  bool
	Reason string
}

// ApplyStartPolicy decides, from the presets dir and the environment, whether
// the app starts disarmed (preset.Store.StartDisarmed) and, if so, disarms the
// switch NOW: call it while wiring the app, before the engine is started, so
// there is no armed window (SetArmed(false) also pauses the key and mouse
// packages). A nil switch (no safety switch here) is reported as armed.
func ApplyStartPolicy(st *preset.Store, arm ArmProvider, getenv func(string) string) ArmStart {
	if arm == nil || st == nil {
		return ArmStart{Armed: true}
	}
	// Armed by default: the deck always starts
	// armed. A disarmed start only happens when asked for with
	// the start-disarmed env var; the old reasons (first run, outside changes,
	// disarmed last session) no longer keep the deck off.
	disarm, why := st.StartDisarmed(getenv)
	if getenv == nil || getenv(preset.EnvStartDisarmed) != "1" {
		if disarm {
			log.Printf("[editor] starting ARMED by default (would have been disarmed: %s)", why)
		}
		if err := st.ClearDisarmed(); err != nil {
			log.Printf("[editor] could not clear the disarmed marker: %v", err)
		}
		arm.SetArmed(true)
		return ArmStart{Armed: true}
	}

	arm.SetArmed(false)
	// A disarmed start is remembered like a deliberate disarm: quit without
	// arming and the next launch comes up disarmed too ("disarmed-last-session"),
	// whatever the reason was this time. Arming (the editor, POST /api/arm) clears it.
	if err := st.MarkDisarmed(); err != nil {
		log.Printf("[editor] could not remember the disarmed start: %v", err)
	}
	return ArmStart{Armed: false, Reason: why}
}
