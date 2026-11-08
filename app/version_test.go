package main

import (
	"encoding/json"
	"os"
	"testing"

	"2t5-deck-plus/internal/aiaccess"
)

func TestAppVersionMatchesWailsJSON(t *testing.T) {
	raw, err := os.ReadFile("wails.json")
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Info struct {
			ProductVersion string `json:"productVersion"`
		} `json:"info"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	if w.Info.ProductVersion == "" || w.Info.ProductVersion != appVersion {
		t.Fatalf("appVersion %q != wails.json productVersion %q: change both together", appVersion, w.Info.ProductVersion)
	}
	if aiaccess.Version != appVersion {
		t.Fatalf("aiaccess.Version %q != appVersion %q: change both together", aiaccess.Version, appVersion)
	}
}
