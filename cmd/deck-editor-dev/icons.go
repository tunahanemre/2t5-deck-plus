package main

import (
	"strings"

	"2t5-deck-plus/internal/editorapi"
	"2t5-deck-plus/internal/macapp"
)

// realIcons gives the sim the same real icons the packaged app shows: the installed app's own icon via
// NSWorkspace (macapp.AppIconPNG). An app that is not installed has none here; the server then falls back
// to the icon bundled with its standard preset. Ids ending in ".noicon" stay icon-less for the 404 tests.
type realIcons struct{}

func (realIcons) AppIconPNG(id string) ([]byte, error) {
	if strings.HasSuffix(id, ".noicon") {
		return nil, editorapi.ErrNoIcon
	}
	if b := macapp.AppIconPNG(id); b != nil {
		return b, nil
	}
	return nil, editorapi.ErrNoIcon
}
