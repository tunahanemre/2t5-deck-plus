package editorapi

import (
	"sync"
	"time"

	"2t5-deck-plus/internal/preset"
)

// "Why did it switch" log (wave F, round-2 research item 8). Every competitor's
// per-app complaint is a switch that did not happen, happened twice, or used
// the wrong profile, with no way to see why. SwitchLog keeps a ring of the
// last 8 active-app changes and counts focus blips: a frontmost change that
// reverted inside BlipWindow, which therefore never changed the active preset.
//
// It is a plain value fed by Note(); the app feeds it from the editor's
// existing frontmost tracking (a few hundred ms ticker plus API calls), the
// engine's hot path never touches it. Pure and clock-injected, so it is tested
// with fake frontmost sources.

const (
	// BlipWindow is how long a frontmost change must hold to count as a switch.
	BlipWindow = 400 * time.Millisecond
	// SwitchKeep is the ring size.
	SwitchKeep = 8
)

// SwitchEvent is one active-app change.
type SwitchEvent struct {
	T        int64  `json:"t"` // Unix ms the new app became frontmost
	From     string `json:"from"`
	To       string `json:"to"`
	Reason   string `json:"reason"` // frontmost | editor-focused-kept-last | no-preset-global-fallback | resolve-cascade
	PresetID string `json:"presetId"`
	FromName string `json:"fromName,omitempty"` // display names (empty = not known)
	ToName   string `json:"toName,omitempty"`
	Editor   bool   `json:"editor,omitempty"` // the 2T5 DECK+ window came to the front; To stays the active app
}

// SwitchObs is one look at the frontmost app.
type SwitchObs struct {
	FrontID       string // bundle id; "" = lookup failed (not a change)
	EditorFocused bool   // 2T5 DECK+ itself has focus (FrontID is then the last real app)
	Active        string // preset id in effect for FrontID
	FrontName     string // display name of FrontID ("" = not known)
}

// SwitchSnapshot is what GET /api/switches and /api/live "why" show.
type SwitchSnapshot struct {
	Log          []SwitchEvent // newest first, at most SwitchKeep, never nil
	IgnoredBlips int
}

// SwitchProvider supplies the log (nil in Deps = an empty one).
type SwitchProvider interface {
	Snapshot(now time.Time) SwitchSnapshot
}

type switchKey struct {
	id      string
	focused bool
}

type switchPend struct {
	key    switchKey
	active string
	reason string
	t      time.Time
}

// SwitchLog is the ring plus the blip counter. The zero value is not usable;
// call NewSwitchLog. A nil *SwitchLog ignores Note and reports an empty log.
type SwitchLog struct {
	mu      sync.Mutex
	started bool
	cur     switchKey
	curAct  string
	pend    *switchPend
	ring    []SwitchEvent // oldest first
	blips   int
	names   map[string]string // bundle id -> display name, as last reported

	// OnSwitch, when set, receives every committed switch (the events ring).
	OnSwitch func(SwitchEvent)
}

// NewSwitchLog returns an empty log.
func NewSwitchLog() *SwitchLog { return &SwitchLog{} }

// WhyReason is the reason string for a frontmost app: shared by /api/live
// "why" and the switch log so both always agree.
func WhyReason(frontID, active string, editorFocused bool) string {
	switch {
	case editorFocused:
		return WhyEditorFocused
	case frontID == preset.ResolveID:
		return WhyResolveCascade
	case frontID == "" || active == preset.GlobalID:
		return WhyGlobalFallback
	}
	return WhyFrontmost
}

// Note records one look at the frontmost app at time now.
func (l *SwitchLog) Note(now time.Time, o SwitchObs) {
	if l == nil || o.FrontID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if o.FrontName != "" {
		if l.names == nil {
			l.names = map[string]string{}
		}
		l.names[o.FrontID] = o.FrontName
	}
	k := switchKey{o.FrontID, o.EditorFocused}
	if !l.started {
		l.started, l.cur, l.curAct = true, k, o.Active
		return
	}
	if l.pend != nil {
		held := now.Sub(l.pend.t)
		switch {
		case k == l.pend.key:
			if held >= BlipWindow {
				l.commitLocked()
			}
			return
		case held >= BlipWindow:
			l.commitLocked() // it was a real switch; fall through for the new change
		default:
			// reverted (or moved on) inside the window: a blip that never
			// became the active app
			l.pend = nil
			l.blips++
		}
	}
	if k != l.cur {
		l.pend = &switchPend{key: k, active: o.Active, reason: WhyReason(o.FrontID, o.Active, o.EditorFocused), t: now}
	}
}

func (l *SwitchLog) commitLocked() {
	p := l.pend
	l.pend = nil
	ev := SwitchEvent{T: p.t.UnixMilli(), From: l.cur.id, To: p.key.id, Reason: p.reason, PresetID: p.active,
		FromName: l.names[l.cur.id], ToName: l.names[p.key.id], Editor: p.key.focused}
	l.ring = append(l.ring, ev)
	if l.OnSwitch != nil {
		l.OnSwitch(ev)
	}
	if len(l.ring) > SwitchKeep {
		l.ring = l.ring[len(l.ring)-SwitchKeep:]
	}
	l.cur, l.curAct = p.key, p.active
}

// Snapshot returns the log, newest first. A pending change that has held for
// BlipWindow by now is committed first.
func (l *SwitchLog) Snapshot(now time.Time) SwitchSnapshot {
	out := SwitchSnapshot{Log: []SwitchEvent{}}
	if l == nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pend != nil && now.Sub(l.pend.t) >= BlipWindow {
		l.commitLocked()
	}
	for i := len(l.ring) - 1; i >= 0; i-- {
		out.Log = append(out.Log, l.ring[i])
	}
	out.IgnoredBlips = l.blips
	return out
}

// NoteFront feeds the log from what the editor's frontmost tracker knows: the
// app it reports (the last real app while the editor window has focus), whether
// the editor has focus, and the current snapshot (a bundle id without a preset
// runs on Global). front.ID "" = lookup failed, ignored.
func (l *SwitchLog) NoteFront(now time.Time, front AppRef, editorFocused bool, snap *preset.Snapshot) {
	if l == nil || front.ID == "" {
		return
	}
	active := preset.GlobalID
	if snap != nil {
		if _, ok := snap.Preset(front.ID); ok {
			active = front.ID
		}
	}
	l.Note(now, SwitchObs{FrontID: front.ID, EditorFocused: editorFocused, Active: active, FrontName: front.Name})
}
