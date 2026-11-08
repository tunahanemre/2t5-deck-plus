package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"2t5-deck-plus/internal/aiaccess"
	"2t5-deck-plus/internal/editorapi"
	"2t5-deck-plus/internal/preset"
)

// Config for New.
type Config struct {
	Addr     string
	Static   string
	Data     string
	SeedFrom string
	Live     string
	Sim      bool
	Logf     func(string, ...any)
	// NoPoll disables the external-edit poll (tests).
	NoPoll bool
	// Token, when set, is the per-launch API token the handler (and the
	// dev-only POSTs) require as X-LD-Token on every mutating request.
	Token string
	// State, when set, is an internal-state folder (the app's ~/Library/Application Support/2t5-deck-plus/state shape: trust/manifest.json + lastgood) so the dev server runs the same startup outside-change check as the packaged app. Use a COPY.
	State string
	// AIDir is the folder of the AI-access state file (ai-access.json); AIExe the command path shown in the setup line. Empty AIDir = a temp folder.
	AIDir, AIExe string
	// EngineState makes the dev server behave like the packaged engine for the deck lamps: /api/live reports
	// sliderMode / customMode and POST /api/dev/deck-press {"control":"Hue|Sat|Lum|CustomMode"} changes them and
	// publishes a deck-state event, exactly as the engine does on a real press.
	EngineState bool
}

// Server is the assembled dev server.
type Server struct {
	Handler http.Handler
	DataDir string
	Store   *preset.Store
	Bus     *editorapi.Bus
	Sim     *editorapi.Simulator
	Dev     *DevState
	sim     bool
}

// DevState is the fake desktop: running apps and the frontmost one.
type DevState struct {
	mu      sync.Mutex
	front   editorapi.AppRef
	running []editorapi.AppRef
	// since / editorFocused feed GET /api/live "why".
	since         int64
	editorFocused bool
	perms         editorapi.PermState
	layer         int // fake engine layer (POST /api/dev/layer), 0 = unknown
	// sw / snap feed GET /api/switches: every dev frontmost change is noted.
	sw    *editorapi.SwitchLog
	snap  func() *preset.Snapshot
	token string // per-launch API token ("" = none)
	// engine-like deck lamp state (Config.EngineState)
	engState bool
	eMode    string
	eCustom  bool

	// fake preflight environment (POST /api/dev/preflight flips it)
	envNoWS01  bool
	envResolve bool

	// fake Python / Resolve / deck / other app / macOS for GET /api/dependencies
	sys *devSys
}

// WS01 implements editorapi.EnvProvider: mounted unless flipped.
func (d *DevState) WS01() (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.envNoWS01 {
		return false, "/Volumes/2T5-WS-01 does not exist: not mounted (dev fake)"
	}
	return true, "/Volumes/2T5-WS-01 is mounted (dev fake)"
}

// LDResolveRunning implements editorapi.EnvProvider: not running unless flipped.
func (d *DevState) LDResolveRunning() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.envResolve }

// note records the current fake frontmost state in the switch log.
func (d *DevState) note() {
	d.mu.Lock()
	front, focused := d.front, d.editorFocused
	d.mu.Unlock()
	var snap *preset.Snapshot
	if d.snap != nil {
		snap = d.snap()
	}
	d.sw.NoteFront(time.Now(), front, focused, snap)
}

// NewDevState starts with Resolve frontmost.
func NewDevState() *DevState {
	run := []editorapi.AppRef{
		{ID: preset.ResolveID, Name: "DaVinci Resolve"},
		{ID: "com.google.Chrome", Name: "Google Chrome"},
		{ID: preset.FinderID, Name: "Finder"},
		{ID: "com.apple.Safari", Name: "Safari"},
		{ID: "com.tinyspeck.slackmacgap", Name: "Slack"},
	}
	return &DevState{sys: newDevSys(), front: run[0], running: run, since: time.Now().UnixMilli(),
		perms: editorapi.PermState{Accessibility: true, ScreenRecording: true, AutomationFinder: "unknown"}}
}

