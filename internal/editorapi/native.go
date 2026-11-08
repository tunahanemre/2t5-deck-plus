package editorapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"2t5-deck-plus/internal/preset"
)

// NativeIO is the dialog-free core of the packaged app's export / import.
// WKWebView cannot download a blob, so the app binds ExportPresets /
// ImportPresetsFromDialog / ApplyImport (app/app.go), which show the native
// file dialogs and call this. It uses the very same Store.Export / Import the
// HTTP endpoints use. An import is two steps: PreviewImport reads the chosen
// file and returns the diff (a dry run) with a one-use token; ApplyImport
// applies exactly the bytes that were previewed.
type NativeIO struct {
	Store *preset.Store
	Now   func() time.Time // nil = time.Now

	mu      sync.Mutex
	pending map[string]pendingImport
}

type pendingImport struct {
	data []byte
	at   time.Time
	// riskSig is the fingerprint of the risk set the user saw in the preview: a
	// confirmation is only good for THAT set (see ApplyImportOpts).
	riskSig string
}

const (
	pendingTTL = 10 * time.Minute
	maxPending = 4
)

// ErrUnknownToken: the token is unknown, already used or expired.
var ErrUnknownToken = errors.New("this import is no longer pending: choose the file again")

// ErrRiskChanged / RiskChangedError live in internal/preset (the check runs inside the
// import's own commit, so nothing can change between the check and the write).
var ErrRiskChanged = preset.ErrRiskChanged

// RiskChangedError carries the new report of an import whose risk set differs
// from the one that was previewed.
type RiskChangedError = preset.RiskChangedError

// ImportPreview is the dry-run answer of ImportPresetsFromDialog.
type ImportPreview struct {
	// Token is empty when the user cancelled the dialog (nothing else is set).
	Token  string              `json:"token"`
	Path   string              `json:"path,omitempty"`
	Name   string              `json:"name,omitempty"`
	DryRun bool                `json:"dryRun"`
	Files  []preset.ImportFile `json:"files"`
	// Risk sums what the added / changed assignments can do; NeedsConfirm:
	// ApplyImport refuses until the user confirmed (shell or destructive actions).
	Risk         preset.ImportRisk `json:"risk"`
	NeedsConfirm bool              `json:"needsConfirm"`
	// Dropped: what an apply with skipRisky would leave out (shell and
	// destructive assignments); only filled when the preview was asked with skipRisky.
	Dropped []preset.DroppedAssignment `json:"dropped"`
	// Inherited: assignments that are dangerous only through what they inherit
	// from Global (settings.actionDefaults or the Global assignment).
	Inherited []preset.InheritedRisk `json:"inherited"`
}

// ImportResult is the answer of ApplyImport.
type ImportResult struct {
	OK     bool                `json:"ok"`
	DryRun bool                `json:"dryRun"`
	Files  []preset.ImportFile `json:"files"`
	Seq    int                 `json:"seq"`
	// Dropped: assignments an apply with skipRisky left out (always an array).
	Dropped []preset.DroppedAssignment `json:"dropped"`
}

func (n *NativeIO) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// ExportBytes renders the zip GET /api/export would send, and its suggested
// file name. app is "all", "" or a preset id.
func (n *NativeIO) ExportBytes(app string) ([]byte, string, error) { return n.Store.Export(app) }

// WriteExport saves an export to path (".zip" is appended when missing),
// atomically, and returns the path actually written.
func (n *NativeIO) WriteExport(path string, data []byte) (string, error) {
	if path == "" {
		return "", errors.New("no file chosen")
	}
	if !strings.EqualFold(filepath.Ext(path), ".zip") {
		path += ".zip"
	}
	if err := preset.WriteAtomic(path, data); err != nil {
		return "", fmt.Errorf("could not save the export: %w", err)
	}
	return path, nil
}

