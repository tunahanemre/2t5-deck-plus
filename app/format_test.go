package main

// Tests for formatHUD -- the pure Go formatter that turns an engine.Status
// snapshot into a hud.HUDModel (HUD v2, rich WKWebView readout). No CGO, no
// window: this is the ONLY test coverage for the HUD's content logic
// (internal/hud itself has no test files -- see that package's arm-guard
// doc comment for why a CGO/AppKit package is deliberately left untested
// by `go test`).

import (
	"testing"

	"2t5-deck-plus/internal/engine"
	"2t5-deck-plus/internal/hud"
)

// TestFormatHUD_Stale locks in the 2026-08-24 fix's straight passthrough
// of Status.SidecarStale -> HUDModel.Stale (see both fields' doc comments):
// a moment-old, carried-forward snapshot while the sidecar is busy but
// alive (ping-confirmed), never a connectivity signal on its own -- LED is
// independent and can be green while Stale is true.
func TestFormatHUD_Stale(t *testing.T) {
	cases := []struct {
		name   string
		status engine.Status
		want   bool
	}{
		{"not_stale_by_default", engine.Status{Running: true, SidecarConnected: true}, false},
		{"stale_while_connected", engine.Status{Running: true, SidecarConnected: true, SidecarStale: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHUD(tc.status).Stale
			if got != tc.want {
				t.Errorf("Stale = %v, want %v", got, tc.want)
			}
			if led := formatHUD(tc.status).LED; led != "green" {
				t.Errorf("LED = %v, want green (Stale must not change connectivity display)", led)
			}
		})
	}
}

