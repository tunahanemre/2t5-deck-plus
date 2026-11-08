// Command deck-editor-dev serves the 2T5 DECK+ preset editor without the app:
// the static editor page, the editor API on /api/, a fake frontmost app and
// running-apps list, and (--sim) simulated input events. It opens no MIDI
// port and imports no engine, midi, keys or mouse package.
//
// Start: go run ./cmd/deck-editor-dev --static app/frontend/public/editor --sim
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "deck-editor-dev:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("deck-editor-dev", flag.ContinueOnError)
	var c Config
	fs.StringVar(&c.Addr, "addr", "127.0.0.1:2541", "listen address (loopback only)")
	fs.StringVar(&c.Static, "static", "app/frontend/public/editor", "directory of the static editor, served at /editor/")
	fs.StringVar(&c.Data, "data", "", "presets directory (default: a fresh temp dir seeded from the live ld-resolve file)")
	fs.StringVar(&c.SeedFrom, "seed-from", "config/bindings.json", "2T5 DECK+ bindings.json used to seed app banks (Finder)")
	fs.StringVar(&c.Live, "live", "", "live ld-resolve bindings.json to seed Resolve from (default ~/Library/Application Support/ld-resolve/bindings.json)")
	fs.StringVar(&c.Token, "token", "", "require this value as the X-LD-Token header on every mutating request (like the packaged app's per-launch token); default none")
	fs.StringVar(&c.State, "state", "", "internal state folder (trust manifest + lastgood) for the startup outside-change check; point at a COPY of ~/Library/Application Support/2t5-deck-plus/state")
	fs.BoolVar(&c.EngineState, "engine-state", false, "behave like the engine for the deck lamps: /api/live sliderMode/customMode + POST /api/dev/deck-press")
	fs.StringVar(&c.AIDir, "ai-dir", "", "folder of the AI-access state file ai-access.json (default: a temp folder)")
	fs.StringVar(&c.AIExe, "ai-exe", "", "command path shown in the AI-access setup line")
	fs.BoolVar(&c.Sim, "sim", false, "publish simulated input events")
	if err := fs.Parse(args); err != nil {
		return err
	}
	logger := log.New(out, "", log.Ltime)
	c.Logf = logger.Printf
	srv, cleanup, err := New(c)
	if err != nil {
		return err
	}
	defer cleanup()
	ln, err := net.Listen("tcp", c.Addr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.StartSim(ctx)
	hs := &http.Server{Handler: srv.Handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = hs.Shutdown(sc)
	}()
	abs, _ := filepath.Abs(c.Static)
	logger.Printf("editor dev server on http://%s/  (static %s, presets %s, sim=%v)", ln.Addr(), abs, srv.DataDir, c.Sim)
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
