package cfgmerge

import (
	"encoding/json"
	"testing"
)

func parse(t *testing.T, s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAdditive_AddsMissingKeysAndNeverTouchesExistingOnes(t *testing.T) {
	src := parse(t, `{"col":{"Lift":{"type":"wheel","sens":15},"Gamma":{"type":"wheel"}},"edit":{"D1":{"type":"jog"}}}`)
	dst := parse(t, `{"col":{"Lift":{"type":"wheel","sens":99}}}`)
	if !Additive(src, dst) {
		t.Fatal("nothing added")
	}
	col := dst["col"].(map[string]any)
	if col["Lift"].(map[string]any)["sens"].(float64) != 99 {
		t.Error("a user value was overwritten")
	}
	if col["Gamma"] == nil || dst["edit"] == nil {
		t.Errorf("missing keys not added: %v", dst)
	}
	if Additive(src, dst) {
		t.Error("not idempotent")
	}
}

func TestAdditive_CaseInsensitiveKeysNeverYieldBothSpellings(t *testing.T) {
	src := parse(t, `{"col":{"Lift":{"type":"wheel","sens":15,"maxDrag":10}}}`)
	dst := parse(t, `{"col":{"lift":{"type":"wheel","Sens":7}}}`) // a legacy spelling in the user's file
	changed := Additive(src, dst)
	col := dst["col"].(map[string]any)
	if len(col) != 1 {
		t.Fatalf("both spellings of the control exist: %v", col)
	}
	lift := col["lift"].(map[string]any)
	if _, both := lift["sens"]; both {
		t.Errorf("both Sens and sens in the result: %v", lift)
	}
	if lift["Sens"].(float64) != 7 {
		t.Errorf("the user's value was changed: %v", lift)
	}
	if !changed || lift["maxDrag"].(float64) != 10 {
		t.Errorf("a genuinely missing key was not added: %v changed=%v", lift, changed)
	}
}
