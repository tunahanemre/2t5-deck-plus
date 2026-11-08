package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"2t5-deck-plus/internal/aiaccess"
	"2t5-deck-plus/internal/appdeck"
	"2t5-deck-plus/internal/calsys"
	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/datadir"
	"2t5-deck-plus/internal/deckenv"
	"2t5-deck-plus/internal/deps"
	"2t5-deck-plus/internal/editorapi"
	"2t5-deck-plus/internal/engine"
	"2t5-deck-plus/internal/leds"
	"2t5-deck-plus/internal/macapp"
	"2t5-deck-plus/internal/mousecal"
	"2t5-deck-plus/internal/preset"
)

// editorWiring connects the preset store, the engine (hot reload, appdeck,
// input observer) and the editor HTTP API. Everything here is plumbing; the
// logic lives in internal/preset, internal/appdeck and internal/editorapi.
type editorWiring struct {
	store    *preset.Store
	handler  http.Handler
	bus      *editorapi.Bus
	eng      *engine.Engine
	ad       *appdeck.Executor
	device   string
	track    *editorapi.EngineTracker   // what the engine runs (GET /api/live engine block)
	sw       *editorapi.SwitchLog       // why-did-it-switch ring (GET /api/switches); never read by the engine
	armMark  *editorapi.ArmMarkerWriter // keeps presets/.disarmed in step with the switch, off the caller's goroutine
	events   *editorapi.EventLog        // last 50 switches, arm changes, shell fires, imports (GET /api/events-log)
	token    string                     // per-launch API token (memory only): X-LD-Token on every mutating request
	armStart editorapi.ArmStart         // how the app came up (armedAtStart / startReason in GET /api/live)
	native   *editorapi.NativeIO        // dialog-free core of the bound export / import methods
	ai       *aiaccess.Service          // the AI door (off by default): MCP / terminal clients, loopback + token

	gate    preset.SeqGate // newest snapshot taken by the appdeck
	compose chan struct{}  // coalescing "recompose" request, cap 1
	stop    chan struct{}
	once    sync.Once

	mu        sync.Mutex
	lastOther editorapi.AppRef // last frontmost app that was not 2T5 DECK+ itself
	// whyFocused / whySince feed GET /api/live "why": the editor window has
	// focus, and when the reported frontmost app (or that state) last changed.
	hudMu   sync.Mutex
	hudHook func(enabled, withoutResolve bool)

	whyFocused bool
	whySince   int64
}

