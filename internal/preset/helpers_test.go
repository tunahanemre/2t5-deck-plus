package preset

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func raw(t *testing.T, s string) map[string]any {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}

// seededStore opens a store in a fresh temp dir seeded from the testdata
// snapshots (never from the machine's live file).
func seededStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "presets")
	s, err := Open(dir, Options{
		SeedFrom:        filepath.Join("testdata", "2t5-deck-plus-repo-bindings.json"),
		LiveResolvePath: filepath.Join("testdata", "live-ld-resolve-bindings.json"),
		PollInterval:    -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, dir
}

// emptyStore has only the Global preset plus whatever the test adds.
func emptyStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "presets")
	s, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, dir
}

func jsonNumber(s string) any { return json.Number(s) }