// FrontInfo implements editorapi.FrontInfoProvider.
func (d *DevState) FrontInfo() editorapi.FrontInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return editorapi.FrontInfo{EditorFocused: d.editorFocused, Since: d.since}
}

func (d *DevState) setEditorFocused(on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.editorFocused != on {
		d.editorFocused = on
		d.since = time.Now().UnixMilli()
	}
}

// Permissions implements editorapi.PermissionsProvider (a fake: all granted,
// Finder automation unknown; POST /api/dev/permissions changes it).
func (d *DevState) Permissions() editorapi.PermState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.perms
}

func (d *DevState) setPerms(acc, scr *bool, fin *string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if acc != nil {
		d.perms.Accessibility = *acc
	}
	if scr != nil {
		d.perms.ScreenRecording = *scr
	}
	if fin != nil {
		d.perms.AutomationFinder = *fin
	}
}

// devArm is the fake safety switch (memory only, starts armed).
type devArm struct{ armed atomic.Bool }

func (a *devArm) Armed() bool     { return a.armed.Load() }
func (a *devArm) SetArmed(b bool) { a.armed.Store(b) }

// ArmCoverage mirrors the real executor's list (appdeck.Executor.ArmCoverage).
func (a *devArm) ArmCoverage() []string {
	return []string{"keys", "mouse", "scroll", "shell", "sidecar-writes", "finder-tags"}
}

// devEngine fakes the engine's config state: every store change counts as
// applied at once, unless a compose failure was injected (POST
// /api/dev/compose-error).
type devEngine struct {
	mu   sync.Mutex
	st   editorapi.EngineState
	fail string
	st0  *preset.Store
}

func newDevEngine(st *preset.Store) *devEngine {
	e := &devEngine{st0: st}
	e.st = editorapi.EngineState{AppliedSeq: st.Snapshot().Seq, AppliedAt: time.Now().UnixMilli()}
	st.OnChange(func(s *preset.Snapshot) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.fail != "" {
			e.st.ComposeError = e.fail
			return
		}
		e.st = editorapi.EngineState{AppliedSeq: s.Seq, AppliedAt: time.Now().UnixMilli()}
	})
	return e
}

func (e *devEngine) setFail(msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fail = msg
	if msg == "" {
		e.st = editorapi.EngineState{AppliedSeq: e.st0.Snapshot().Seq, AppliedAt: time.Now().UnixMilli()}
		return
	}
	e.st.ComposeError = msg
}

func (e *devEngine) EngineState() editorapi.EngineState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.st
}

func (e *devEngine) WaitApplied(int, time.Duration) editorapi.EngineState { return e.EngineState() }

// Frontmost implements editorapi.FrontmostProvider.
func (d *DevState) Frontmost() editorapi.AppRef { d.mu.Lock(); defer d.mu.Unlock(); return d.front }

// Running implements editorapi.RunningProvider.
func (d *DevState) Running() []editorapi.AppRef {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]editorapi.AppRef(nil), d.running...)
}

// Live implements editorapi.LiveProvider: Resolve frontmost = sidecar connected.
func (d *DevState) Live() editorapi.LiveState {
	st := editorapi.LiveState{EngineRunning: true, Sidecar: "waiting"}
	if d.Frontmost().ID == preset.ResolveID {
		st.Sidecar = "connected"
	}
	if s := d.sys.scriptingState(); s != "" {
		st.Sidecar = s
	}
	d.mu.Lock()
	st.Layer = d.layer
	if d.engState {
		st.SliderMode, st.CustomMode = d.eMode, d.eCustom
	}
	d.mu.Unlock()
	return st
}

func (d *DevState) setFront(a editorapi.AppRef) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.front.ID != a.ID {
		d.since = time.Now().UnixMilli()
	}
	d.front = a
	for _, r := range d.running {
		if r.ID == a.ID {
			return
		}
	}
	d.running = append(d.running, a)
}