// initEditor opens the preset store under <cfgDir>/presets (seeded from
// <cfgDir>/bindings.json), composes it into the engine, and builds the API
// handler. A failure is logged and leaves the engine on its bindings.json
// config; the app still starts.
func initEditor(e *engine.Engine, cfgDir string) *editorWiring {
	stateDir, stateErr := appStateDir()
	if stateErr != nil {
		log.Printf("[editor] internal state folder unusable (%s): the deck stays DISARMED until it is fixed: %v", preset.StartReasonStateDown, stateErr)
	}
	preset.BuildID = buildID() // the canary's pre-update backup compares it
	st, err := preset.Open(filepath.Join(cfgDir, "presets"), preset.Options{
		SeedFrom: filepath.Join(cfgDir, "bindings.json"),
		Controls: editorapi.ControlIDs(),
		Logf:     log.Printf,
		// One writer: a second 2T5 DECK+ opens the presets read-only and its
		// writes answer 423 (see GET /api/live engine.readonly).
		Lock:        true,
		StateDir:    stateDir,
		StateDirErr: stateErr,
		// an outside change that adds risk is held back; say so in the events ring
		OnQuarantine: func(pe preset.PendingExternal) {
			if holdEvents != nil {
				holdEvents.Add("external", "held back an outside change to "+pe.File, "", map[string]any{"id": pe.ID, "changes": pe.Changes, "risks": pe.Risks})
			}
		},
	})
	if err != nil {
		// Fail CLOSED: with no editor there is nothing to arm the deck from, so
		// the engine must not run armed on bindings.json alone. Pause the key and
		// mouse packages and install a disarmed executor (it also holds back the
		// sidecar writes and Finder tags); nothing fires until the app is
		// restarted with a working presets folder.
		log.Printf("[editor] preset store unavailable (%s): the deck stays DISARMED until it is fixed: %v", preset.StartReasonStoreDown, err)
		ad := appdeck.NewDisarmed()
		e.SetAppDeck(ad)
		return nil
	}
	w := &editorWiring{
		token:   newEditorToken(),
		store:   st,
		bus:     editorapi.NewBus(),
		eng:     e,
		ad:      appdeck.New(),
		device:  filepath.Join(cfgDir, "device-map.json"),
		track:   editorapi.NewEngineTracker(),
		sw:      editorapi.NewSwitchLog(),
		events:  editorapi.NewEventLog(),
		native:  &editorapi.NativeIO{Store: st},
		compose: make(chan struct{}, 1),
		stop:    make(chan struct{}),
	}
	// Start DISARMED when the policy says so (env, first run of this install,
	// new build). Done HERE, before SetAppDeck and long before the engine is
	// started (App.startup), so there is no armed window: SetArmed(false) also
	// pauses the key and mouse packages. The user arms deliberately from the
	// editor (POST /api/arm), which records .first-run-done.
	holdEvents = w.events
	w.sw.OnSwitch = w.events.AddSwitch
	w.armMark = &editorapi.ArmMarkerWriter{Mark: st.MarkDisarmed, Clear: st.ClearDisarmed, Logf: log.Printf}
	w.ad.OnArmChange = func(armed bool, src string) {
		// runs under the arm lock, possibly on the MIDI callback: ring entry and a
		// hand-off to the marker writer, nothing that touches the disk
		w.events.Add("arm", map[bool]string{true: "armed", false: "disarmed"}[armed], src, nil)
		log.Printf("[editor] deck %s (source: %s)", map[bool]string{true: "ARMED", false: "DISARMED"}[armed], src)
	}
	// every explicit request (also a disarm of an already disarmed deck) refreshes the marker
	w.ad.OnArmRequest = func(armed bool, src string) { w.armMark.Set(armed) }
	w.ad.OnShell = func(cmd string, exit int, note string) {
		d := map[string]any{"exit": exit}
		if note != "" {
			d["note"] = note
		}
		w.events.Add("shell", editorapi.ShellBase(cmd), "", d) // the program's name only, never its arguments
	}
	w.armStart = editorapi.ApplyStartPolicy(st, w.ad, os.Getenv)
	if !w.armStart.Armed {
		w.events.Add("arm", "disarmed", "start:"+w.armStart.Reason, nil)
		log.Printf("[editor] starting DISARMED (%s): nothing fires until you arm the deck in the editor", w.armStart.Reason)
	}
	snap := st.Snapshot()
	w.gate.Do(snap.Seq, func() { w.ad.SetSnapshot(snap) })
	if cfg, err := snap.ComposeConfig(w.device); err != nil {
		log.Printf("[editor] initial compose failed, keeping bindings.json config: %v", err)
		w.track.Failed(snap.Seq, err)
	} else if cfg == nil {
		log.Printf("[editor] initial compose gave no config, keeping bindings.json config")
		w.track.Failed(snap.Seq, errNoConfig)
	} else {
		e.SetConfig(cfg)
		w.track.Applied(snap.Seq)
	}
	e.SetAppDeck(w.ad)
	// Deck lamps (Hue / Sat / Lum / Custom Mode) over MIDI out: EXPERIMENTAL and
	// off unless DECKPLUS_LED_OUT=1, because no source documents the Loupedeck+'s
	// lamp protocol (see internal/leds). Never sends while the deck is disarmed.
	if deckenv.Get("LED_OUT") == "1" {
		drv := leds.New(leds.NewMIDISender("Loupedeck"), w.ad.Armed)
		drv.Logf = log.Printf
		prevArm := w.ad.OnArmChange
		w.ad.OnArmChange = func(armed bool, src string) { // runs under the arm lock: no blocking
			if prevArm != nil {
				prevArm(armed, src)
			}
			if armed {
				drv.Forget()
				go drv.Update(e.SliderMode(), e.CustomMode())
			}
		}
		e.SetLEDSink(drv.Update)
		log.Printf("[leds] LED output ON (DECKPLUS_LED_OUT=1, experimental, armed-only)")
	}
	e.SetInputObserver(w.publish)
	// the editor's Hue / Sat / Lum / Custom Mode LEDs: pushed the moment the engine changes them (long-polled /api/events)
	e.SetStateHook(func(mode string, custom bool, layer int) {
		c := custom
		w.bus.Publish(editorapi.Event{Type: "deck-state", Kind: "state", T: time.Now().UnixMilli(), Mode: mode, Custom: &c, Layer: layer})
	})
	// Momentum tails (only for assignments that set momentumMs): stop as soon
	// as the app that was frontmost when the turn began no longer is, and show
	// each tail step in the live view.
	w.ad.TailGuard = tailGuard
	w.ad.HoldGuard = tailGuard // a hold-to-fire press fires only into the app it began in
	w.ad.OnTail = func(in appdeck.Input, res appdeck.Result) { e.ObserveAppdeckTail(in.Control, res) }
	w.ad.OnHold = func(in appdeck.Input, res appdeck.Result) { e.ObserveAppdeckHold(in.Control, res) }

	// Store.OnChange runs on the editing goroutine: swap the appdeck snapshot
	// (a pointer store) and ask the worker to recompose; never compose here.
	// The gate refuses a snapshot older than one already taken (the Store
	// already delivers in order; this keeps the appdeck from ever going back).
	st.OnChange(func(s *preset.Snapshot) {
		w.gate.Do(s.Seq, func() {
			w.ad.SetSnapshot(s)
			select {
			case w.compose <- struct{}{}:
			default:
			}
		})
	})
	ensureGlobalHUDToggle(st, stateDir)
	go w.composeLoop()
	go w.watchFrontmost()

	// Mouse calibration: every point the engine clicks or drags at now comes
	// from the calibration stored in the Resolve preset, resolved against the
	// window as it is, and is refused (never clicked blind) when the window,
	// screen or layout no longer matches. Installed before the engine starts.
	cal := &mousecal.Service{
		Sys: &calsys.System{Layout: func(id string) string {
			if strings.EqualFold(id, preset.ResolveID) {
				return strings.ToLower(e.Status().Page)
			}
			return ""
		}},
		Store: mousecal.PresetSettings{Store: st},
		Preset: func(id string) *preset.Preset {
			p, _ := st.Snapshot().Preset(id)
			return p
		},
		Legacy: func(key string) (mousecal.Pt, string, bool) {
			x, y, axis, ok := e.LegacyCalibPoint(key)
			return mousecal.Pt{X: x, Y: y}, axis, ok
		},
		Logf: log.Printf,
	}
	e.SetCalibResolver(cal.EngineResolver(preset.ResolveID))
	e.SetCaptureHook(cal.OnDeckKey)
	// One-time move of the old (ld-resolve) absolute calibration into the store, so the
	// warning counts what was already calibrated. Only when the app has no calibration of
	// its own and has never imported; reads displays, posts nothing.
	if old := e.LegacyCalibPoints(); len(old) > 0 {
		pts := make(map[string]mousecal.LegacyPoint, len(old))
		for k, p := range old {
			pts[k] = mousecal.LegacyPoint{X: p.X, Y: p.Y, Axis: p.Axis}
		}
		if n, why := cal.MigrateLegacy(preset.ResolveID, pts); n == 0 && why != "" {
			log.Printf("[calibration] ld-resolve import skipped: %s", why)
		}
	}

	// AI access: every tool is a request to this same handler (same validation, trust and undo as a hand edit)
	w.ai = aiaccess.New(aiaccess.Options{
		Editor:      http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { w.handler.ServeHTTP(rw, r) }),
		EditorToken: w.token, Audit: w.events.Add, Logf: log.Printf,
	})
	w.handler = editorapi.NewHandler(editorapi.Deps{
		Calib: cal, AI: w.ai,
		Store: st, Frontmost: w, Running: w, Live: w, Why: w, Bus: w.bus, Icons: w,
		Engine: w.track, DeckState: e, Perms: w, Arm: w.ad, Switches: w.sw, ArmStart: &w.armStart, Token: w.token, Native: w.native, Events: w.events, Env: w, ArmMark: w.armMark,
		System: deps.NewSystem(systemProbes()), OpenURL: func(u string) error { return openURLFn(u) }, SetupDir: setupMarkerDir(), Version: appVersion,
	})
	if err := w.ai.Resume(); err != nil { // the user left AI access on last time
		log.Printf("[ai-access] could not resume: %v", err)
	}
	return w
}

