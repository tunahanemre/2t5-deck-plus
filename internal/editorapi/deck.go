package editorapi

import (
	"embed"
	"encoding/json"
	"sync"
)

// The Loupedeck+ device profile and layout rects are DECKMAP's
// (internal/device/profiles/loupedeck-plus.json), copied unchanged.
//
//go:embed profile/loupedeck-plus.json
var profileFS embed.FS

var (
	profileOnce sync.Once
	profileRaw  []byte
	profileIDs  []string
)

func loadProfile() {
	profileRaw, _ = profileFS.ReadFile("profile/loupedeck-plus.json")
	var p struct {
		Controls []struct {
			ID string `json:"id"`
		} `json:"controls"`
	}
	_ = json.Unmarshal(profileRaw, &p)
	for _, c := range p.Controls {
		profileIDs = append(profileIDs, c.ID)
	}
}

// DeckJSON is the embedded device profile served by GET /api/deck.
func DeckJSON() []byte { profileOnce.Do(loadProfile); return profileRaw }

// ControlIDs lists every control id of the deck (knobs, sliders, buttons),
// for preset.Options.Controls.
func ControlIDs() []string {
	profileOnce.Do(loadProfile)
	return append([]string(nil), profileIDs...)
}