// New builds the dev server. cleanup removes a temp data dir it created.
func New(c Config) (*Server, func(), error) {
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
	if err := loopbackOnly(c.Addr); err != nil {
		return nil, nil, err
	}
	cleanup := func() {}
	data := c.Data
	if data == "" {
		d, err := tempDataDir()
		if err != nil {
			return nil, nil, err
		}
		data = d
		cleanup = func() { _ = os.RemoveAll(d) }
	}
	seed := c.SeedFrom
	if seed != "" {
		if _, err := os.Stat(seed); err != nil {
			c.Logf("seed-from %s not readable (%v); Finder/app banks will not be seeded", seed, err)
			seed = ""
		}
	}
	opts := preset.Options{SeedFrom: seed, LiveResolvePath: c.Live, Controls: editorapi.ControlIDs(), Logf: c.Logf, Lock: true, StateDir: c.State}
	if c.NoPoll {
		opts.PollInterval = -1
	}
	st, err := preset.Open(data, opts)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	prevCleanup := cleanup
	cleanup = func() { st.Close(); prevCleanup() }

	dev := NewDevState()
	dev.engState, dev.eMode = c.EngineState, "hue"
	dev.sw, dev.snap, dev.token = editorapi.NewSwitchLog(), st.Snapshot, c.Token
	ring := editorapi.NewEventLog()
	dev.sw.OnSwitch = ring.AddSwitch
	dev.note()
	bus := editorapi.NewBus()
	arm := &devArm{}
	arm.armed.Store(true)
	sim := &editorapi.Simulator{Store: st, Frontmost: dev, Bus: bus, Arm: arm}
	eng := newDevEngine(st)
	mux := http.NewServeMux()
	cal := newCalib(mux, st, dev, arm)
	simulate := func(req editorapi.SimRequest) (editorapi.Event, error) {
		// like the engine: while the editor waits for "press a deck button", a press is the capture trigger
		if req.Kind == "press" && cal.OnDeckKey() {
			return editorapi.Event{Control: req.Control, Kind: "press", Out: editorapi.Out{Text: "used as the calibration capture trigger"}}, nil
		}
		return sim.Simulate(req)
	}
	aiDir := c.AIDir
	if aiDir == "" {
		aiDir, _ = os.MkdirTemp("", "2t5-deck-plus-ai-")
		prevCleanup3 := cleanup
		cleanup = func() { _ = os.RemoveAll(aiDir); prevCleanup3() }
	}
	// the first-run marker lives in a scratch folder of its own, so the dev server never touches ~/Library
	setupDir, _ := os.MkdirTemp("", "2t5-deck-plus-setup-")
	prevCleanup4 := cleanup
	cleanup = func() { _ = os.RemoveAll(setupDir); prevCleanup4() }
	dev.sys.setupDir = setupDir
	var apiH http.Handler
	ai := aiaccess.New(aiaccess.Options{Dir: aiDir, Exe: c.AIExe, EditorToken: c.Token, Audit: ring.Add, Logf: c.Logf,
		Editor: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { apiH.ServeHTTP(w, r) })})
	prevCleanup2 := cleanup
	cleanup = func() { ai.Close(); prevCleanup2() }
	api := editorapi.NewHandler(editorapi.Deps{AI: ai, Calib: cal, Store: st, Frontmost: dev, Running: dev, Live: dev, Why: dev, Bus: bus, Icons: realIcons{},
		Simulate: simulate, Engine: eng, DeckState: devDeck{dev, bus}, Perms: dev, Arm: arm, Switches: dev.sw, Token: c.Token, Events: ring, Env: dev,
		System: dev.sys, OpenURL: dev.sys.openURL, SetupDir: setupDir, Version: "dev"})

	apiH = api
	mux.HandleFunc("POST /api/dev/deck-press", func(w http.ResponseWriter, r *http.Request) {
		if !dev.guard(w, r) {
			return
		}
		var body struct {
			Control string `json:"control"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body) != nil || !dev.engState {
			writeJSON(w, 400, map[string]string{"error": "needs --engine-state and {\"control\":\"Hue|Sat|Lum|CustomMode\"}"})
			return
		}
		dev.mu.Lock()
		switch body.Control {
		case "Hue":
			dev.eMode = "hue"
		case "Sat":
			dev.eMode = "sat"
		case "Lum":
			dev.eMode = "lum"
		case "CustomMode":
			dev.eCustom = !dev.eCustom
		default:
			dev.mu.Unlock()
			writeJSON(w, 400, map[string]string{"error": "not a deck-state key"})
			return
		}
		m, cu := dev.eMode, dev.eCustom
		dev.mu.Unlock()
		bus.Publish(editorapi.Event{Type: "deck-state", Kind: "state", T: time.Now().UnixMilli(), Mode: m, Custom: &cu})
		writeJSON(w, 200, map[string]any{"mode": m, "custom": cu, "t": time.Now().UnixMilli()})
	})
	mux.HandleFunc("POST /api/dev/layer", func(w http.ResponseWriter, r *http.Request) { // fake engine layer, for trying the editor's layer follow
		if !dev.guard(w, r) {
			return
		}
		var body struct {
			Layer int `json:"layer"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body) != nil || body.Layer < 1 || body.Layer > 3 {
			writeJSON(w, 400, map[string]string{"error": "body must be {\"layer\":1..3}"})
			return
		}
		dev.mu.Lock()
		dev.layer = body.Layer
		dev.mu.Unlock()
		bus.Publish(editorapi.Event{Type: "deck-state", Kind: "state", T: time.Now().UnixMilli(), Layer: body.Layer}) // the same push the engine makes
		writeJSON(w, 200, map[string]int{"layer": body.Layer})
	})
	mux.HandleFunc("GET /api/dev/frontmost", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, dev.Frontmost()) })
	mux.HandleFunc("POST /api/dev/frontmost", func(w http.ResponseWriter, r *http.Request) {
		if !dev.guard(w, r) {
			return
		}
		var body struct {
			editorapi.AppRef
			EditorFocused *bool `json:"editorFocused"`
		}
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
		if err == nil && body.EditorFocused != nil {
			// dev switch for the "why" reason: the editor window has focus
			dev.setEditorFocused(*body.EditorFocused)
			dev.note()
			if body.ID == "" {
				writeJSON(w, 200, dev.Frontmost())
				return
			}
		}
		a := body.AppRef
		if err != nil || a.ID == "" {
			writeJSON(w, 400, map[string]string{"error": "body must be {\"id\":\"bundle.id\",\"name\":\"Name\"}", "field": "id"})
			return
		}
		if a.Name == "" {
			a.Name = preset.DefaultName(a.ID)
		}
		dev.setFront(a)
		dev.note()
		writeJSON(w, 200, a)
	})
	mux.HandleFunc("POST /api/dev/preflight", func(w http.ResponseWriter, r *http.Request) {
		if !dev.guard(w, r) {
			return
		}
		var body struct {
			WS01             *bool `json:"ws01"`
			LDResolveRunning *bool `json:"ldResolveRunning"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "body must be {\"ws01\":bool,\"ldResolveRunning\":bool}"})
			return
		}
		dev.mu.Lock()
		if body.WS01 != nil {
			dev.envNoWS01 = !*body.WS01
		}
		if body.LDResolveRunning != nil {
			dev.envResolve = *body.LDResolveRunning
		}
		dev.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/dev/compose-error", func(w http.ResponseWriter, r *http.Request) {
		if !dev.guard(w, r) {
			return
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "body must be {\"error\":\"text or empty\"}"})
			return
		}
		eng.setFail(body.Error)
		writeJSON(w, 200, eng.EngineState())
	})
	mux.HandleFunc("POST /api/dev/permissions", func(w http.ResponseWriter, r *http.Request) {
		if !dev.guard(w, r) {
			return
		}
		var body struct {
			Accessibility    *bool   `json:"accessibility"`
			ScreenRecording  *bool   `json:"screenRecording"`
			AutomationFinder *string `json:"automationFinder"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad JSON body"})
			return
		}
		if body.AutomationFinder != nil {
			switch *body.AutomationFinder {
			case "unknown", "granted", "denied":
			default:
				writeJSON(w, 400, map[string]string{"error": "automationFinder must be unknown, granted or denied", "field": "automationFinder"})
				return
			}
		}
		dev.setPerms(body.Accessibility, body.ScreenRecording, body.AutomationFinder)
		writeJSON(w, 200, dev.Permissions())
	})
	registerDevSys(mux, dev)
	mux.Handle("/api/", api)
	if c.Static != "" {
		fsrv := http.StripPrefix("/editor/", http.FileServer(http.Dir(c.Static)))
		mux.HandleFunc("/editor/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			fsrv.ServeHTTP(w, r)
		})
		mux.HandleFunc("/editor", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/editor/", http.StatusFound) })
	}
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/editor/", http.StatusFound) })
	return &Server{Handler: editorapi.HostGuard(mux), DataDir: data, Store: st, Bus: bus, Sim: sim, Dev: dev, sim: c.Sim}, cleanup, nil
}

