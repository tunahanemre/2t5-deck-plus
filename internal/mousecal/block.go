package mousecal

import (
	"bytes"
	"encoding/json"
	"sort"
)

// SettingKey is the key of the calibration block in a preset's settings.
const SettingKey = "mouseCalibration"

// MaxEntries is how many contexts one target keeps; the oldest is dropped.
const MaxEntries = 12

// Verify modes of the layout check.
const (
	VerifyBlock = "block" // a failed probe refuses the click (default)
	VerifyWarn  = "warn"  // a failed probe is reported, the click still goes
	VerifyOff   = "off"
)

// TargetData is the stored calibration of one target.
type TargetData struct {
	Entries []Entry `json:"entries"`
}

// Block is the calibration of one app: the value of settings.mouseCalibration.
type Block struct {
	V int `json:"v"`
	// Verify: block (default) | warn | off.
	Verify string `json:"verify,omitempty"`
	// BlockLegacy refuses the old absolute points of calibration.json.
	BlockLegacy bool                   `json:"blockLegacy,omitempty"`
	Targets     map[string]*TargetData `json:"targets"`
	// Import records that the old absolute calibration was moved in (once), so
	// a cleared target is never silently re-imported.
	Import *ImportInfo `json:"import,omitempty"`
}

// ParseBlock reads the settings value. nil / absent gives an empty block.
func ParseBlock(v any) (Block, error) {
	b := Block{V: 1, Targets: map[string]*TargetData{}}
	if v == nil {
		return b, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return Block{V: 1, Targets: map[string]*TargetData{}}, err
	}
	if b.Targets == nil {
		b.Targets = map[string]*TargetData{}
	}
	if b.V == 0 {
		b.V = 1
	}
	return b, nil
}

// VerifyMode returns the effective verify mode.
func (b Block) VerifyMode() string {
	switch b.Verify {
	case VerifyWarn, VerifyOff:
		return b.Verify
	}
	return VerifyBlock
}

// Entries returns a copy of a target's entries.
func (b Block) Entries(key string) []Entry {
	if t := b.Targets[key]; t != nil {
		return append([]Entry(nil), t.Entries...)
	}
	return nil
}

// WithEntry adds e to the target, replacing an entry of the same context and
// dropping the oldest beyond MaxEntries.
func (b Block) WithEntry(key string, e Entry) Block {
	out := b.clone()
	t := out.Targets[key]
	if t == nil {
		t = &TargetData{}
		out.Targets[key] = t
	}
	ctx := Context{Display: e.Display, Layout: e.Layout, Win: Rect{W: e.WinW, H: e.WinH}}
	kept := t.Entries[:0:0]
	for _, o := range t.Entries {
		// a fresh capture supersedes the imported absolute point of its screen+layout
		if !o.SameContext(ctx) && !(o.Abs && o.Display == e.Display && o.Layout == e.Layout) {
			kept = append(kept, o)
		}
	}
	kept = append(kept, e)
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].At < kept[j].At })
	if len(kept) > MaxEntries {
		kept = kept[len(kept)-MaxEntries:]
	}
	t.Entries = kept
	return out
}

// Without removes one entry (id) or, with id "", every entry of the target.
func (b Block) Without(key, id string) Block {
	out := b.clone()
	t := out.Targets[key]
	if t == nil {
		return out
	}
	if id == "" {
		delete(out.Targets, key)
		return out
	}
	kept := t.Entries[:0:0]
	for _, o := range t.Entries {
		if o.ID != id {
			kept = append(kept, o)
		}
	}
	if len(kept) == 0 {
		delete(out.Targets, key)
	} else {
		t.Entries = kept
	}
	return out
}

func (b Block) clone() Block {
	out := Block{V: 1, Verify: b.Verify, BlockLegacy: b.BlockLegacy, Import: b.Import, Targets: map[string]*TargetData{}}
	for k, t := range b.Targets {
		out.Targets[k] = &TargetData{Entries: append([]Entry(nil), t.Entries...)}
	}
	return out
}

// Value is the block as a settings value (generic JSON, numbers as json.Number
// so the preset file stays byte-stable).
func (b Block) Value() any {
	raw, _ := json.Marshal(b)
	var out map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	_ = d.Decode(&out)
	return out
}
