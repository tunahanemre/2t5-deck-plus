package editorapi

import (
	"net/http"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/preset"
)

// Slider-mode (Hue / Sat / Lum) layers of the P1-P8 sliders and P1btn-P8btn keys.
//
// The data is ordinary preset data: a mode is a bank. For an app (and Global)
// the banks are "pm-hue", "pm-sat", "pm-lum"; for the Resolve preset they are
// "{page}-pm-{mode}" ("col-pm-sat", "edit-pm-lum"; page defaults to "col").
// These routes only translate (mode, page) into that bank and wrap the existing
// store calls, so every write is one undo/redo step in the same history as any
// other edit, and GET /api/preset/{id} shows the banks as they are.
//
//	GET    /api/preset/{id}/modes?control=P1[&page=col]
//	PUT    /api/preset/{id}/modes          {mode, control, assignment[, page]}
//	DELETE /api/preset/{id}/modes?mode=sat&control=P1[&page=col]
//	GET    /api/preset/{id}/modes/standard                    (Resolve only: what the standard holds, per bank applied or not)
//	POST   /api/preset/{id}/modes/standard {mode?}            (Resolve only: opt in; mode sat|lum|omitted = both)
func (s *server) registerModes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/preset/{id}/modes", s.modesGet)
	mux.HandleFunc("PUT /api/preset/{id}/modes", s.modesPut)
	mux.HandleFunc("DELETE /api/preset/{id}/modes", s.modesDelete)
	mux.HandleFunc("GET /api/preset/{id}/modes/standard", s.modesStandardGet)
	mux.HandleFunc("POST /api/preset/{id}/modes/standard", s.modesStandard)
}

// modeBankFor is the bank a (preset, mode, page) is stored in.
func modeBankFor(id, mode, page string) string {
	if id == preset.ResolveID {
		if page == "" {
			page = "col"
		}
		return config.ModeBankOf(page, mode)
	}
	return config.PModeBank(mode)
}

func (s *server) modeArgs(w http.ResponseWriter, id, mode, control, page string) (string, bool) {
	if !config.ValidPMode(mode) {
		writeErr(w, http.StatusBadRequest, "mode", `mode must be "hue", "sat" or "lum"`)
		return "", false
	}
	if !config.IsPControl(control) {
		writeErr(w, http.StatusBadRequest, "control", "only P1-P8 and P1btn-P8btn have per-mode assignments")
		return "", false
	}
	if id == "global" {
		id = preset.GlobalID
	}
	if _, ok := s.d.Store.Snapshot().Preset(id); !ok {
		writeErr(w, http.StatusNotFound, "id", "no such app")
		return "", false
	}
	if page != "" && !pageNameOK(page) {
		writeErr(w, http.StatusBadRequest, "page", "page must be a Resolve page name such as col or edit")
		return "", false
	}
	return modeBankFor(id, mode, page), true
}

func pageNameOK(p string) bool {
	if p == "" {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

type modeView struct {
	Mode string `json:"mode"`
	Bank string `json:"bank"`
	// Own is true when this mode has its own assignment for the control; false
	// means the control falls back (Fallback) while this mode is live.
	Own bool `json:"own"`
	// Effective is the resolved assignment of the mode bank (same shape as the
	// "effective" of an assign reply), absent when Own is false.
	Effective any `json:"effective,omitempty"`
}

func (s *server) modesGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	control, page := r.URL.Query().Get("control"), r.URL.Query().Get("page")
	if id == "global" {
		id = preset.GlobalID
	}
	if _, ok := s.modeArgs(w, id, "hue", control, page); !ok {
		return
	}
	snap := s.d.Store.Snapshot()
	views := make([]modeView, 0, 3)
	for _, m := range config.PModes {
		bank := modeBankFor(id, m, page)
		v := modeView{Mode: m, Bank: bank}
		if p, ok := snap.Preset(id); ok {
			if _, has := p.Banks[bank][control]; has {
				v.Own = true
				if e, ok := snap.Effective(id, bank, control); ok {
					v.Effective = e
				}
			}
		}
		views = append(views, v)
	}
	// What fires when a mode has no assignment of its own: the ordinary
	// resolution (modifier none, layer 1, no mode).
	fallback := map[string]any{"assigned": false}
	if id == preset.ResolveID {
		pg := page
		if pg == "" {
			pg = "col"
		}
		if eb, ok := snap.ResolveLegacy(pg, 1, control); ok {
			fallback = map[string]any{"assigned": true, "bank": eb.Bank, "action": eb.Action, "source": eb.Source}
		}
	} else if eb, ok := snap.ResolveForModeMod(id, "", "", 1, control); ok {
		fallback = map[string]any{"assigned": true, "bank": eb.Bank, "action": eb.Action, "source": eb.Source}
	}
	out := map[string]any{"ok": true, "app": id, "control": control, "page": pageOrCol(id, page), "modes": views, "fallback": fallback,
		"note": "A mode without its own assignment does what the fallback does. Held modifiers still win over the mode."}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

func pageOrCol(id, page string) string {
	if id != preset.ResolveID {
		return ""
	}
	if page == "" {
		return "col"
	}
	return page
}

func (s *server) modesPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode       string         `json:"mode"`
		Control    string         `json:"control"`
		Page       string         `json:"page"`
		Assignment map[string]any `json:"assignment"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	bank, ok := s.modeArgs(w, id, body.Mode, body.Control, body.Page)
	if !ok {
		return
	}
	if body.Assignment == nil {
		writeErr(w, http.StatusBadRequest, "assignment", "assignment is required")
		return
	}
	if err := s.d.Store.Assign(id, bank, body.Control, body.Assignment); err != nil {
		fail(w, err)
		return
	}
	s.effectiveReply(w, id, bank, body.Control)
}

func (s *server) modesDelete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := r.PathValue("id")
	bank, ok := s.modeArgs(w, id, q.Get("mode"), q.Get("control"), q.Get("page"))
	if !ok {
		return
	}
	if err := s.d.Store.Unassign(id, bank, q.Get("control")); err != nil {
		fail(w, err)
		return
	}
	s.effectiveReply(w, id, bank, q.Get("control"))
}

// modesStandard copies the Resolve STANDARD slider-mode mapping into the Resolve
// preset: the opt-in. Only the banks missing or being replaced named by the
// request are touched (mode: "sat" | "lum" | omitted = both); nothing else.
func (s *server) modesStandard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != preset.ResolveID {
		writeErr(w, http.StatusBadRequest, "id", "the standard slider-mode mapping exists for DaVinci Resolve only")
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	banks, err := s.d.Store.ApplyStandardModes(body.Mode)
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"ok": true, "banks": banks, "seq": s.d.Store.Snapshot().Seq}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

func (s *server) modesStandardGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != preset.ResolveID {
		writeErr(w, http.StatusBadRequest, "id", "the standard slider-mode mapping exists for DaVinci Resolve only")
		return
	}
	cur, _ := s.d.Store.Snapshot().Preset(id)
	std := preset.StandardModeBanks()
	banks := []map[string]any{}
	for _, bn := range preset.StandardModeBankNames() {
		mode, _ := config.ParseModeBank(bn)
		applied := cur != nil && len(cur.Banks[bn]) == len(std[bn]) && len(std[bn]) > 0
		banks = append(banks, map[string]any{"bank": bn, "mode": mode, "controls": len(std[bn]), "applied": applied})
	}
	out := map[string]any{"ok": true, "banks": banks,
		"note": "Hue mode keeps Colour balance toward a hue. Sat and Lum drag the Hue vs Sat / Hue vs Lum curve points and need their calibration targets (hvs_*, hvl_*)."}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}
