package mousecal

import (
	"errors"
	"image"
	"image/color"
	"math"
	"sync"
)

// FakeSystem is a simulated desktop: apps with windows, displays, a pointer,
// permissions and a drawn "screen" whose widgets sit where an app would put
// them. It posts nothing anywhere: MoveTo and Click are recorded. The --sim dev
// server and the tests use it; the packaged app uses the macOS System.
type FakeSystem struct {
	mu sync.Mutex

	Displays []Display
	Apps     map[string]*FakeApp
	Cursor_  Pt
	// ArmedFn is the safety switch (nil = armed).
	ArmedFn func() bool
	// Input mimics "this process may post input": false = MoveTo / Click do nothing.
	Input         bool
	Accessibility bool
	ScreenRec     bool

	Moves  []Pt
	Clicks []Pt
}

// FakeApp is one simulated app.
type FakeApp struct {
	Name    string
	Running bool
	Front   bool
	Windows []Window
	Layout  string
	Widgets []FakeWidget
	// NeedLayout: the app reports pages and an empty Layout means "unknown".
	NeedLayout bool
	PanelMove  Pt // every widget shifted by this: "a panel moved" with the window unchanged
}

// FakeWidget is a control the fake screen draws: at fraction FX of the window
// width and FromBottom points above the window's bottom edge.
type FakeWidget struct {
	Key        string
	FX         float64
	FromBottom float64
}

// NewFakeSystem returns a desktop with a laptop and a 4K display, Resolve
// running in front with one window on the laptop, all permissions granted.
func NewFakeSystem(resolveID string) *FakeSystem {
	f := &FakeSystem{
		Displays: []Display{
			{ID: "d1", Name: "Built-in Display", Frame: Rect{0, 0, 1512, 982}, Scale: 2},
			{ID: "d2", Name: "PB287Q", Frame: Rect{1512, 0, 3840, 2160}, Scale: 1},
		},
		Apps:  map[string]*FakeApp{},
		Input: true, Accessibility: true, ScreenRec: true,
	}
	var ws []FakeWidget
	for i, w := range []string{"lift", "gamma", "gain", "offset"} {
		fx := 0.14 + 0.24*float64(i)
		ws = append(ws, FakeWidget{w + "_master", fx, 60}, FakeWidget{w + "_reset", fx + 0.08, 360})
		for j, c := range []string{"R", "G", "B"} {
			ws = append(ws, FakeWidget{w + "_" + c, fx - 0.06 + 0.06*float64(j), 130})
		}
	}
	f.Apps[resolveID] = &FakeApp{Name: "DaVinci Resolve", Running: true, Front: true, Layout: "color", NeedLayout: true,
		Windows: []Window{{Title: "DaVinci Resolve - demo", Frame: Rect{0, 25, 1512, 957}}}, Widgets: ws}
	return f
}

// Env implements System.
func (f *FakeSystem) Env(appID string) (Env, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.Apps[appID]
	if a == nil || !a.Running {
		return Env{Displays: append([]Display(nil), f.Displays...)}, nil
	}
	return Env{Running: true, Frontmost: a.Front, Windows: append([]Window(nil), a.Windows...), Displays: append([]Display(nil), f.Displays...), Layout: a.Layout, LayoutRequired: a.NeedLayout}, nil
}

// AppName implements System.
func (f *FakeSystem) AppName(appID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a := f.Apps[appID]; a != nil {
		return a.Name
	}
	return ""
}

// Cursor implements System.
func (f *FakeSystem) Cursor() Pt { f.mu.Lock(); defer f.mu.Unlock(); return f.Cursor_ }

// SetCursor places the simulated pointer (a test or the dev server's "hover").
func (f *FakeSystem) SetCursor(p Pt) { f.mu.Lock(); f.Cursor_ = p; f.mu.Unlock() }

func (f *FakeSystem) canPost() bool {
	return f.Input && (f.ArmedFn == nil || f.ArmedFn())
}

// MoveTo implements System: records the move, moves the simulated pointer.
func (f *FakeSystem) MoveTo(p Pt) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.canPost() {
		return false
	}
	f.Moves = append(f.Moves, p)
	f.Cursor_ = p
	return true
}

// Click implements System: records the click, posts nothing.
func (f *FakeSystem) Click(p Pt) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.canPost() {
		return false
	}
	f.Clicks = append(f.Clicks, p)
	return true
}

// Perms implements System.
func (f *FakeSystem) Perms() (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Accessibility, f.ScreenRec
}

// Armed implements System.
func (f *FakeSystem) Armed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.canPostArm()
}

func (f *FakeSystem) canPostArm() bool { return f.ArmedFn == nil || f.ArmedFn() }

// Grab implements System: draws the widgets of the app whose window holds the
// centre (bright squares on a dark field).
func (f *FakeSystem) Grab(c Pt, size float64) (image.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.ScreenRec {
		return nil, errors.New("Screen Recording is off")
	}
	n := int(size)
	img := image.NewGray(image.Rect(0, 0, n, n))
	var wins []struct {
		w Rect
		a *FakeApp
	}
	for _, a := range f.Apps {
		if !a.Running {
			continue
		}
		for _, w := range a.Windows {
			wins = append(wins, struct {
				w Rect
				a *FakeApp
			}{w.Frame, a})
		}
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			g := c.X - size/2 + float64(x)
			h := c.Y - size/2 + float64(y)
			v := 18.0
			for _, wa := range wins {
				if !wa.w.Contains(Pt{g, h}) {
					continue
				}
				v = 40
				for _, wd := range wa.a.Widgets {
					cx := wa.w.X + wd.FX*wa.w.W + wa.a.PanelMove.X
					cy := wa.w.Y + wa.w.H - wd.FromBottom + wa.a.PanelMove.Y
					if math.Abs(g-cx) <= 12 && math.Abs(h-cy) <= 12 {
						v = 230
					}
				}
			}
			img.SetGray(x, y, color.Gray{Y: uint8(v)})
		}
	}
	return img, nil
}

// WidgetPoint is where a widget of the app's window is drawn right now.
func (f *FakeSystem) WidgetPoint(appID, key string) (Pt, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.Apps[appID]
	if a == nil || len(a.Windows) == 0 {
		return Pt{}, false
	}
	w := a.Windows[0].Frame
	for _, wd := range a.Widgets {
		if wd.Key == key {
			return Pt{w.X + wd.FX*w.W + a.PanelMove.X, w.Y + w.H - wd.FromBottom + a.PanelMove.Y}, true
		}
	}
	return Pt{}, false
}

// With runs fn on the app under the lock (the dev server's "resize / move").
func (f *FakeSystem) With(appID string, fn func(a *FakeApp)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a := f.Apps[appID]; a != nil {
		fn(a)
	}
}

// Recorded returns copies of the recorded moves and clicks and the pointer.
func (f *FakeSystem) Recorded() (cursor Pt, moves, clicks []Pt) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Cursor_, append([]Pt{}, f.Moves...), append([]Pt{}, f.Clicks...)
}
