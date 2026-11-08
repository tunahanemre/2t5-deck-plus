package editorapi

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoInternalNamesInUserVisibleFiles keeps studio-internal names out of
// everything a stranger can read or see: the plists (permission prompts), the
// editor assets, the web shell, the README, AGENTS.md, the AI-access guide and
// the dependency list. Functional code (datadir defaults, build-app.sh, the
// sidecar's mirror of them) is NOT scanned here: those paths are deliberate and
// are labelled "studio default; override with DECKPLUS_DATA_DIR".
func TestNoInternalNamesInUserVisibleFiles(t *testing.T) {
	root := filepath.Join("..", "..")
	banned := []string{"2T5-WS-", "WS-01", "WS-02", "Dodee", "DODEE", "/Volumes/2T5"}
	// allowed: path (slash form, relative to the repo root) -> substrings that may stay. Keep this empty unless a hit is functional.
	allowed := map[string][]string{}

	files := []string{
		"README.md", "AGENTS.md", "docs/ai-access.md", "docs/dependencies.json",
		"app/build/darwin/Info.plist", "app/build/darwin/Info.dev.plist", "app/frontend/index.html",
	}
	for _, dir := range []string{"app/frontend/public/editor", "app/frontend/src"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch filepath.Ext(p) {
			case ".js", ".mjs", ".css", ".html", ".json":
				rel, _ := filepath.Rel(root, p)
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		text := string(b)
		for _, ok := range allowed[rel] {
			text = strings.ReplaceAll(text, ok, "")
		}
		for _, bad := range banned {
			if i := strings.Index(text, bad); i >= 0 {
				t.Errorf("%s contains internal name %q near %q", rel, bad, text[i:min(len(text), i+60)])
			}
		}
	}
}

// TestNoPeopleOrAgentNamesInShippedSource keeps person names, internal agent
// names and chat-quote markers out of every shipped source file (comments and
// strings alike). Decisions are written as plain technical comments saying what
// and why, never as attributed quotes.
func TestNoPeopleOrAgentNamesInShippedSource(t *testing.T) {
	root := filepath.Join("..", "..")
	banned := []string{"Tunahan", "tunahan", "claude-dev", "app-manager", "post-producer", "3d-artist",
		"backend-developer", "frontend-developer", "Master Chef", "his words", "he said", "per user request"}
	// The public brand links (and nothing else) may carry the founder's handle.
	urls := []string{
		"instagram.com/tunahanemre", "github.com/sponsors/tunahanemre", "github.com/tunahanemre/", "github: tunahanemre",
	}
	// allowedFiles: whole files that may break the rule, with the reason.
	allowedFiles := map[string]string{}
	// Shipped roots; docs/notes, docs/plans and docs/specs are excluded from the public export.
	dirs := []string{"app", "internal", "cmd", "sidecar", "scripts", "config", "tools", ".github"}
	rootFiles := []string{"README.md", "AGENTS.md", "docs/ai-access.md", "docs/dependencies.json"}
	exts := map[string]bool{".go": true, ".py": true, ".sh": true, ".js": true, ".mjs": true, ".css": true, ".html": true,
		".json": true, ".m": true, ".plist": true, ".yml": true, ".md": true}
	skipDir := map[string]bool{"node_modules": true, "wailsjs": true, "dist": true, "bin": true}

	var files []string
	files = append(files, rootFiles...)
	for _, dir := range dirs {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if skipDir[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !exts[filepath.Ext(name)] || strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, "test_") ||
				strings.HasSuffix(name, ".test.mjs") || name == "package-lock.json" {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
	}
	if len(files) < 50 {
		t.Fatalf("scanned only %d files: the walk is broken", len(files))
	}
	for _, rel := range files {
		if _, skip := allowedFiles[rel]; skip {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		text := string(b)
		for _, u := range urls {
			text = strings.ReplaceAll(text, u, "")
		}
		for _, bad := range banned {
			if i := strings.Index(text, bad); i >= 0 {
				t.Errorf("%s contains %q near %q", rel, bad, text[max(0, i-30):min(len(text), i+50)])
			}
		}
	}
}