func TestFormatHUD_LED(t *testing.T) {
	cases := []struct {
		name   string
		status engine.Status
		want   string
	}{
		{"stopped", engine.Status{Running: false}, "grey"},
		{"stopped_but_sidecar_connected_still_grey", engine.Status{Running: false, SidecarConnected: true}, "grey"},
		{"running_connected", engine.Status{Running: true, SidecarConnected: true}, "green"},
		{"running_not_connected", engine.Status{Running: true, SidecarConnected: false}, "amber"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHUD(tc.status).LED
			if got != tc.want {
				t.Errorf("LED = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFormatHUD_PageBadge(t *testing.T) {
	cases := []struct {
		name   string
		status engine.Status
		want   string
	}{
		{"color", engine.Status{Page: "color"}, "Color"},
		{"edit", engine.Status{Page: "edit"}, "Edit"},
		{"empty_before_first_tick", engine.Status{Page: ""}, "—"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHUD(tc.status).Page
			if got != tc.want {
				t.Errorf("Page = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatHUD_GateOpen(t *testing.T) {
	if !formatHUD(engine.Status{GateAllow: true}).GateOpen {
		t.Error("GateOpen = false, want true")
	}
	if formatHUD(engine.Status{GateAllow: false}).GateOpen {
		t.Error("GateOpen = true, want false")
	}
}

// TestFormatHUD_Timecode_DashesWhenDisconnected is the named test for the
// design doc's explicit requirement: Status.Timecode is "" whenever there
// is no live timeline (no project open, or Resolve/sidecar unreachable) --
// formatHUD must pass it through as-is (the web template renders the
// dashes, not formatHUD itself, per "formatter + web template render
// missing fields gracefully"). This test locks in that HUDModel.Timecode
// is a pure passthrough, never a Go-side "--:--:--:--" substitution.
func TestFormatHUD_Timecode_Passthrough(t *testing.T) {
	cases := []struct {
		name   string
		status engine.Status
		want   string
	}{
		{"populated", engine.Status{Timecode: "01:04:22:11"}, "01:04:22:11"},
		{"empty_no_timeline", engine.Status{Timecode: ""}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHUD(tc.status).Timecode
			if got != tc.want {
				t.Errorf("Timecode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatHUD_TimecodeSub(t *testing.T) {
	cases := []struct {
		name   string
		status engine.Status
		want   string
	}{
		{
			"connected_project_and_timeline",
			engine.Status{Running: true, SidecarConnected: true, Project: "2026-06-DODEE-EP10", TimelineName: "EP10-v0"},
			"2026-06-DODEE-EP10 · EP10-v0",
		},
		{
			"connected_project_only",
			engine.Status{Running: true, SidecarConnected: true, Project: "2026-06-DODEE-EP10", TimelineName: ""},
			"2026-06-DODEE-EP10",
		},
		{
			"connected_timeline_only",
			engine.Status{Running: true, SidecarConnected: true, Project: "", TimelineName: "EP10-v0"},
			"EP10-v0",
		},
		{
			"connected_both_empty",
			engine.Status{Running: true, SidecarConnected: true},
			"",
		},
		{
			"not_running",
			engine.Status{Running: false},
			"engine stopped",
		},
		{
			"running_not_connected",
			engine.Status{Running: true, SidecarConnected: false},
			"engine running · no Resolve link",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHUD(tc.status).TimecodeSub
			if got != tc.want {
				t.Errorf("TimecodeSub = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatHUD_ErrorMessage(t *testing.T) {
	cases := []struct {
		name   string
		status engine.Status
		want   string
	}{
		{"connected_no_error", engine.Status{Running: true, SidecarConnected: true}, ""},
		{"not_running", engine.Status{Running: false}, "Engine stopped."},
		{
			"running_disconnected_with_sidecar_error",
			engine.Status{Running: true, SidecarConnected: false, SidecarError: "python3 not found"},
			"Sidecar error: python3 not found",
		},
		{
			"running_disconnected_no_error_text",
			engine.Status{Running: true, SidecarConnected: false, SidecarError: ""},
			"Sidecar can't reach DaVinci Resolve.\nOpen Resolve, then press Start.",
		},
		{
			"running_waiting_for_resolve",
			engine.Status{Running: true, SidecarConnected: false, SidecarState: engine.SidecarStateWaiting},
			"Waiting for DaVinci Resolve.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHUD(tc.status).ErrorMessage
			if got != tc.want {
				t.Errorf("ErrorMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFormatHUD_Knobs_PerPageMap_AndActiveHighlight is the named test for
// the mandatory 3-way page branch on the knob-map row: Color shows the
// Lift/Gamma/Gain/Offset map with the active wheel highlighted; Edit shows
// the fixed Scale/Pos X/Pos Y/Crop map with NONE active (each maps
// continuously); every other page (including empty/before-first-tick)
// renders 4 fully blank cells -- never a stale Color-page highlight or
// Edit-page labels leaking onto Cut/Fusion/Fairlight/Deliver/Media, the
// same hazard class as v1's Row4/ActiveWheel finding.
func TestFormatHUD_Knobs_PerPageMap_AndActiveHighlight(t *testing.T) {
	blank := [4]hud.HUDKnob{{}, {}, {}, {}}

	cases := []struct {
		name   string
		status engine.Status
		want   [4]hud.HUDKnob
	}{
		{
			"color_gamma_active",
			engine.Status{Page: "color", ActiveWheel: "gamma"},
			[4]hud.HUDKnob{
				{Label: "Lift"}, {Label: "Gamma", Active: true}, {Label: "Gain"}, {Label: "Offset"},
			},
		},
		{
			"color_lift_active_default",
			engine.Status{Page: "color", ActiveWheel: "lift"},
			[4]hud.HUDKnob{
				{Label: "Lift", Active: true}, {Label: "Gamma"}, {Label: "Gain"}, {Label: "Offset"},
			},
		},
		{
			"edit_none_active",
			engine.Status{Page: "edit", ActiveWheel: "lift"}, // ActiveWheel set but irrelevant on Edit
			[4]hud.HUDKnob{
				// HaveEditClip is false (zero value) here -- Scale/Pos X/
				// Pos Y render "—" (never a stale/zero number), Crop stays
				// label-only.
				{Label: "Scale", Value: "—"}, {Label: "Pos X", Value: "—"}, {Label: "Pos Y", Value: "—"}, {Label: "Crop"},
			},
		},
		{"other_page_cut_blank_even_with_activewheel_set", engine.Status{Page: "cut", ActiveWheel: "lift"}, blank},
		{"other_page_fusion_blank", engine.Status{Page: "fusion", ActiveWheel: "gain"}, blank},
		{"other_page_fairlight_blank", engine.Status{Page: "fairlight", ActiveWheel: "offset"}, blank},
		{"other_page_deliver_blank", engine.Status{Page: "deliver", ActiveWheel: "lift"}, blank},
		{"other_page_media_blank", engine.Status{Page: "media", ActiveWheel: "lift"}, blank},
		{"empty_page_blank", engine.Status{Page: "", ActiveWheel: "lift"}, blank},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHUD(tc.status).Knobs
			if got != tc.want {
				t.Errorf("Knobs (page=%q) = %+v, want %+v", tc.status.Page, got, tc.want)
			}
		})
	}
}

// TestFormatHUD_Knobs_Edit_LiveValues covers the 2026-07-08 HUD v2
// Edit-page addition: Scale/Pos X/Pos Y show the target clip's live
// Zoom/Pan/Tilt when HaveEditClip is true, "—" when it isn't (never a
// stale/zero number) -- Crop stays label-only either way, and Color/other
// pages must NEVER show a numeric Value (CDL isn't readable).
func TestFormatHUD_Knobs_Edit_LiveValues(t *testing.T) {
	cases := []struct {
		name   string
		status engine.Status
		want   [4]hud.HUDKnob
	}{
		{
			"edit_with_target_clip",
			engine.Status{
				Page: "edit", HaveEditClip: true,
				EditClipZoom: 1.4, EditClipPan: 12.5, EditClipTilt: -3.25,
			},
			[4]hud.HUDKnob{
				{Label: "Scale", Value: "1.400"}, {Label: "Pos X", Value: "12.500"},
				{Label: "Pos Y", Value: "-3.250"}, {Label: "Crop"},
			},
		},
		{
			"edit_no_target_clip",
			engine.Status{Page: "edit", HaveEditClip: false},
			[4]hud.HUDKnob{
				{Label: "Scale", Value: "—"}, {Label: "Pos X", Value: "—"},
				{Label: "Pos Y", Value: "—"}, {Label: "Crop"},
			},
		},
		{
			"edit_default_zoom_pan_tilt_all_zero",
			engine.Status{Page: "edit", HaveEditClip: true, EditClipZoom: 1.0},
			[4]hud.HUDKnob{
				{Label: "Scale", Value: "1.000"}, {Label: "Pos X", Value: "0.000"},
				{Label: "Pos Y", Value: "0.000"}, {Label: "Crop"},
			},
		},
		{
			"color_page_never_shows_values_even_if_haveeditclip_somehow_true",
			engine.Status{Page: "color", HaveEditClip: true, EditClipZoom: 1.4, ActiveWheel: "gamma"},
			[4]hud.HUDKnob{
				{Label: "Lift"}, {Label: "Gamma", Active: true}, {Label: "Gain"}, {Label: "Offset"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHUD(tc.status).Knobs
			if got != tc.want {
				t.Errorf("Knobs (page=%q) = %+v, want %+v", tc.status.Page, got, tc.want)
			}
		})
	}
}

// TestFormatHUD_EditClip_NameNearKnobMap covers the 2026-07-08 HUD v2
// addition: the Edit-page target clip's NAME row (hud.HUDModel.EditClip).
// Always populated on Edit (even with no active D1/direction-button
// selection -- unlike Clip/ActiveClip, which stays "" until a selection is
// pinned), "—" when no target clip, and "" (hidden) on every other page.
func TestFormatHUD_EditClip_NameNearKnobMap(t *testing.T) {
	cases := []struct {
		name   string
		status engine.Status
		want   string
	}{
		{
			"edit_with_target_clip",
			engine.Status{Page: "edit", HaveEditClip: true, EditClipName: "DSCF9804.MOV"},
			"DSCF9804.MOV",
		},
		{
			"edit_no_target_clip",
			engine.Status{Page: "edit", HaveEditClip: false},
			"—",
		},
		{
			"edit_haveeditclip_true_but_empty_name",
			engine.Status{Page: "edit", HaveEditClip: true, EditClipName: ""},
			"—",
		},
		{"color_page_hidden", engine.Status{Page: "color", HaveEditClip: true, EditClipName: "x.mov"}, ""},
		{"other_page_hidden", engine.Status{Page: "cut", HaveEditClip: true, EditClipName: "x.mov"}, ""},
		{"empty_page_hidden", engine.Status{Page: "", HaveEditClip: true, EditClipName: "x.mov"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHUD(tc.status).EditClip
			if got != tc.want {
				t.Errorf("EditClip (page=%q) = %q, want %q", tc.status.Page, got, tc.want)
			}
		})
	}
}

// TestFormatHUD_Context_ThreeWayPageBranch is HUD v2's version of v1's
// TestFormatHUD_Row4_ThreeWayPageBranch -- see that test's doc comment
// (app/format_test.go history) for the full rationale: Status.ActiveWheel
// is NOT page-gated by the engine (copied into Status every tick
// regardless of page), so a naive 2-way branch would leak a stale
// "Wheel: Lift" onto every non-Color page. This function branches THREE
// ways: "edit" -> marker context (or blank if inactive); "color" -> active
// wheel; anything else -> blank.
func TestFormatHUD_Context_ThreeWayPageBranch(t *testing.T) {
	type want struct{ label, value string }
	cases := []struct {
		name   string
		status engine.Status
		want   want
	}{
		{
			"edit_with_marker_mode",
			engine.Status{Page: "edit", MarkerMode: "active @1.5s Blue"},
			want{"Marker", "active @1.5s Blue"},
		},
		{
			"edit_no_marker_mode_blank",
			engine.Status{Page: "edit", MarkerMode: ""},
			want{"", ""},
		},
		{
			"color_shows_active_wheel",
			engine.Status{Page: "color", ActiveWheel: "gamma"},
			want{"Wheel", "Gamma"},
		},
		{
			"color_default_wheel_lift",
			engine.Status{Page: "color", ActiveWheel: "lift"},
			want{"Wheel", "Lift"},
		},
		{
			"other_page_cut_blank_even_though_activewheel_set",
			engine.Status{Page: "cut", ActiveWheel: "lift"},
			want{"", ""},
		},
		{"other_page_fusion_blank", engine.Status{Page: "fusion", ActiveWheel: "gain"}, want{"", ""}},
		{"other_page_fairlight_blank", engine.Status{Page: "fairlight", ActiveWheel: "offset"}, want{"", ""}},
		{"other_page_deliver_blank", engine.Status{Page: "deliver", ActiveWheel: "lift"}, want{"", ""}},
		{"other_page_media_blank", engine.Status{Page: "media", ActiveWheel: "lift"}, want{"", ""}},
		{"empty_page_blank", engine.Status{Page: "", ActiveWheel: "lift"}, want{"", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatHUD(tc.status)
			if got.ContextLabel != tc.want.label || got.ContextValue != tc.want.value {
				t.Errorf("Context (page=%q) = (%q,%q), want (%q,%q)",
					tc.status.Page, got.ContextLabel, got.ContextValue, tc.want.label, tc.want.value)
			}
		})
	}
}

func TestFormatHUD_Clip_Passthrough(t *testing.T) {
	if got := formatHUD(engine.Status{ActiveClip: "V4 · DSCF9804.MOV"}).Clip; got != "V4 · DSCF9804.MOV" {
		t.Errorf("Clip = %q, want %q", got, "V4 · DSCF9804.MOV")
	}
	if got := formatHUD(engine.Status{ActiveClip: ""}).Clip; got != "" {
		t.Errorf("Clip = %q, want \"\"", got)
	}
}

func TestFormatHUD_DiagnosticFoot_NodeBankFineLastAction(t *testing.T) {
	status := engine.Status{
		Node:       3,
		Bank:       "edit",
		FineMode:   true,
		LastAction: "Gamma +3",
	}
	got := formatHUD(status)
	if got.Node != 3 {
		t.Errorf("Node = %d, want 3", got.Node)
	}
	if got.Bank != "edit" {
		t.Errorf("Bank = %q, want %q", got.Bank, "edit")
	}
	if !got.Fine {
		t.Error("Fine = false, want true")
	}
	if got.LastAction != "Gamma +3" {
		t.Errorf("LastAction = %q, want %q", got.LastAction, "Gamma +3")
	}
}

// NOTE: the HUD no longer displays the held-modifier chord overlay (removed
// 2026-08-13 per user request -- the #chipmod chip/data-mod attribute/
// card glow this test used to pin, added in 5b75933, are gone from
// hud.html/hud.HUDModel). engine.Status.HeldOverlay itself is UNTOUCHED --
// it is the engine's general Status surface (parallel to Layer, also
// exposed via app.go's GetStatus() to the Wails frontend) and is still
// covered by internal/engine/overlay_test.go's
// TestStatusHeldOverlay_ReflectsPressAndRelease. Only this HUD-specific
// propagation test (formatHUD no longer copies it into hud.HUDModel) was
// removed.

func TestJoinNonEmpty(t *testing.T) {
	cases := []struct {
		sep   string
		parts []string
		want  string
	}{
		{" · ", []string{"a", "b"}, "a · b"},
		{" · ", []string{"", "b"}, "b"},
		{" · ", []string{"a", ""}, "a"},
		{" · ", []string{"", ""}, ""},
		{" · ", nil, ""},
	}
	for _, tc := range cases {
		if got := joinNonEmpty(tc.sep, tc.parts...); got != tc.want {
			t.Errorf("joinNonEmpty(%q, %v) = %q, want %q", tc.sep, tc.parts, got, tc.want)
		}
	}
}

func TestTitleCase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"lift", "Lift"},
		{"color", "Color"},
		{"edit", "Edit"},
	}
	for _, tc := range cases {
		if got := titleCase(tc.in); got != tc.want {
			t.Errorf("titleCase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFormatHUD_FullModel_Smoke exercises formatHUD end-to-end for a
// realistic "Color page, mid-grade" snapshot and checks every field lands
// where expected -- a broad smoke test complementing the narrower
// per-field tests above.
func TestFormatHUD_FullModel_Smoke(t *testing.T) {
	status := engine.Status{
		Running:          true,
		SidecarConnected: true,
		Page:             "color",
		GateAllow:        true,
		ActiveWheel:      "gamma",
		Project:          "2026-06-DODEE-EP10",
		TimelineName:     "EP10-v0",
		Timecode:         "01:04:22:11",
		ActiveClip:       "V2 · shot_047",
		Node:             3,
		Bank:             "col",
		FineMode:         false,
		LastAction:       "Gamma +0.03",
	}
	got := formatHUD(status)
	want := hud.HUDModel{
		LED:         "green",
		Page:        "Color",
		GateOpen:    true,
		Timecode:    "01:04:22:11",
		TimecodeSub: "2026-06-DODEE-EP10 · EP10-v0",
		Knobs: [4]hud.HUDKnob{
			{Label: "Lift"}, {Label: "Gamma", Active: true}, {Label: "Gain"}, {Label: "Offset"},
		},
		ContextLabel: "Wheel",
		ContextValue: "Gamma",
		Clip:         "V2 · shot_047",
		Node:         3,
		Bank:         "col",
		// formatHUD now carries project and timeline as SEPARATE fields (not
		// only pre-joined in TimecodeSub) so the band can align one left and
		// one right; the fixture supplies both.
		Project:    "2026-06-DODEE-EP10",
		Timeline:   "EP10-v0",
		Fine:       false,
		LastAction: "Gamma +0.03",
	}
	if got != want {
		t.Errorf("formatHUD() = %+v, want %+v", got, want)
	}
}
