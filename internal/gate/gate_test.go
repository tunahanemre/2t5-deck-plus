package gate

import "testing"

func TestAllow(t *testing.T) {
	if !Allow(true, "color") {
		t.Error("frontmost+color must allow")
	}
	if Allow(false, "color") {
		t.Error("not frontmost must block")
	}
	if Allow(true, "edit") {
		t.Error("non-color must block")
	}
}
