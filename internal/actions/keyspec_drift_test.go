package actions

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Drift guard: the pure validator's key and modifier names must equal the
// tables in internal/keys/keys_darwin.go. Read as source text so the test
// never links the cgo package (nothing can arm or post an event).
func TestKeySpecTablesMatchKeysSource(t *testing.T) {
	b, err := os.ReadFile("../keys/keys_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	block := func(start string) string {
		i := strings.Index(src, start)
		if i < 0 {
			t.Fatalf("%s not found", start)
		}
		j := strings.Index(src[i:], "\n}\n")
		return src[i : i+j]
	}
	nameRe := regexp.MustCompile(`"((?:\\\\|[^"\\])+)":`)
	collect := func(blk string) map[string]bool {
		out := map[string]bool{}
		for _, line := range strings.Split(blk, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, m := range nameRe.FindAllStringSubmatch(line, -1) {
				out[strings.ReplaceAll(m[1], `\\`, `\`)] = true
			}
		}
		return out
	}
	keysSrc := collect(block("var keyTable"))
	modsSrc := collect(block("var modifierTable"))
	if len(keysSrc) < 60 || len(modsSrc) != 7 {
		t.Fatalf("parsed %d keys, %d modifiers", len(keysSrc), len(modsSrc))
	}
	for k := range keysSrc {
		if !specKeys[k] {
			t.Errorf("keys has key %q, pure validator does not", k)
		}
	}
	for k := range specKeys {
		if !keysSrc[k] {
			t.Errorf("pure validator has key %q, keys does not", k)
		}
	}
	for k := range modsSrc {
		if !specModifiers[k] {
			t.Errorf("keys has modifier %q, pure validator does not", k)
		}
	}
	for k := range specModifiers {
		if !modsSrc[k] {
			t.Errorf("pure validator has modifier %q, keys does not", k)
		}
	}
}

// The pure media-key names must equal keys.mediaKeyTable (read as source text).
func TestMediaSpecMatchesKeysSource(t *testing.T) {
	b, err := os.ReadFile("../keys/media_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "var mediaKeyTable")
	if i < 0 {
		t.Fatal("mediaKeyTable not found")
	}
	blk := src[i : i+strings.Index(src[i:], "\n}\n")]
	got := map[string]bool{}
	for _, m := range regexp.MustCompile(`"(\w+)":`).FindAllStringSubmatch(blk, -1) {
		got[m[1]] = true
	}
	if len(got) != len(specMedia) {
		t.Fatalf("keys %v vs validator %v", got, specMedia)
	}
	for k := range got {
		if !specMedia[k] {
			t.Errorf("keys has media key %q, validator does not", k)
		}
	}
	for _, spec := range []string{"media:play", "media:brightnessUp", "MEDIA:Next"} {
		if err := ValidateKeySpec(spec); err != nil {
			t.Errorf("%s: %v", spec, err)
		}
	}
	for _, spec := range []string{"media:", "media:stop", "Cmd+media:play"} {
		if ValidateKeySpec(spec) == nil {
			t.Errorf("%q should be invalid", spec)
		}
	}
}
