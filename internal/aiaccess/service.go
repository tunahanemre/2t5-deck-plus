package aiaccess

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"2t5-deck-plus/internal/datadir"
	"2t5-deck-plus/internal/editorapi"
)

// PreferredPort is tried first so the address is stable between runs; when it
// is taken the OS picks a free port. Clients never need to know either: they
// read the port from the state file.
const PreferredPort = 47860

// EnvDir overrides the folder of the state file (tests, dev server).
const EnvDir = "DECKPLUS_AI_ACCESS_DIR"

// LegacyEnvDir is the name EnvDir had before the rename to 2T5 DECK+; still read when EnvDir is unset.
const LegacyEnvDir = "LD_DECK_AI_ACCESS_DIR"

// StateFile is the per-install file: {enabled, port, token, pid}, mode 0600.
const StateFile = "ai-access.json"

// DefaultDir is ~/Library/Application Support/2t5-deck-plus. Until the app has
// copied the old ~/Library/Application Support/ld-deck folder across (it does so
// at its first start, see datadir.MigrateAppSupport), a terminal client that
// finds only the old folder reads that one.
func DefaultDir() (string, error) {
	if d := os.Getenv(EnvDir); d != "" {
		return d, nil
	}
	if d := os.Getenv(LegacyEnvDir); d != "" {
		return d, nil
	}
	newDir, oldDir, err := datadir.AppSupportDirs()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(newDir); err != nil {
		if fi, e2 := os.Stat(oldDir); e2 == nil && fi.IsDir() {
			return oldDir, nil
		}
	}
	return newDir, nil
}