// composeLoop recomposes the engine config from the newest snapshot. Requests
// arriving during a compose coalesce into one more pass.
func (w *editorWiring) composeLoop() {
	for {
		select {
		case <-w.stop:
			return
		case <-w.compose:
			snap := w.store.Snapshot()
			cfg, err := snap.ComposeConfig(w.device)
			if err == nil && cfg == nil {
				err = errNoConfig
			}
			if err != nil {
				// The engine keeps the previous config: say so where the editor can see it.
				log.Printf("[editor] compose failed, keeping the previous config: %v", err)
				w.track.Failed(snap.Seq, err)
				continue
			}
			w.eng.SetConfig(cfg)
			w.track.Applied(snap.Seq)
			w.pushHUDSetting(cfg)
		}
	}
}

// setHUDHook registers the App's hudWithoutResolve receiver and feeds it the
// current value at once (the initial compose ran before the hook existed).
func (w *editorWiring) setHUDHook(fn func(enabled, withoutResolve bool)) {
	w.hudMu.Lock()
	w.hudHook = fn
	w.hudMu.Unlock()
	if cfg, err := w.store.Snapshot().ComposeConfig(w.device); err == nil && cfg != nil {
		w.pushHUDSetting(cfg)
	}
}

func (w *editorWiring) pushHUDSetting(cfg *config.Config) {
	w.hudMu.Lock()
	fn := w.hudHook
	w.hudMu.Unlock()
	if fn == nil || cfg == nil {
		return
	}
	on := cfg.TopLevel.HUDWithoutResolve == nil || *cfg.TopLevel.HUDWithoutResolve
	enabled := cfg.TopLevel.HUDEnabled == nil || *cfg.TopLevel.HUDEnabled
	fn(enabled, on)
}

