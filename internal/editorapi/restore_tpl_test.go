package editorapi

import (
	"fmt"
	"strings"
	"testing"
)

// Restore defaults of a starter-template app returns the template's controls
// and says so; a custom app answers 400 reason no-shipped-preset.
func TestRestoreDefaultsResponseShape(t *testing.T) {
	e := newEnv(t, false)
	code, row := e.do(t, "POST", "/api/apps/from-template", `{"templateId":"safari"}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, row)
	}
	want := row["controls"].(float64)
	if want < 1 {
		t.Fatal("template has no controls")
	}
	code, out := e.do(t, "POST", "/api/preset/com.apple.Safari/reset-defaults", "")
	rt, _ := out["restoredTo"].(map[string]any)
	if code != 200 || out["ok"] != true || rt["source"] != "template" || rt["templateId"] != "safari" || rt["controls"] != want || rt["templateName"] == "" {
		t.Fatalf("reset-defaults: %d %v", code, out)
	}
	code, out = e.do(t, "POST", "/api/preset/com.apple.Safari/restore", `{"scope":"app"}`)
	if rt, _ = out["restoredTo"].(map[string]any); code != 200 || rt["source"] != "template" {
		t.Fatalf("restore app: %d %v", code, out)
	}
	if code, out = e.do(t, "POST", "/api/apps", `{"id":"com.example.Custom","name":"Custom"}`); code != 201 {
		t.Fatalf("add custom: %d %v", code, out)
	}
	for _, path := range []string{"/api/preset/com.example.Custom/reset-defaults", "/api/preset/com.example.Custom/restore"} {
		body := `{"scope":"app"}`
		code, out = e.do(t, "POST", path, body)
		if code != 400 || out["reason"] != "no-shipped-preset" || out["error"] != "no shipped preset for this app" || out["field"] != "id" {
			t.Errorf("%s: %d %v", path, code, out)
		}
	}
}

// An app whose bundle id has two templates and no recorded templateId restores
// to the best-overlap one, says "template-guess" and names it; the history
// label names the app.
func TestRestoreDefaultsTemplateGuessResponse(t *testing.T) {
	e := newEnv(t, false)
	if code, out := e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome","name":"Google Chrome"}`); code != 201 {
		t.Fatalf("add: %d %v", code, out)
	}
	code, out := e.do(t, "POST", "/api/preset/com.google.Chrome/reset-defaults", "")
	rt, _ := out["restoredTo"].(map[string]any)
	if code != 200 || rt["source"] != "template-guess" || rt["templateId"] == "" || rt["templateName"] == "" || rt["controls"].(float64) < 1 {
		t.Fatalf("guess: %d %v", code, out)
	}
	code, out = e.do(t, "POST", "/api/preset/com.google.Chrome/reset-defaults", "")
	if rt, _ = out["restoredTo"].(map[string]any); code != 200 || rt["source"] != "template" {
		t.Fatalf("second restore should be exact: %d %v", code, out)
	}
	_, h := e.do(t, "GET", "/api/history", "")
	if s := fmt.Sprint(h); !strings.Contains(s, "Restore Google Chrome defaults") {
		t.Errorf("history label missing: %s", s)
	}
}