// StartSim launches the random-traffic goroutine when --sim was given.
func (s *Server) StartSim(ctx context.Context) {
	if !s.sim {
		return
	}
	go s.Sim.Run(ctx, rand.New(rand.NewSource(time.Now().UnixNano())))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// guard vets a dev-only POST like the API's own mutations (editorapi.CheckMutationToken):
// no null or foreign Origin, the X-LD-Editor header, a JSON body, the token when one is set.
func (d *DevState) guard(w http.ResponseWriter, r *http.Request) bool {
	if code, msg := editorapi.CheckMutationToken(r, d.token); code != 0 {
		writeJSON(w, code, map[string]string{"error": msg})
		return false
	}
	return true
}

// loopbackOnly refuses any listen address that is not 127.0.0.1 / ::1 / localhost.
func loopbackOnly(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bad --addr %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("--addr %q is not loopback: the editor API may only listen on 127.0.0.1", addr)
}

// tempDataDir prefers the shared WS-01 sandbox (scratch rule), else the OS temp dir.
func tempDataDir() (string, error) {
	const sb = "/Volumes/2T5-WS-01/02-AI-PROJECTS/00-SANDBOX"
	if st, err := os.Stat(sb); err == nil && st.IsDir() {
		base := filepath.Join(sb, "_tmp")
		if _, err := os.Stat(base); os.IsNotExist(err) {
			_ = os.Mkdir(base, 0o755) // parent proven to exist
		}
		if d, err := os.MkdirTemp(base, "deck-editor-dev-"); err == nil {
			return d, nil
		}
	}
	return os.MkdirTemp("", "deck-editor-dev-")
}

// devDeck is the dev server's DeckStateSetter: like the engine, it changes the lamp state and publishes a deck-state event.
type devDeck struct {
	d   *DevState
	bus *editorapi.Bus
}

func (x devDeck) SetDeckState(mode string, custom *bool) error {
	if !x.d.engState {
		return errors.New("start the dev server with --engine-state")
	}
	x.d.mu.Lock()
	if mode != "" {
		x.d.eMode = mode
	}
	if custom != nil {
		x.d.eCustom = *custom
	}
	m, cu := x.d.eMode, x.d.eCustom
	x.d.mu.Unlock()
	x.bus.Publish(editorapi.Event{Type: "deck-state", Kind: "state", T: time.Now().UnixMilli(), Mode: m, Custom: &cu})
	return nil
}