// toggleHUDEnabled flips the persisted general HUD on/off (hudEnabled lives on the
// Resolve preset but acts globally; a missing value means on) in ONE store change
// and returns the new value. It only swaps a snapshot and signals the compose
// worker, so the MIDI callback may call it.
func (w *editorWiring) toggleHUDEnabled() (bool, error) {
	next := false
	err := w.store.UpdateSetting(preset.ResolveID, "hudEnabled", "Toggle HUD (deck)", func(old any) (any, error) {
		cur := true
		if b, ok := old.(bool); ok {
			cur = b
		}
		next = !cur
		return next, nil
	})
	return next, err
}

// ensureGlobalHUDToggle gives the Global preset the deck keys every app needs: what
// Fn + C6 = Toggle HUD needs in every app : C6 in the fn bank, and
// Fn itself as a modifier hold (outside Resolve nothing else makes Fn raise the fn
// bank, so C6 never paired). One-time, each only when absent: a marker in the state
// folder keeps it from coming back after he removes or changes it in the editor.
func ensureGlobalHUDToggle(st *preset.Store, stateDir string) {
	if st == nil || stateDir == "" {
		return
	}
	if ro, _ := st.ReadOnly(); ro {
		return
	}
	marker := filepath.Join(stateDir, "global-deck-keys-v3")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if g, ok := st.Snapshot().Preset(preset.GlobalID); ok {
		if _, has := g.Banks["fn"]["C6"]; !has {
			if err := st.Assign(preset.GlobalID, "fn", "C6", map[string]any{"type": "toggleHud"}); err != nil {
				log.Printf("[editor] could not assign Global Fn+C6 = Toggle HUD: %v", err)
				return
			}
		}
		// L1 / L2 / L3 switch the deck layer in every app (before this only Resolve's
		// preset assigned them, so outside Resolve the layer, and the HUD, stayed on L1)
		for n, c := range []string{"L1", "L2", "L3"} {
			if _, has := g.Banks["base"][c]; !has {
				if err := st.Assign(preset.GlobalID, "base", c, map[string]any{"type": "layer", "layer": n + 1}); err != nil {
					log.Printf("[editor] could not assign Global %s = layer %d: %v", c, n+1, err)
					return
				}
			}
		}
		if _, has := g.Banks["base"]["Fn"]; !has {
			if err := st.Assign(preset.GlobalID, "base", "Fn", map[string]any{"type": "modifierHold", "modifier": "fn"}); err != nil {
				log.Printf("[editor] could not assign Global Fn = hold the Fn bank: %v", err)
				return
			}
		}
	}
	if err := os.WriteFile(marker, []byte("done\n"), 0o644); err != nil {
		log.Printf("[editor] could not write %s: %v", marker, err)
	}
}

