package editorapi

import (
	"fmt"
	"strings"
	"testing"
)

// Decision 2026-11-05: an AI tool (same editor path, X-LD-Editor) may assign
// actions that run a program; nothing refuses them any more.
func TestAIPath_ShellAssignmentIsAccepted(t *testing.T) {
	e := newEnv(t, false)
	if code, out := e.do(t, "POST", "/api/apps", `{"id":"com.example.Tool"}`); code != 201 {
		t.Fatal(code, out)
	}
	for _, body := range []string{
		`{"bank":"base","control":"C1","assignment":{"action":"generic.shell","cmd":"/usr/bin/open","args":["-a","Calculator"]}}`,
		`{"bank":"base","control":"C2","assignment":{"type":"shell","cmd":"/bin/sh","args":["-c","echo hi"]}}`,
	} {
		if code, out := e.do(t, "PUT", "/api/preset/com.example.Tool/assign", body); code != 200 {
			t.Fatalf("shell assignment refused: %d %s", code, out)
		}
	}
}

// hudWithoutResolve is a Resolve-preset setting: read from the registry,
// written through PUT /api/preset/{id}/settings, boolean only.
func TestHUDWithoutResolve_Setting(t *testing.T) {
	e := newEnv(t, true)
	id := "com.blackmagic-design.DaVinciResolve"
	if code, out := e.do(t, "GET", "/api/registry", ""); code != 200 || !strings.Contains(fmt.Sprint(out), `hudWithoutResolve`) {
		t.Fatalf("registry lacks the setting: %d %s", code, out)
	}
	if code, out := e.do(t, "PUT", "/api/preset/"+id+"/settings", `{"key":"hudWithoutResolve","value":false}`); code != 200 {
		t.Fatalf("set false: %d %s", code, out)
	}
	if code, out := e.do(t, "PUT", "/api/preset/"+id+"/settings", `{"key":"hudWithoutResolve","value":"yes"}`); code != 400 {
		t.Fatalf("non-bool must be refused: %d %s", code, out)
	}
	if code, out := e.do(t, "GET", "/api/preset/"+id, ""); code != 200 || !strings.Contains(fmt.Sprint(out), `hudWithoutResolve:false`) {
		t.Fatalf("not stored: %d %s", code, out)
	}
}
