package engine

import (
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/keys"
	"2t5-deck-plus/internal/macapp"
	"2t5-deck-plus/internal/preset"
)

// Hold to fire for Resolve KEY presses (wave H, item 4). A "key" binding with
// holdMs > 0 does not post its key at press: one time.AfterFunc posts it once
// the control has stayed down that long (and Resolve is still frontmost); a
// release before that cancels the timer and reports "hold to fire (N ms)".
// Nothing here runs unless the binding has holdMs, so a binding without it
// takes exactly the path it took before (dispatchNoteOn's IsKey case).
// Other Resolve verbs ignore holdMs. Non-Resolve apps are handled by
// internal/appdeck with the same field.

const maxHoldMs = 2000

// Seams for tests.
var (
	holdAfterFn = func(d time.Duration, f func()) func() {
		t := time.AfterFunc(d, f)
		return func() { t.Stop() }
	}
	holdSendKeyFn = keys.Send
)

type engHold struct {
	gen    uint64
	ms     int
	cancel func()
	epoch  uint64 // appdeck ArmEpoch at press (0 without an executor)
}

type holdTable struct {
	mu  sync.Mutex
	gen uint64
	m   map[string]*engHold
}

// holdMsOf returns the usable hold of a binding: 0 unless it is a key press with
// a positive holdMs, clamped to maxHoldMs.
func holdMsOf(b config.Binding) int {
	if !b.IsKey() || math.IsNaN(b.HoldMs) || b.HoldMs < 1 {
		return 0
	}
	if b.HoldMs > maxHoldMs {
		return maxHoldMs
	}
	return int(b.HoldMs)
}

// holdResolveKey arms the hold for control and returns the press text.
func (e *Engine) holdResolveKey(control string, b config.Binding) string {
	ms := holdMsOf(b)
	key := b.Key
	e.holds.mu.Lock()
	if e.holds.m == nil {
		e.holds.m = map[string]*engHold{}
	}
	if old := e.holds.m[control]; old != nil && old.cancel != nil {
		old.cancel()
	}
	e.holds.gen++
	h := &engHold{gen: e.holds.gen, ms: ms}
	if ad := e.hooks.appdeck.Load(); ad != nil {
		h.epoch = ad.ArmEpoch()
	}
	e.holds.m[control] = h
	gen := h.gen
	e.holds.mu.Unlock()

	cancel := holdAfterFn(time.Duration(ms)*time.Millisecond, func() { e.holdFire(control, gen, key) })
	e.holds.mu.Lock()
	if cur := e.holds.m[control]; cur != nil && cur.gen == gen {
		cur.cancel = cancel
	} else {
		cancel()
	}
	e.holds.mu.Unlock()
	log.Printf("[note_on] control=%q key=%q waits for a %d ms hold", control, key, ms)
	return fmt.Sprintf("hold %d ms to fire", ms)
}

func (e *Engine) holdFire(control string, gen uint64, key string) {
	e.holds.mu.Lock()
	h := e.holds.m[control]
	if h == nil || h.gen != gen {
		e.holds.mu.Unlock()
		return
	}
	delete(e.holds.m, control)
	e.holds.mu.Unlock()

	// a hold belongs to the arm state it began in (arm -> disarm -> arm in between cancels it)
	if ad := e.hooks.appdeck.Load(); ad != nil && ad.ArmEpoch() != h.epoch {
		log.Printf("[note_on] control=%q hold cancelled: the arm switch changed", control)
		return
	}

	text := key
	switch {
	case !macapp.BundleIDIsResolve(frontmostBundleIDFn()):
		log.Printf("[note_on] control=%q hold fired but Resolve is no longer frontmost: key %q not sent", control, key)
		return
	case !e.writesAllowed():
		text = "disarmed: " + key
	default:
		log.Printf("[note_on] control=%q key=%q (held %d ms)", control, key, h.ms)
		if err := holdSendKeyFn(key); err != nil {
			log.Printf("[note_on] control=%q key=%q error: %v", control, key, err)
		}
		e.setLastAction(control)
	}
	if e.observing() {
		e.emitInput(InputEvent{Control: control, Kind: "press", Accel: 1, Out: InputOut{Text: text},
			PresetID: preset.ResolveID, Source: "app", Action: "key"})
	}
}

// holdRelease cancels a pending hold for control. was reports whether one was
// pending (the control was released before the threshold).
func (e *Engine) holdRelease(control string) (ms int, was bool) {
	e.holds.mu.Lock()
	defer e.holds.mu.Unlock()
	h := e.holds.m[control]
	if h == nil {
		return 0, false
	}
	delete(e.holds.m, control)
	if h.cancel != nil {
		h.cancel()
	}
	return h.ms, true
}

// holdCancelAll drops every pending hold (Stop).
func (e *Engine) holdCancelAll() {
	e.holds.mu.Lock()
	all := e.holds.m
	e.holds.m = nil
	e.holds.mu.Unlock()
	for _, h := range all {
		if h.cancel != nil {
			h.cancel()
		}
	}
}

// panicDisarm is the Resolve preset's own "disarm" key: the same full disarm as
// POST /api/arm {armed:false}. Returns the press text.
func (e *Engine) panicDisarm(control string) string {
	ad := e.hooks.appdeck.Load()
	if ad == nil {
		return ""
	}
	was := ad.Armed()
	ad.SetArmedFrom(false, "deck-key "+control)
	log.Printf("[arm] control=%q: DISARMED from the deck (panic key)", control)
	if was {
		return "deck disarmed"
	}
	return "deck already disarmed"
}

// panicPress handles a press of a control assigned to the panic disarm action:
// disarm, log, and show it in the live test. Runs on the MIDI callback. The
// disarm itself is immediate (atomics); the OnArmChange callback it triggers
// runs under the arm lock and must be quick (the app hands the .disarmed marker
// to a writer goroutine).
func (e *Engine) panicPress(control string) {
	text := e.panicDisarm(control)
	e.setLastAction(text)
	if e.observing() {
		e.emitInput(InputEvent{Control: control, Kind: "press", Accel: 1, Out: InputOut{Text: text},
			Fine: false, PresetID: preset.GlobalID, Source: "global", Action: "generic.disarm"})
	}
}
