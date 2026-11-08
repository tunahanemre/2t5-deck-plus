package config

import (
	"encoding/json"
	"testing"
)

func TestBindingIsLayer(t *testing.T) {
	b := Binding{Type: "layer", Layer: 2}
	if !b.IsLayer() {
		t.Error("Type=layer should satisfy IsLayer()")
	}
	if b.Layer != 2 {
		t.Errorf("Layer = %d, want 2", b.Layer)
	}
}

func TestBindingIsLayerFalseForOtherTypes(t *testing.T) {
	for _, ty := range []string{"key", "wheel", "cdl", "click", "property", "jog", ""} {
		if (Binding{Type: ty}).IsLayer() {
			t.Errorf("Type=%q must not satisfy IsLayer()", ty)
		}
	}
}

func TestLayerBindingUnmarshals(t *testing.T) {
	var b Binding
	if err := json.Unmarshal([]byte(`{"type":"layer","layer":3}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !b.IsLayer() || b.Layer != 3 {
		t.Errorf("got IsLayer=%v Layer=%d, want true/3", b.IsLayer(), b.Layer)
	}
}
