package editorapi

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"2t5-deck-plus/internal/preset"
)

// Wave B endpoints: icons, copy, restore defaults, export / import / backups,
// coverage, search, history, warnings.

const maxImportBytes = 5 << 20

func (s *server) routesB(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/icon/{file}", s.iconMaster)
	mux.HandleFunc("GET /api/appicon/{file}", s.appIcon)
	mux.HandleFunc("POST /api/preset/{id}/copy", s.copy)
	mux.HandleFunc("POST /api/preset/{id}/copy-from", s.copy)
	mux.HandleFunc("POST /api/preset/{id}/restore", s.restore)
	mux.HandleFunc("POST /api/preset/{id}/reset-defaults", s.resetDefaults)
	mux.HandleFunc("GET /api/preset/{id}/export", s.exportOne)
	mux.HandleFunc("GET /api/export", s.export)
	mux.HandleFunc("POST /api/import", s.importZip)
	for _, base := range []string{"/api/backups", "/api/snapshots"} {
		mux.HandleFunc("GET "+base, s.backups)
		mux.HandleFunc("POST "+base, s.backupNow)
		mux.HandleFunc("POST "+base+"/{name}/restore", s.restoreBackup)
		mux.HandleFunc("GET "+base+"/{name}/diff", s.backupDiff)
	}
	mux.HandleFunc("GET /api/coverage", s.coverage)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("GET /api/history", s.history)
	mux.HandleFunc("POST /api/history/jump", s.jump)
	mux.HandleFunc("GET /api/warnings", s.warnings)
}

func (s *server) seq() int { return s.d.Store.Snapshot().Seq }

func (s *server) copy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From    string `json:"from"`
		Scope   string `json:"scope"`
		Banks   string `json:"banks"` // copy-from alias of scope: "all" | "bank"
		Bank    string `json:"bank"`
		Control string `json:"control"`
		Mode    string `json:"mode"`
		Force   bool   `json:"force"`
		DryRun  bool   `json:"dryRun"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Scope == "" {
		body.Scope = body.Banks
	}
	if body.DryRun || truthy(r, "dryRun", "dry-run", "dry_run") {
		// What Copy would do, written nowhere: no undo step, no backup.
		res, err := s.d.Store.CopyPlan(r.PathValue("id"), body.From, body.Scope, body.Bank, body.Control, body.Mode, body.Force)
		if err != nil {
			fail(w, err)
			return
		}
		byReason := map[string]int{}
		for _, sk := range res.Skipped {
			byReason[sk.Reason]++
		}
		writeJSON(w, 200, map[string]any{"ok": true, "dryRun": true, "seq": s.seq(), "copied": res.Copied, "removed": res.Removed,
			"skipped": res.Skipped, "skippedByReason": byReason})
		return
	}
	res, err := s.d.Store.Copy(r.PathValue("id"), body.From, body.Scope, body.Bank, body.Control, body.Mode, body.Force)
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"ok": true, "seq": s.seq(), "copied": res.Copied, "skipped": res.Skipped}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

func (s *server) restore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope   string `json:"scope"`
		Bank    string `json:"bank"`
		Control string `json:"control"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	info, err := s.d.Store.RestoreDefaultsInfo(id, body.Scope, body.Bank, body.Control)
	if err != nil {
		fail(w, err)
		return
	}
	if body.Scope == "control" {
		s.effectiveReply(w, id, body.Bank, body.Control)
		return
	}
	s.restoreReply(w, info)
}

func (s *server) resetDefaults(w http.ResponseWriter, r *http.Request) {
	info, err := s.d.Store.RestoreDefaultsInfo(r.PathValue("id"), "app", "", "")
	if err != nil {
		fail(w, err)
		return
	}
	s.restoreReply(w, info)
}

// restoreReply is okReply plus what the preset was returned to, so the UI can
// word the toast ("Restored the Google Chrome template" / "Restored defaults").
func (s *server) restoreReply(w http.ResponseWriter, info *preset.RestoreInfo) {
	out := map[string]any{"ok": true, "seq": s.seq(), "restoredTo": info}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

func sendZip(w http.ResponseWriter, data []byte, name string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *server) export(w http.ResponseWriter, r *http.Request) {
	data, name, err := s.d.Store.Export(r.URL.Query().Get("app"))
	if err != nil {
		fail(w, err)
		return
	}
	sendZip(w, data, name)
}

func (s *server) exportOne(w http.ResponseWriter, r *http.Request) {
	data, name, err := s.d.Store.Export(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	sendZip(w, data, name)
}

func truthy(r *http.Request, names ...string) bool {
	q := r.URL.Query()
	for _, n := range names {
		if _, ok := q[n]; ok {
			v := strings.ToLower(q.Get(n))
			return v != "0" && v != "false" && v != "no"
		}
	}
	return false
}

// failImport maps bundle problems to 400 {error, problems}.
func failImport(w http.ResponseWriter, err error) {
	var ie *preset.ImportError
	if errors.As(err, &ie) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": ie.Error(), "problems": ie.Problems})
		return
	}
	fail(w, err)
}

func readUpload(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	var rd io.Reader = r.Body
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "", "bad upload: "+err.Error())
			return nil, false
		}
		var part *multipart.Part
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "file" {
				part = p
				break
			}
		}
		if part == nil {
			writeErr(w, http.StatusBadRequest, "file", `the upload has no "file" part`)
			return nil, false
		}
		rd = part
	}
	b, err := io.ReadAll(rd)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "", "upload is larger than 5 MB")
		} else {
			writeErr(w, http.StatusBadRequest, "", "bad upload: "+err.Error())
		}
		return nil, false
	}
	if len(b) == 0 {
		writeErr(w, http.StatusBadRequest, "", "empty upload")
		return nil, false
	}
	return b, true
}