var errNoConfig = errors.New("the composed configuration is empty")

// Permissions implements editorapi.PermissionsProvider (read-only TCC queries).
func (w *editorWiring) Permissions() editorapi.PermState {
	p := macapp.ReadPermissions()
	return editorapi.PermState{Accessibility: p.Accessibility, ScreenRecording: p.ScreenRecording, AutomationFinder: p.AutomationFinder}
}

// Close stops the background goroutines and the store poller.
func (w *editorWiring) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		close(w.stop)
		w.ai.Close()
		w.armMark.Close() // applies a pending .disarmed change before the store closes
		w.store.Close()
	})
}

// publish converts an engine InputEvent to the editor API's Event.
func (w *editorWiring) publish(ev engine.InputEvent) { w.bus.Publish(toAPIEvent(ev)) }

func toAPIEvent(ev engine.InputEvent) editorapi.Event {
	return editorapi.Event{
		T: ev.T, Control: ev.Control, Kind: ev.Kind, RawTicks: ev.RawTicks,
		TicksPerSec: ev.TicksPerSec, Accel: ev.Accel,
		Out:  editorapi.Out{Text: ev.Out.Text, Value: ev.Out.Value, Unit: ev.Out.Unit},
		Fine: ev.Fine, PresetID: ev.PresetID, Source: ev.Source, Bank: ev.Bank, Action: ev.Action,
		LatencyMs: ev.LatencyMs,
	}
}

// watchFrontmost remembers the last frontmost app that is not 2T5 DECK+ itself,
// so "add the current app" works while the editor window has focus.
func (w *editorWiring) watchFrontmost() {
	// 250 ms: fine enough for the switch log to see a focus blip (< 400 ms)
	// come and go; one NSWorkspace query per tick, off the engine's input path.
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.Frontmost()
		}
	}
}

// Frontmost implements editorapi.FrontmostProvider. While 2T5 DECK+ itself is
// frontmost it reports the previous other app.
func (w *editorWiring) Frontmost() editorapi.AppRef {
	id := macapp.FrontmostBundleID()
	self := macapp.SelfBundleID()
	w.mu.Lock()
	prevID, prevFocused := w.lastOther.ID, w.whyFocused
	ref := w.frontmostLocked(id, self)
	w.sw.NoteFront(time.Now(), ref, w.whyFocused, w.store.Snapshot())
	changed := ref.ID != prevID || w.whyFocused != prevFocused
	w.mu.Unlock()
	if changed && w.bus != nil {
		// push it: the editor repaints the status at once instead of on its next
		// 1-2 s /api/live poll (the status otherwise shows the frontmost app late)
		// PresetID is the preset the engine now uses (the front app's, else Global) so the editor can
		// switch to it from the event itself, without a round trip.
		active := preset.GlobalID
		if _, ok := w.store.Snapshot().Preset(ref.ID); ok && ref.ID != "" {
			active = ref.ID
		}
		w.bus.Publish(editorapi.Event{Type: "front", Kind: "state", T: time.Now().UnixMilli(), PresetID: active})
	}
	return ref
}

