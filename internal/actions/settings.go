package actions

// Preset-level settings the editor may change (wave D). They live in a
// preset's "settings" object; only the keys listed here are validated, other
// keys keep passing through verbatim.

const resolveBundleID = "com.blackmagic-design.DaVinciResolve"

// SettingField is one editable preset-level setting.
type SettingField struct {
	Field
	// Presets lists the preset ids the setting applies to (empty = all).
	Presets []string `json:"presets,omitempty"`
}

var settingFields = []SettingField{
	{Presets: []string{resolveBundleID}, Field: Field{
		Key: "fineFactor", Label: "Fine-mode multiplier", Unit: "× while fine mode is held", Kind: "number",
		Default: 0.5, Min: f64(0.05), Max: f64(1), Step: f64(0.05),
		Help:    "How much the sensitivity of the colour knobs is multiplied while the fine-mode button is held. 0.5 = half speed, 0.1 = ten times finer. Takes effect at once.",
		Primary: true}},
	{Presets: []string{resolveBundleID}, Field: Field{
		Key: "hudEnabled", Label: "Show the HUD", Kind: "bool",
		Default: true,
		Help:    "The HUD on/off. Off hides it everywhere, with or without Resolve, and it stays off after a restart. Takes effect at once."}},
	{Presets: []string{resolveBundleID}, Field: Field{
		Key: "hudWithoutResolve", Label: "Show the HUD without Resolve", Kind: "bool",
		Default: true,
		Help:    "When on, the HUD can show while DaVinci Resolve is not running. When off, it is hidden whenever Resolve is closed and comes back with it. Takes effect at once."}},
}

// Settings lists the editable preset-level settings.
func Settings() []SettingField { return append([]SettingField(nil), settingFields...) }

// SettingFor returns the schema of a setting key.
func SettingFor(key string) (SettingField, bool) {
	for _, s := range settingFields {
		if s.Key == key {
			return s, true
		}
	}
	return SettingField{}, false
}

// AppliesTo reports whether the setting belongs to preset id.
func (s SettingField) AppliesTo(id string) bool {
	if len(s.Presets) == 0 {
		return true
	}
	for _, p := range s.Presets {
		if p == id {
			return true
		}
	}
	return false
}
