package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/engine"
	"2t5-deck-plus/internal/keys"
	"2t5-deck-plus/internal/mouse"
)

// calibrateMode runs an interactive capture session, MERGING into the existing
// calibration.json. Only missing points are prompted.
func calibrateMode(recapture bool) {
	dest := "config/calibration.json"

	cfg, err := config.Load("config/device-map.json", "config/bindings.json")
	if err != nil {
		log.Fatalf("[calibrate] failed to load config: %v", err)
	}
	requiredPoints := engine.DeriveRequiredCalibPoints(cfg)

	type calibFile struct {
		Points map[string]map[string]any `json:"points"`
	}
	existing := calibFile{Points: map[string]map[string]any{}}
	if b, err := os.ReadFile(dest); err == nil {
		if err2 := json.Unmarshal(b, &existing); err2 != nil {
			log.Printf("[calibrate] warning: could not parse existing %s (%v) — will overwrite", dest, err2)
			existing.Points = map[string]map[string]any{}
		}
	}

	if recapture {
		// --recalibrate: discard every existing point so all of them are
		// re-prompted. Without this, --calibrate only ever asks for MISSING
		// points, so after a screen swap (which invalidates every absolute
		// coordinate) it prints "All N calibration points are already
		// present -- nothing to capture" and exits, leaving no supported way
		// to re-capture.
		fmt.Printf("--recalibrate: discarding %d existing point(s); re-capturing all.\n", len(existing.Points))
		existing.Points = map[string]map[string]any{}
	}

	var missing []engine.CalibrateCapture
	for _, p := range requiredPoints {
		if _, found := existing.Points[p.Key]; !found {
			missing = append(missing, p)
		}
	}

	if len(missing) == 0 {
		fmt.Printf("All %d calibration points are already present in %s — nothing to capture.\n",
			len(requiredPoints), dest)
		return
	}

	fmt.Println("=== 2t5-deck-plus --calibrate ===")
	fmt.Printf("Already captured: %d/%d points. Prompting for %d missing point(s).\n",
		len(requiredPoints)-len(missing), len(requiredPoints), len(missing))
	fmt.Println("For each prompt, hover the cursor at the indicated position in Resolve, then press Enter.")

	scanner := bufio.NewScanner(os.Stdin)
	for _, p := range missing {
		fmt.Printf("\n  [%s] %s, then press Enter...", p.Key, p.Hint)
		scanner.Scan()
		x, y := mouse.CursorPos()
		existing.Points[p.Key] = map[string]any{"x": x, "y": y, "axis": p.Axis}
		fmt.Printf("  captured %s at (%.1f, %.1f)\n", p.Key, x, y)
	}

	b, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		log.Fatal("marshal:", err)
	}
	if err := os.WriteFile(dest, b, 0644); err != nil {
		log.Fatal("write:", err)
	}
	fmt.Printf("\nWrote %s (%d points total)\n", dest, len(existing.Points))
}

func main() {
	doCalibrate := flag.Bool("calibrate", false, "capture any MISSING calibration points")
	doRecalibrate := flag.Bool("recalibrate", false, "discard ALL existing calibration points and re-capture every one (use after a screen/resolution change, which invalidates every absolute coordinate)")
	flag.Parse()

	if *doCalibrate || *doRecalibrate {
		calibrateMode(*doRecalibrate)
		return
	}

	// Arm the real input packages. This is the ONLY place in the codebase
	// that should ever do this — go test never reaches this line, and
	// --calibrate already returned above (it only reads the cursor via
	// mouse.CursorPos, which is unarmed-safe). From here on, mouse.Click/
	// Drag/Press/DragTo/Release/DoubleClick and keys.Send will post real
	// CGEvent input, matching the pre-arm-guard (v8 and earlier) behavior
	// byte-for-byte.
	mouse.Arm()
	keys.Arm()

	eng, err := engine.New("config")
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel ctx on SIGINT/SIGTERM so Stop() runs (joins the drain-loop
	// goroutine, unregisters MIDI, closes the sidecar, saves the sentinel)
	// instead of relying on an abrupt process kill.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := eng.Start(ctx); err != nil {
		log.Fatal(err)
	}

	<-ctx.Done()
	eng.Stop()
}