func (w *editorWiring) frontmostLocked(id, self string) editorapi.AppRef {
	if id == "" || (self != "" && id == self) {
		// the editor (or an unknown window) has focus: keep the last real app
		if self != "" && id == self && !w.whyFocused {
			w.ad.CancelHolds() // the editor took focus
			w.whyFocused, w.whySince = true, time.Now().UnixMilli()
		}
		return w.lastOther
	}
	if w.whyFocused {
		w.whyFocused, w.whySince = false, time.Now().UnixMilli()
	}
	if w.lastOther.ID != id {
		w.ad.CancelHolds() // the frontmost app changed: nothing held may fire into the new one
		w.whySince = time.Now().UnixMilli()
		name := macapp.AppName(id)
		if name == "" {
			name = preset.DefaultName(id)
		}
		w.lastOther = editorapi.AppRef{ID: id, Name: name}
	}
	return w.lastOther
}

// FrontInfo implements editorapi.FrontInfoProvider (GET /api/live "why"). It
// reads what Frontmost() last recorded; the 250 ms watcher keeps it fresh.
func (w *editorWiring) FrontInfo() editorapi.FrontInfo {
	w.mu.Lock()
	defer w.mu.Unlock()
	return editorapi.FrontInfo{EditorFocused: w.whyFocused, Since: w.whySince}
}

// tailGuard says whether a momentum tail that began under bundle may still
// act: the same app is frontmost (Resolve, or an unknown lookup, counts as
// Resolve like the engine's turn path does).
func tailGuard(bundle string) bool {
	id := macapp.FrontmostBundleID()
	if bundle == preset.ResolveID {
		return id == "" || macapp.BundleIDIsResolve(id)
	}
	return id == bundle
}

// Running implements editorapi.RunningProvider.
func (w *editorWiring) Running() []editorapi.AppRef {
	apps := macapp.RunningApps()
	out := make([]editorapi.AppRef, 0, len(apps))
	self := macapp.SelfBundleID()
	for _, a := range apps {
		if self != "" && a.ID == self {
			continue
		}
		out = append(out, editorapi.AppRef{ID: a.ID, Name: a.Name})
	}
	return out
}

// AppIconPNG implements editorapi.IconProvider (NSWorkspace, no Screen Recording).
func (w *editorWiring) AppIconPNG(bundleID string) ([]byte, error) {
	if b := macapp.AppIconPNG(bundleID); b != nil {
		return b, nil
	}
	return nil, editorapi.ErrNoIcon
}

// Live implements editorapi.LiveProvider from the engine status.
func (w *editorWiring) Live() editorapi.LiveState {
	st := w.eng.Status()
	side := st.SidecarState
	if side == "" {
		side = engine.SidecarStateWaiting
	}
	return editorapi.LiveState{EngineRunning: st.Running, Sidecar: side, Project: st.Project, SliderMode: w.eng.SliderMode(), CustomMode: w.eng.CustomMode(), Layer: st.Layer}
}

// WS01 implements editorapi.EnvProvider (preflight): read-only mount test of the data drive.
func (w *editorWiring) WS01() (bool, string) {
	// studio default; override with DECKPLUS_DATA_DIR
	return editorapi.MountState("/Volumes/2T5-WS-01", ".2t5-mount-sentinel")
}

// WS01Applicable implements the optional preflight check: the data-drive row only
// belongs on a Mac whose data folder is on an external volume. On any other Mac
// (data under ~/Library) there is no such drive to be missing.
func (w *editorWiring) WS01Applicable() bool { return strings.HasPrefix(datadir.Root(), "/Volumes/") }

// ldResolveBundleID is the old LD-RESOLVE app (it contends for the same MIDI port).
const ldResolveBundleID = "com.2t5.ld-resolve"

// LDResolveRunning implements editorapi.EnvProvider (preflight).
func (w *editorWiring) LDResolveRunning() bool { return macapp.IsAppRunning(ldResolveBundleID) }

// APIHandler is the http.Handler for /api/ (nil when the editor is unavailable).
func (a *App) APIHandler() http.Handler {
	if a == nil || a.editor == nil {
		return nil
	}
	return a.editor.handler
}