// State is what the state file holds.
type State struct {
	Version int    `json:"version"`
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port,omitempty"`
	Token   string `json:"token,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

// Options wires a Service.
type Options struct {
	// Dir holds the state file ("" = DefaultDir).
	Dir string
	// Editor is the editor's own handler: every tool is a request to it.
	Editor http.Handler
	// EditorToken is the editor handler's per-launch token ("" = none).
	EditorToken string
	// Exe is the command path shown in the setup line ("" = this executable).
	Exe string
	// Port is the preferred port (0 = PreferredPort).
	Port int
	// Audit records an AI change in the editor's activity log (may be nil).
	Audit func(kind, text, source string, detail map[string]any)
	Logf  func(string, ...any)
}

// Service is the AI listener. The zero value is not usable; call New.
type Service struct {
	o Options

	mu      sync.Mutex
	srv     *http.Server
	ln      net.Listener
	token   string
	changes int
}

// New builds a Service. It does not listen: AI access is off until SetAIAccess(true) or Resume.
func New(o Options) *Service {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Dir == "" {
		if d, err := DefaultDir(); err == nil {
			o.Dir = d
		}
	}
	if o.Port == 0 {
		o.Port = PreferredPort
	}
	if o.Exe == "" {
		if e, err := os.Executable(); err == nil {
			o.Exe = e
		}
	}
	return &Service{o: o}
}

func (s *Service) statePath() string { return filepath.Join(s.o.Dir, StateFile) }

// ReadState reads a state file; a missing one is the off state.
func ReadState(dir string) (State, error) {
	var st State
	b, err := os.ReadFile(filepath.Join(dir, StateFile))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("%s is unreadable: %w", StateFile, err)
	}
	return st, nil
}

// writeState replaces the state file atomically with mode 0600 (the token is in it).
func (s *Service) writeState(st State) error {
	if s.o.Dir == "" {
		return errors.New("no state folder")
	}
	if err := os.MkdirAll(s.o.Dir, 0o700); err != nil {
		return err
	}
	st.Version = 1
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp, err := os.CreateTemp(s.o.Dir, ".ai-access-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.statePath())
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Resume re-opens the listener when the user left the switch on last time.
func (s *Service) Resume() error {
	st, err := ReadState(s.o.Dir)
	if err != nil || !st.Enabled {
		return err
	}
	_, err = s.SetAIAccess(true)
	return err
}

// Close shuts the listener without changing the saved switch (app quit: it resumes next start).
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *Service) stopLocked() {
	if s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		_ = s.srv.Close()
	}
	s.srv, s.ln, s.token = nil, nil, ""
}

// SetAIAccess implements editorapi.AIAccessProvider.
func (s *Service) SetAIAccess(on bool) (editorapi.AIAccessStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !on {
		s.stopLocked()
		s.changes = 0
		// the token is dropped from the file: nothing can use an old one
		if err := s.writeState(State{Enabled: false}); err != nil {
			s.o.Logf("[ai-access] could not save the off state: %v", err)
		}
		s.o.Logf("[ai-access] OFF: listener closed")
		return s.statusLocked(), nil
	}
	if s.srv != nil {
		return s.statusLocked(), nil
	}
	tok, err := newToken() // a fresh token every time the switch goes on; clients read it from the file per call
	if err != nil {
		return s.statusLocked(), err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(s.o.Port))
	if err != nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return s.statusLocked(), err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := s.writeState(State{Enabled: true, Port: port, Token: tok, PID: os.Getpid()}); err != nil {
		ln.Close() // fail closed: no token file, no door
		return s.statusLocked(), fmt.Errorf("could not save the token file: %w", err)
	}
	srv := &http.Server{Handler: s.handler(tok, port), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	s.srv, s.ln, s.token = srv, ln, tok
	go func() { _ = srv.Serve(ln) }()
	s.o.Logf("[ai-access] ON: listening on 127.0.0.1:%d", port)
	return s.statusLocked(), nil
}

// AIStatus implements editorapi.AIAccessProvider.
func (s *Service) AIStatus() editorapi.AIAccessStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Service) statusLocked() editorapi.AIAccessStatus {
	all, ro := Counts()
	st := editorapi.AIAccessStatus{Enabled: s.srv != nil, Tools: all, ReadOnly: ro, Changes: s.changes, Command: s.o.Exe}
	if s.ln != nil {
		st.Address = s.ln.Addr().String()
	}
	st.TokenFile = s.statePath()
	q := shellQuote(s.o.Exe)
	st.SetupLine = "claude mcp add 2t5-deck-plus -- " + q + " mcp"
	cfg, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"2t5-deck-plus": map[string]any{"command": s.o.Exe, "args": []string{"mcp"}}}}, "", "  ")
	st.MCPConfig = string(cfg)
	return st
}

func shellQuote(p string) string {
	if p != "" && strings.IndexFunc(p, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) < 0 {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// handler is the AI door: /ai/v1/ping, /ai/v1/tools, POST /ai/v1/call/{tool}.
func (s *Service) handler(token string, port int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ai/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		all, ro := Counts()
		jsonOut(w, 200, map[string]any{"ok": true, "app": "2t5-deck-plus", "tools": all, "readOnly": ro})
	})
	mux.HandleFunc("GET /ai/v1/tools", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]any{"tools": Tools()}) })
	mux.HandleFunc("POST /ai/v1/call/{tool}", s.call)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, 404, map[string]any{"error": "no such endpoint; the AI door has /ai/v1/ping, /ai/v1/tools and POST /ai/v1/call/{tool}"})
	})
	return guard(token, port, mux)
}

// guard: loopback Host only (DNS rebinding), no browser Origin at all (a web page
// is never a legitimate client), and the bearer token on every request.
func guard(token string, port int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch strings.ToLower(strings.Trim(host, "[]")) {
		case "127.0.0.1", "localhost", "::1":
		default:
			jsonOut(w, 403, map[string]any{"error": "request for another host refused"})
			return
		}
		if r.Header.Get("Origin") != "" {
			jsonOut(w, 403, map[string]any{"error": "browser requests are refused"})
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			jsonOut(w, 401, map[string]any{"error": "missing or wrong token; the AI client reads it from the ai-access.json file in 2t5-deck-plus's Application Support folder"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Service) call(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("tool")
	t, ok := Find(name)
	if !ok {
		jsonOut(w, 404, map[string]any{"error": "no such tool " + name + "; tools: " + toolNames()})
		return
	}
	a := args{}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		jsonOut(w, 400, map[string]any{"error": "body too large or unreadable"})
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		var env struct {
			Arguments map[string]any `json:"arguments"`
		}
		d := json.NewDecoder(bytes.NewReader(body))
		d.UseNumber()
		if err := d.Decode(&env); err != nil {
			jsonOut(w, 400, map[string]any{"error": "body must be JSON like {\"arguments\":{...}}: " + err.Error()})
			return
		}
		a = env.Arguments
		if a == nil {
			a = args{}
		}
	}
	method, path, payload, err := t.build(a)
	if err != nil {
		jsonOut(w, 400, map[string]any{"error": err.Error()})
		return
	}
	code, raw := s.editor(method, path, payload)
	if code >= 200 && code < 300 && t.post != nil {
		if out, err := t.post(a, raw); err == nil {
			raw = out
		}
	}
	if code >= 200 && code < 300 && !t.ReadOnly {
		s.mu.Lock()
		s.changes++
		s.mu.Unlock()
		if s.o.Audit != nil {
			detail := map[string]any{"tool": name}
			for _, k := range []string{"app", "bank", "control", "key"} {
				if v := a.str(k); v != "" {
					detail[k] = v
				}
			}
			s.o.Audit("ai", "AI tool: "+name+auditTarget(a), "ai", detail)
		}
		s.o.Logf("[ai-access] %s %s -> %d", name, auditTarget(a), code)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}

func auditTarget(a args) string {
	var p []string
	for _, k := range []string{"app", "bank", "control", "key"} {
		if v := a.str(k); v != "" {
			p = append(p, v)
		}
	}
	if len(p) == 0 {
		return ""
	}
	return " (" + strings.Join(p, " / ") + ")"
}

// editor runs one request against the editor handler in process, as the editor's own write path does.
func (s *Service) editor(method, path string, payload any) (int, []byte) {
	var rd io.Reader = http.NoBody
	if payload != nil {
		b, _ := json.Marshal(payload)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://127.0.0.1"+path, rd)
	if err != nil {
		return 400, []byte(`{"error":"bad request"}`)
	}
	req.Host = "127.0.0.1"
	req.RequestURI = path
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set(editorapi.EditorHeader, "1")
	if s.o.EditorToken != "" {
		req.Header.Set(editorapi.TokenHeader, s.o.EditorToken)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.o.Editor.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}
