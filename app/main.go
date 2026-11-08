// Command app is the Wails v2 entry point for the 2T5 DECK+ Status app — a
// signed, double-click-launchable macOS Dock app that wraps the existing
// internal/engine.Engine (unchanged grading logic) with a Start/Stop + live
// status window. See app.go for the bound App struct/methods and
// docs/plans/2026-07-01-ld-resolve-app-phase1.md for the full Phase 1 plan.
//
// Build with `wails build` from this directory (app/). The Go module is the
// repo-root module "2t5-deck-plus" — this directory has no go.mod of its own,
// so `2t5-deck-plus/internal/...` imports resolve exactly like they do from
// cmd/2t5-deck-plus.
package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"2t5-deck-plus/internal/aiaccess"
	"2t5-deck-plus/internal/datadir"
	"2t5-deck-plus/internal/hud"
	"2t5-deck-plus/internal/sidecar"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// The terminal command and the MCP server are this same executable: `2t5-deck-plus mcp`, `2t5-deck-plus apps` ...
	// They are thin clients of the running app's AI door and never start the window, the engine or MIDI.
	if len(os.Args) > 1 && aiaccess.Commands[os.Args[1]] {
		os.Exit(aiaccess.Main())
	}
	// Redirect the engine's Go log to a file. Wails swallows stderr at
	// runtime (only pre-wails.Run lines survive a shell redirect), which
	// made live engine debugging (MIDI decode, drain dispatch, sidecar
	// client state, page resolution) effectively blind. A tail-able file at
	// a fixed path restores that visibility with zero UI cost.
	// Lives in the WS-01 data root alongside config/ and state/ (see
	// internal/datadir). If the volume is not mounted, datadir.Dir returns
	// an error instead of creating the path, and the log simply stays on
	// stderr -- a missing log file is never a reason to fail startup.
	//
	// The old-name folder copy runs BEFORE datadir.Dir: Dir records the data-root
	// marker in that same per-user folder, and MigrateDir only copies while the
	// new folder is still missing. The result is logged further down.
	migDone, migFrom, migTo, migErr := datadir.MigrateAppSupport()
	if logDir, err := datadir.Dir(datadir.Logs); err != nil {
		log.Printf("[app] log file unavailable, keeping stderr: %v", err)
	} else if f, err := os.OpenFile(filepath.Join(logDir, "2t5-deck-plus.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(f)
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
		log.Printf("=== 2t5-deck-plus started ===")
	}

	// One-time carry-over from the app's old name (ld-deck). It only COPIES: the
	// old folder and the old state files stay where they are. Each step logs once,
	// the first time it actually copies something.
	if migErr != nil {
		log.Printf("[app] could not copy the old user folder %s to %s: %v", migFrom, migTo, migErr)
	} else if migDone {
		log.Printf("[app] first start under the new name: copied %s to %s (the old folder is left untouched)", migFrom, migTo)
	}
	if names, err := datadir.MigrateDataFiles(); err != nil {
		log.Printf("[app] could not copy the old state files: %v", err)
	} else if len(names) > 0 {
		log.Printf("[app] first start under the new name: copied the old state files %v inside %s", names, datadir.Root())
	}

	// Reap any sidecar.py process left over from a PREVIOUS run before this
	// run's own engine.Start() (called from app.startup, below) ever calls
	// sidecar.Launch (2026-08-13 fix, "duplicate sidecar feedback loop"
	// live incident -- see sidecar.ReapOrphans's doc comment). Same
	// go-test-safety placement as hud.Arm()/mouse.Arm()/keys.Arm() below:
	// func main() is never invoked by `go test`, and ReapOrphans is
	// deliberately NOT wired into any test-covered path (Launch,
	// recoverSidecar) for exactly that reason -- this is the one and only
	// call site. excludePID=0: this process has not spawned a sidecar of
	// its own yet, so nothing is exempt from the sweep.
	if killed := sidecar.ReapOrphans(0); len(killed) > 0 {
		log.Printf("[app] reaped %d orphaned sidecar.py process(es) from a previous run: %v", len(killed), killed)
	}

	// Arm the HUD package's native-window gate. This is the ONLY place in
	// the codebase that should ever do this -- go test never reaches this
	// line (package main's func main() is never invoked by the generated
	// test binary; this is the exact same proven-safe placement as
	// cmd/2t5-deck-plus/main.go's mouse.Arm()/keys.Arm() calls). Unlike
	// mouse/keys (gated behind StartEngine + an Accessibility check, since
	// those post real input to Resolve), the HUD is a harmless internal
	// status window with no such gate -- arming it unconditionally here
	// just means Show()/Toggle()/Update() actually reach AppKit once this
	// real process starts, instead of staying a permanent no-op.
	hud.Arm()

	app := NewApp()

	// Bundle ID (wails.json has no such field pre-v3; it's hardcoded in
	// build/darwin/Info.plist/Info.dev.plist as com.2t5.deck-plus — see
	// those files' CFBundleIdentifier key). LSUIElement is intentionally
	// absent from both plists, which makes this a normal Dock app.
	err := wails.Run(&options.App{
		Title:  "2T5 DECK+",
		Width:  1360,
		Height: 860,
		// below this the editor's deck labels are unreadable
		MinWidth:  1180,
		MinHeight: 700,
		AssetServer: &assetserver.Options{
			Assets: assets,
			// /api/ (the preset editor) is served by Go; Assets stays the fallback for the UI files.
			Handler: app.APIHandler(),
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		// Tunahan 2026-11-04 "full screen available app": without Mac options Wails disables the zoom button, so the green button could not go full screen
		Mac:        &mac.Options{},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