// buildID identifies this build for the preset canary's pre-update backup: a
// build-time stamp (-ldflags "-X main.buildStamp=...") when there is one, else
// the executable's size and mtime, which change with every rebuild.
func buildID() string {
	if buildStamp != "" {
		return buildStamp
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("exe-%d-%d", fi.ModTime().Unix(), fi.Size())
}

// buildStamp is set at link time (see scripts/build-app.sh); empty otherwise.
var buildStamp string

// newEditorToken is 32 random bytes as hex: the per-launch API token. Memory
// only. If the system random source fails it panics rather than fall back to an
// empty token: a deck that cannot get random bytes must not run an
// unauthenticated editor API.
func newEditorToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("editor: no system randomness for the API token: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// holdEvents is the events ring the store's quarantine callback writes to; set once the ring exists.
var holdEvents *editorapi.EventLog

// stateDirEnv overrides the internal state folder (tests, and anyone who wants it elsewhere).
const stateDirEnv = "DECKPLUS_STATE_DIR"

// legacyStateDirEnv is the name stateDirEnv had before the rename; still read when stateDirEnv is unset.
const legacyStateDirEnv = "LD_DECK_STATE_DIR"

// appStateDir is the INTERNAL-disk folder for the authoritative arm markers and
// the trust store (~/Library/Application Support/2t5-deck-plus/state). DECKPLUS_STATE_DIR
// overrides it. It FAILS (and the store then fails closed: disarmed, with a
// warning) when HOME is unknown, and when running under `go test` without the
// override, so a test run can never write into the real state.
// underGoTest is true in any test binary: the .test suffix, the registered
// test.v flag, or testing.Testing(). Any one of them refuses the real folder.
func underGoTest() bool {
	return testing.Testing() || flag.Lookup("test.v") != nil || strings.HasSuffix(os.Args[0], ".test")
}

func appStateDir() (string, error) {
	if v := os.Getenv(stateDirEnv); v != "" {
		return v, nil
	}
	if v := os.Getenv(legacyStateDirEnv); v != "" {
		return v, nil
	}
	if underGoTest() {
		return "", errors.New("refusing to use the real state folder under go test: set " + stateDirEnv)
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "", errors.New("HOME is not set")
	}
	return filepath.Join(h, "Library", "Application Support", datadir.AppSupportName, "state"), nil
}

// EditorResponse is what EditorRequest returns to the page.
type EditorResponse struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// EditorRequest runs one editor API write IN PROCESS, through the same handler (guards, token, logging) the HTTP
// path uses, and returns its status and body. The editor calls it (window.go.main.App.EditorRequest) for every
// non-GET request instead of fetch(): Wails v2 on macOS hands a fetch() POST body to the handler through an
// NSInputStream read that returns "end of data" when the bytes have not arrived yet, so writes arrived with an empty
// body (400 "bad JSON body: EOF"), or never visibly landed. GETs keep using fetch. JSON bodies only; the zip import
// keeps its own path.
func (a *App) EditorRequest(method, path, body string) EditorResponse {
	if a == nil || a.editor == nil || a.editor.handler == nil {
		return EditorResponse{Status: 503, Body: `{"error":"the editor API is not available"}`}
	}
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return EditorResponse{Status: 405, Body: `{"error":"EditorRequest takes writes only"}`}
	}
	if !strings.HasPrefix(path, "/api/") || strings.Contains(path, "..") || len(body) > 1<<20 {
		return EditorResponse{Status: 400, Body: `{"error":"bad editor request"}`}
	}
	req, err := http.NewRequest(method, "http://wails"+path, strings.NewReader(body))
	if err != nil {
		return EditorResponse{Status: 400, Body: `{"error":"bad editor request"}`}
	}
	req.Host = "wails"
	req.RequestURI = path
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set(editorapi.EditorHeader, "1")
	req.Header.Set(editorapi.TokenHeader, a.editor.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	a.editor.handler.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		log.Printf("[editor] %s %s answered %d (in-process write): %.200s", method, path, rec.Code, rec.Body.String())
	}
	return EditorResponse{Status: rec.Code, Body: rec.Body.String()}
}
