package main

import (
	"encoding/json"
	"strings"
	"testing"

	"2t5-deck-plus/internal/editorapi"
	"2t5-deck-plus/internal/preset"
)

type fakeArm struct{ on bool }

func (f *fakeArm) Armed() bool     { return f.on }
func (f *fakeArm) SetArmed(b bool) { f.on = b }

func TestEditorRequestRunsWritesInProcessWithTheToken(t *testing.T) {
	st, err := preset.Open(t.TempDir(), preset.Options{LiveResolvePath: "-", PollInterval: -1, Controls: editorapi.ControlIDs()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	arm := &fakeArm{on: false}
	h := editorapi.NewHandler(editorapi.Deps{Store: st, Arm: arm, Token: "TOK"})
	a := &App{editor: &editorWiring{token: "TOK", handler: h}}

	r := a.EditorRequest("POST", "/api/arm", `{"armed":true}`)
	if r.Status != 200 || !arm.on {
		t.Fatalf("arm via EditorRequest: %d %s (armed=%v)", r.Status, r.Body, arm.on)
	}
	var out struct{ Armed bool }
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil || !out.Armed {
		t.Errorf("body %s", r.Body)
	}
	if r := a.EditorRequest("GET", "/api/arm", ""); r.Status != 405 {
		t.Errorf("GET must be refused here, got %d", r.Status)
	}
	if r := a.EditorRequest("POST", "/etc/passwd", ""); r.Status != 400 {
		t.Errorf("non-api path: %d", r.Status)
	}
	if r := a.EditorRequest("POST", "/api/arm", strings.Repeat("x", 2<<20)); r.Status != 400 {
		t.Errorf("huge body: %d", r.Status)
	}
	// a handler that wants a different token still refuses (the bound method does not bypass the guard)
	a.editor.token = "WRONG"
	if r := a.EditorRequest("POST", "/api/arm", `{"armed":false}`); r.Status != 403 {
		t.Errorf("wrong token: %d", r.Status)
	}
}