// importOpts reads the import choices from the query: confirmRisk, skipRisky.
func importOpts(r *http.Request) preset.ImportOptions {
	return preset.ImportOptions{
		ConfirmRisk: truthy(r, "confirmRisk", "confirm-risk", "confirm_risk"),
		SkipRisky:   truthy(r, "skipRisky", "skip-risky", "skip_risky"),
	}
}

// native is the import token store, shared with the packaged app's native
// import (Deps.Native) or private to this handler.
func (s *server) native() *NativeIO {
	if s.d.Native != nil {
		return s.d.Native
	}
	s.nativeOnce.Do(func() { s.nativeOwn = &NativeIO{Store: s.d.Store} })
	return s.nativeOwn
}

// importZip is POST /api/import, in two steps like the native import:
//
//	?dryRun=1 (+ the zip)   -> 200 {ok,dryRun:true,token,files,risk,needsConfirm,dropped,seq}; nothing written
//	?token=T [&confirmRisk=1] [&skipRisky=1]  -> applies exactly the previewed bytes (one undo step)
//
// A real import without a valid token is refused 409 {needsToken:true} (with the
// report when the zip came along): nobody applies an import they have not
// previewed. A shell or destructive import still needs confirmRisk=1 (or skipRisky=1).
func (s *server) importZip(w http.ResponseWriter, r *http.Request) {
	opts := importOpts(r)
	if truthy(r, "dryRun", "dry-run", "dry_run") {
		data, ok := readUpload(w, r)
		if !ok {
			return
		}
		pv, err := s.native().PreviewData(data, "", "upload.zip", opts)
		if err != nil {
			failImport(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "dryRun": true, "token": pv.Token, "files": pv.Files, "risk": pv.Risk,
			"needsConfirm": pv.NeedsConfirm, "dropped": pv.Dropped, "inherited": pv.Inherited, "seq": s.seq()})
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		out := map[string]any{"error": "import needs a dry run first: send the zip with dryRun=1, review the report, then apply with the token it returns (?token=...)",
			"ok": false, "needsToken": true, "seq": s.seq()}
		if r.ContentLength != 0 {
			data, ok := readUpload(w, r)
			if !ok {
				return
			}
			rep, err := s.d.Store.ImportOpts(data, true, opts)
			if err != nil {
				failImport(w, err)
				return
			}
			out["files"], out["risk"], out["needsConfirm"], out["dropped"], out["inherited"] = rep.Files, rep.Risk, rep.NeedsConfirm, rep.Dropped, rep.Inherited
		} else if _, ok := readUpload(w, r); !ok { // an empty body is the 400 it always was
			return
		}
		writeJSON(w, http.StatusConflict, out)
		return
	}
	res, err := s.native().ApplyImportOpts(token, opts)
	if err != nil {
		var re *preset.RiskError
		var rc *RiskChangedError
		switch {
		case errors.As(err, &rc):
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "ok": false, "riskChanged": true, "needsConfirm": true,
				"files": rc.Report.Files, "risk": rc.Report.Risk, "inherited": rc.Report.Inherited, "seq": s.seq()})
		case errors.Is(err, ErrUnknownToken):
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "ok": false, "needsToken": true, "seq": s.seq()})
		case errors.As(err, &re): // 409: the report says what it would install; the token stays valid
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "ok": false, "dryRun": false,
				"needsConfirm": true, "files": re.Report.Files, "risk": re.Report.Risk, "inherited": re.Report.Inherited, "seq": s.seq()})
		default:
			failImport(w, err)
		}
		return
	}
	var risk preset.ImportRisk
	for _, f := range res.Files {
		risk = risk.Add(f.Risk)
	}
	s.ring().Add("import", fmt.Sprintf("imported %d preset file(s)", len(res.Files)), "editor", map[string]any{"files": len(res.Files), "dropped": len(res.Dropped)})
	out := map[string]any{"ok": true, "dryRun": false, "files": res.Files, "risk": risk, "needsConfirm": false, "dropped": res.Dropped, "seq": s.seq()}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

