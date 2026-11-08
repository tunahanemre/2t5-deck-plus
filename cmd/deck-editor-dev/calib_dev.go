package main

import (
	"encoding/json"
	"net/http"

	"2t5-deck-plus/internal/mousecal"
	"2t5-deck-plus/internal/preset"
)

// devCalSys is the dev server's mouse-calibration desktop: the simulated
// windows / displays / pixels of mousecal.FakeSystem, with the permissions, the
// frontmost app and the safety switch taken from the dev state so the editor's
// existing dev controls drive it too. Nothing here posts any input.
type devCalSys struct {
	*mousecal.FakeSystem
	dev *DevState
	arm *devArm
}

func (s devCalSys) Perms() (bool, bool) {
	p := s.dev.Permissions()
	return p.Accessibility, p.ScreenRecording
}

func (s devCalSys) Env(appID string) (mousecal.Env, error) {
	env, err := s.FakeSystem.Env(appID)
	if env.Running {
		env.Frontmost = s.dev.Frontmost().ID == appID
	}
	return env, err
}

func (s devCalSys) Armed() bool { return s.arm.Armed() }

// MoveTo / Click: the fake records them; they are gated by the dev switch.
func (s devCalSys) MoveTo(p mousecal.Pt) bool { return s.FakeSystem.MoveTo(p) }
func (s devCalSys) Click(p mousecal.Pt) bool  { return s.FakeSystem.Click(p) }

// newCalib builds the service and registers the dev routes that drive the fake
// desktop (GET/POST /api/dev/calibration).
func newCalib(mux *http.ServeMux, st *preset.Store, dev *DevState, arm *devArm) *mousecal.Service {
	fake := mousecal.NewFakeSystem(preset.ResolveID)
	fake.ArmedFn = arm.Armed
	svc := &mousecal.Service{
		Sys:   devCalSys{fake, dev, arm},
		Store: mousecal.PresetSettings{Store: st},
		Preset: func(id string) *preset.Preset {
			p, _ := st.Snapshot().Preset(id)
			return p
		},
	}
	mux.HandleFunc("GET /api/dev/calibration", func(w http.ResponseWriter, r *http.Request) {
		var app mousecal.FakeApp
		fake.With(preset.ResolveID, func(a *mousecal.FakeApp) { app = *a })
		cur, moves, clicks := fake.Recorded()
		writeJSON(w, 200, map[string]any{"windows": app.Windows, "layout": app.Layout, "running": app.Running, "panelMove": app.PanelMove,
			"cursor": cur, "displays": fake.Displays, "moves": moves, "clicks": clicks})
	})
	mux.HandleFunc("POST /api/dev/calibration", func(w http.ResponseWriter, r *http.Request) {
		if !dev.guard(w, r) {
			return
		}
		var b struct {
			Action  string   `json:"action"`
			X       *float64 `json:"x"`
			Y       *float64 `json:"y"`
			W       *float64 `json:"w"`
			H       *float64 `json:"h"`
			Name    *string  `json:"name"`
			On      *bool    `json:"on"`
			Target  string   `json:"target"`
			Display string   `json:"display"`
			DX, DY  float64
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&b); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad JSON body"})
			return
		}
		app := preset.ResolveID
		set := func(dst *float64, v *float64) {
			if v != nil {
				*dst = *v
			}
		}
		out := map[string]any{"ok": true}
		switch b.Action {
		case "cursor": // {x,y}
			if b.X == nil || b.Y == nil {
				writeJSON(w, 400, map[string]string{"error": "cursor needs x and y"})
				return
			}
			fake.SetCursor(mousecal.Pt{X: *b.X, Y: *b.Y})
		case "hover": // {target, dx?, dy?}: the pointer on the drawn control (plus an offset)
			p, ok := fake.WidgetPoint(app, b.Target)
			if !ok {
				writeJSON(w, 404, map[string]string{"error": "the fake screen draws no control " + b.Target})
				return
			}
			fake.SetCursor(mousecal.Pt{X: p.X + b.DX, Y: p.Y + b.DY})
			out["cursor"], _, _ = fake.Recorded()
		case "window": // {x,y,w,h}: any of them
			fake.With(app, func(a *mousecal.FakeApp) {
				f := &a.Windows[0].Frame
				set(&f.X, b.X)
				set(&f.Y, b.Y)
				set(&f.W, b.W)
				set(&f.H, b.H)
			})
		case "screen": // {display: laptop|4k}: window fills that display
			for _, d := range fake.Displays {
				if (b.Display == "laptop" && d.ID == "d1") || (b.Display == "4k" && d.ID == "d2") {
					fake.With(app, func(a *mousecal.FakeApp) {
						a.Windows[0].Frame = mousecal.Rect{X: d.Frame.X, Y: d.Frame.Y + 25, W: d.Frame.W, H: d.Frame.H - 25}
					})
				}
			}
		case "layout": // {name}: Resolve page; "" = unknown
			fake.With(app, func(a *mousecal.FakeApp) {
				if b.Name != nil {
					a.Layout = *b.Name
				}
			})
		case "panel": // {x,y}: every control moves by this much, window unchanged
			fake.With(app, func(a *mousecal.FakeApp) {
				set(&a.PanelMove.X, b.X)
				set(&a.PanelMove.Y, b.Y)
			})
		case "running": // {on}
			fake.With(app, func(a *mousecal.FakeApp) { a.Running = b.On == nil || *b.On })
		case "second-window": // {on}: a second big window
			fake.With(app, func(a *mousecal.FakeApp) {
				a.Windows = a.Windows[:1]
				if b.On != nil && *b.On {
					f := a.Windows[0].Frame
					a.Windows = append(a.Windows, mousecal.Window{Title: "Project Manager", Frame: mousecal.Rect{X: f.X + 40, Y: f.Y + 30, W: f.W - 80, H: f.H - 60}})
				}
			})
		case "deck-key": // a deck button press, as the engine hook sees it
			out["consumed"] = svc.OnDeckKey()
		default:
			writeJSON(w, 400, map[string]string{"error": "action must be cursor, hover, window, screen, layout, panel, running, second-window or deck-key"})
			return
		}
		writeJSON(w, 200, out)
	})
	return svc
}
