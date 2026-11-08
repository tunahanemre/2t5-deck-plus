package editorapi

import (
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"2t5-deck-plus/internal/preset"
)

// Events ring (wave H, item 7): the last 50 things that happened to the deck
// that someone may later ask "what was that?" about. It extends the switch log
// (kept as it is: GET /api/switches and why.switchLog are unchanged; every
// committed switch is also copied here as kind "switch").
//
//	switch   an active-app change                          detail {from,to,reason,presetId}
//	arm      the deck was armed or disarmed                text "armed"|"disarmed", source
//	shell    a command was run                             text = the program's base name ONLY, detail {exit}
//	import   an import was applied                         detail {files,dropped}
//	restore  a backup was restored / last-good restored    text = what
//	backup   a backup was made by hand                     text = its label or name
//
// Arguments of a command are never recorded.

// EventKeep is the ring size.
const EventKeep = 50

// LogEvent is one entry of the ring.
type LogEvent struct {
	T      int64          `json:"t"` // Unix ms
	Kind   string         `json:"kind"`
	Text   string         `json:"text"`
	Source string         `json:"source,omitempty"` // arm: editor | deck-key | startup | env ...
	Detail map[string]any `json:"detail,omitempty"`
}

// EventLog is a bounded, concurrency-safe ring. A nil *EventLog ignores Add and
// reports an empty list.
type EventLog struct {
	mu   sync.Mutex
	now  func() time.Time
	ring []LogEvent // oldest first
}

// NewEventLog returns an empty ring using the real clock.
func NewEventLog() *EventLog { return &EventLog{} }

// SetClock replaces the clock (tests).
func (l *EventLog) SetClock(now func() time.Time) { l.mu.Lock(); l.now = now; l.mu.Unlock() }

// Add records one event.
func (l *EventLog) Add(kind, text, source string, detail map[string]any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t := time.Now()
	if l.now != nil {
		t = l.now()
	}
	l.ring = append(l.ring, LogEvent{T: t.UnixMilli(), Kind: kind, Text: text, Source: source, Detail: detail})
	if len(l.ring) > EventKeep {
		l.ring = l.ring[len(l.ring)-EventKeep:]
	}
}

// List returns the events newest first (never nil).
func (l *EventLog) List() []LogEvent {
	out := []LogEvent{}
	if l == nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.ring) - 1; i >= 0; i-- {
		out = append(out, l.ring[i])
	}
	return out
}

// ShellBase is what the ring records of a command: its base name, nothing else.
func ShellBase(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	return filepath.Base(cmd)
}

// AddSwitch copies a committed switch into the ring.
func (l *EventLog) AddSwitch(e SwitchEvent) {
	l.Add("switch", SwitchText(e), "", map[string]any{"from": e.From, "to": e.To, "reason": e.Reason, "presetId": e.PresetID, "fromName": e.FromName, "toName": e.ToName, "editor": e.Editor})
}

// SwitchText is a switch in words: app names where known, and the editor
// window coming to the front said as such (the last real app stays active).
func SwitchText(e SwitchEvent) string {
	nm := func(id, name string) string {
		if name != "" {
			return name
		}
		return id
	}
	if e.Editor {
		return "2T5 DECK+ window in front, " + nm(e.To, e.ToName) + " stays active"
	}
	from := nm(e.From, e.FromName)
	if from == "" {
		from = "start"
	}
	return from + " \u2192 " + nm(e.To, e.ToName)
}

// ring is the handler's events ring (shared with the app when Deps.Events is set).
func (s *server) ring() *EventLog {
	if s.d.Events != nil {
		return s.d.Events
	}
	s.eventsOnce.Do(func() { s.eventsOwn = NewEventLog() })
	return s.eventsOwn
}

// GET /api/events-log -> {"events":[{t,kind,text,source?,detail?}...],"keep":50}, newest first.
func (s *server) eventsLog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"events": s.ring().List(), "keep": EventKeep})
}

// ---- outside changes held back (wave O) --------------------------------------------------------

// GET /api/pending-external -> {"pending":[{id,preset,file,kind:"change|removal",changes:[plain words],risks:[...],detectedAt}]}
func (s *server) pendingExternal(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"pending": s.d.Store.PendingExternal()})
}

// POST /api/pending-external/{id}/approve {"confirmRisk":true} applies the held
// outside change as one undo step (after a backup). 400 without confirmRisk, 404 unknown id.
func (s *server) approveExternal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ConfirmRisk bool `json:"confirmRisk"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	err := s.d.Store.ApproveExternal(id, body.ConfirmRisk)
	if err != nil {
		log.Printf("[editor] approve outside change %s FAILED: %v", id, err)
	} else {
		log.Printf("[editor] approve outside change %s: applied", id)
	}
	switch {
	case errors.Is(err, preset.ErrConfirmNeeded):
		writeErr(w, http.StatusBadRequest, "confirmRisk", err.Error())
		return
	case errors.Is(err, preset.ErrNoPending):
		writeErr(w, http.StatusNotFound, "id", err.Error())
		return
	case err != nil:
		fail(w, err)
		return
	}
	s.ring().Add("import", "approved an outside change ("+id+")", "editor", map[string]any{"pendingId": id})
	out := map[string]any{"ok": true, "seq": s.seq()}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

// POST /api/pending-external/{id}/reject throws the outside change away: the file is
// restored from the running state after a copy to .backups/rejected-*.
func (s *server) rejectExternal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.d.Store.RejectExternal(id)
	if err != nil {
		log.Printf("[editor] reject outside change %s FAILED: %v", id, err)
	} else {
		log.Printf("[editor] reject outside change %s: file put back", id)
	}
	switch {
	case errors.Is(err, preset.ErrNoPending):
		writeErr(w, http.StatusNotFound, "id", err.Error())
		return
	case err != nil:
		fail(w, err)
		return
	}
	s.ring().Add("restore", "rejected an outside change ("+id+")", "editor", map[string]any{"pendingId": id})
	writeJSON(w, 200, map[string]any{"ok": true, "seq": s.seq()})
}