func (s *server) backups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"backups": s.d.Store.Backups(), "keep": 14})
}

// backupNow makes a manual backup (wave D). Not an undo step.
func (s *server) backupNow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label string `json:"label"`
	}
	if r.ContentLength != 0 && !decode(w, r, &body) { // the body is optional: {"label":"before the shoot"}
		return
	}
	b, ok, err := s.d.Store.BackupNowLabelled(body.Label)
	if err != nil {
		fail(w, err)
		return
	}
	if !ok {
		writeJSON(w, 200, map[string]any{"ok": true, "made": false})
		return
	}
	text := b.Name
	if b.Label != "" {
		text = b.Label
	}
	s.ring().Add("backup", text, "editor", map[string]any{"name": b.Name})
	writeJSON(w, 201, map[string]any{"ok": true, "made": true, "backup": b})
}

// GET /api/backups/{name}/diff: per preset, how the backup differs from now
// (what restoring it would add, remove and change). Writes nothing.
func (s *server) backupDiff(w http.ResponseWriter, r *http.Request) {
	d, err := s.d.Store.DiffBackup(r.PathValue("name"))
	switch {
	case errors.Is(err, preset.ErrNoBackup):
		writeErr(w, http.StatusNotFound, "name", err.Error())
		return
	case err != nil:
		failImport(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "diff": d})
}

func (s *server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	rep, err := s.d.Store.RestoreBackupOpts(r.PathValue("name"), importOpts(r))
	var re *preset.RiskError
	switch {
	case errors.As(err, &re): // a backup that is not ours and would install risk: judged like an import
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "ok": false, "needsConfirm": true,
			"files": re.Report.Files, "risk": re.Report.Risk, "inherited": re.Report.Inherited, "dropped": re.Report.Dropped, "seq": s.seq()})
		return
	case errors.Is(err, preset.ErrNoBackup):
		writeErr(w, http.StatusNotFound, "name", err.Error())
		return
	case err != nil:
		failImport(w, err)
		return
	}
	s.ring().Add("restore", "backup "+r.PathValue("name"), "editor", map[string]any{"files": len(rep.Files), "skipped": len(rep.Skipped)})
	out := map[string]any{"ok": true, "files": rep.Files, "skipped": rep.Skipped, "dropped": rep.Dropped, "risk": rep.Risk, "seq": s.seq()}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

func (s *server) coverage(w http.ResponseWriter, r *http.Request) {
	app := r.URL.Query().Get("app")
	if app == "" {
		writeErr(w, http.StatusBadRequest, "app", "app is required")
		return
	}
	cov, ok := s.d.Store.CoverageLens(app, ControlIDs())
	if !ok {
		writeErr(w, http.StatusNotFound, "app", "no such preset "+app)
		return
	}
	writeJSON(w, 200, cov)
}

func (s *server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeErr(w, http.StatusBadRequest, "limit", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	hits, trunc := s.d.Store.Snapshot().Search(q, limit)
	writeJSON(w, 200, map[string]any{"q": q, "hits": hits, "truncated": trunc})
}

func (s *server) history(w http.ResponseWriter, r *http.Request) {
	entries, cur := s.d.Store.History()
	type row struct {
		Index int    `json:"index"`
		Label string `json:"label"`
		T     int64  `json:"t"`
		Done  bool   `json:"done"`
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, row{e.Index, e.Label, e.T.UnixMilli(), e.Done})
	}
	writeJSON(w, 200, map[string]any{"current": cur, "pos": cur, "entries": rows, "seq": s.seq()})
}

func (s *server) jump(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Index *int `json:"index"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Index == nil {
		writeErr(w, http.StatusBadRequest, "index", "index is required")
		return
	}
	label, err := s.d.Store.JumpTo(*body.Index)
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"ok": true, "label": label, "current": *body.Index, "seq": s.seq()}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

func (s *server) warnings(w http.ResponseWriter, r *http.Request) {
	ws := s.d.Store.Warnings()
	if s.d.ArmMark != nil {
		if e := s.d.ArmMark.LastError(); e != "" {
			ws = append(ws, preset.Warning{Severity: "warn", Kind: "disarm-marker",
				Message: "could not save the disarmed state (" + e + "): the next launch may come up armed. The app keeps retrying; check that the presets folder is writable."})
		}
	}
	errs, warn, hint := 0, 0, 0
	for _, x := range ws {
		switch x.Severity {
		case "error":
			errs++
		case "warn":
			warn++
		default:
			hint++
		}
	}
	out := map[string]any{"warnings": ws, "counts": map[string]int{"error": errs, "warn": warn, "hint": hint}}
	if c := s.d.Store.Canary(); c != nil {
		out["canary"] = c
	}
	writeJSON(w, 200, out)
}