// PreviewImport reads the zip at path (max 5 MB), validates it and returns
// the dry-run diff plus a token for ApplyImport. Nothing is written.
func (n *NativeIO) PreviewImport(path string) (ImportPreview, error) {
	f, err := os.Open(path)
	if err != nil {
		return ImportPreview{}, fmt.Errorf("could not open the file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImportBytes+1))
	if err != nil {
		return ImportPreview{}, fmt.Errorf("could not read the file: %w", err)
	}
	if len(data) > maxImportBytes {
		return ImportPreview{}, errors.New("the file is larger than 5 MB")
	}
	if len(data) == 0 {
		return ImportPreview{}, errors.New("the file is empty")
	}
	return n.PreviewData(data, path, filepath.Base(path), preset.ImportOptions{})
}

// PreviewData is PreviewImport for bytes already in hand (the HTTP upload):
// validate, dry-run, store the bytes under a one-use token (10 minutes, at most
// 4 pending, the oldest dropped). o.SkipRisky previews what a skipRisky apply
// would leave out.
func (n *NativeIO) PreviewData(data []byte, path, name string, o preset.ImportOptions) (ImportPreview, error) {
	rep, err := n.Store.ImportOpts(data, true, o)
	if err != nil {
		return ImportPreview{}, err
	}
	tok, err := newToken()
	if err != nil {
		return ImportPreview{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pending == nil {
		n.pending = map[string]pendingImport{}
	}
	now := n.now()
	for k, p := range n.pending {
		if now.Sub(p.at) > pendingTTL {
			delete(n.pending, k)
		}
	}
	for len(n.pending) >= maxPending { // drop the oldest
		oldest, ot := "", now
		for k, p := range n.pending {
			if oldest == "" || p.at.Before(ot) {
				oldest, ot = k, p.at
			}
		}
		delete(n.pending, oldest)
	}
	base, _ := n.Store.ImportOpts(data, true, preset.ImportOptions{}) // the plain report: what a confirmation is for
	n.pending[tok] = pendingImport{data: data, at: now, riskSig: preset.RiskSignature(base)}
	return ImportPreview{Token: tok, Path: path, Name: name, DryRun: true, Files: rep.Files, Risk: rep.Risk, NeedsConfirm: rep.NeedsConfirm, Dropped: rep.Dropped, Inherited: rep.Inherited}, nil
}

// ApplyImport applies the import that PreviewImport returned the token for
// (one undo step, preceded by a backup). The token is single use, except that
// a refusal for risk (*preset.RiskError: shell or destructive actions without
// confirmRisk) leaves it valid, so the UI can ask and call again with
// confirmRisk true.
func (n *NativeIO) ApplyImport(token string, confirmRisk bool) (ImportResult, error) {
	return n.ApplyImportOpts(token, preset.ImportOptions{ConfirmRisk: confirmRisk})
}

// ApplyImportOpts is ApplyImport with all the caller's choices; SkipRisky
// installs everything except added or changed shell and destructive
// assignments (reported in ImportResult.Dropped) and needs no confirmation.
func (n *NativeIO) ApplyImportOpts(token string, o preset.ImportOptions) (ImportResult, error) {
	n.mu.Lock()
	p, ok := n.pending[token]
	if ok {
		delete(n.pending, token)
	}
	n.mu.Unlock()
	if !ok || n.now().Sub(p.at) > pendingTTL {
		return ImportResult{}, ErrUnknownToken
	}
	if o.ConfirmRisk {
		o.ExpectRiskSig = p.riskSig // a confirmation covers the risk set the user was shown, checked inside the commit
	}
	rep, err := n.Store.ImportOpts(p.data, false, o)
	if err != nil {
		var rc *preset.RiskChangedError
		if errors.As(err, &rc) {
			n.mu.Lock()
			if n.pending == nil {
				n.pending = map[string]pendingImport{}
			}
			p.riskSig = preset.RiskSignature(rc.Report) // the next confirmation is for the report just shown
			n.pending[token] = p
			n.mu.Unlock()
			return ImportResult{}, err
		}
		var re *preset.RiskError
		if errors.As(err, &re) {
			n.mu.Lock()
			if n.pending == nil {
				n.pending = map[string]pendingImport{}
			}
			n.pending[token] = p // keep the original time: the TTL does not restart
			n.mu.Unlock()
		}
		return ImportResult{}, err
	}
	return ImportResult{OK: true, DryRun: false, Files: rep.Files, Seq: n.Store.Snapshot().Seq, Dropped: rep.Dropped}, nil
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
