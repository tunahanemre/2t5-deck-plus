// Package aiaccess is the "AI access" channel of 2T5 DECK+: one small local API
// (loopback only, token-guarded, off by default) that lets a user's AI tool
// read and edit the deck setup, plus the two thin clients of it (an MCP server
// over stdio and a terminal command).
//
// Every tool is a call into the editor's own HTTP handler, in process, exactly
// as a hand edit from the editor arrives (same validation, same trust, same
// history: one undo step per change). Nothing here writes a preset file, and
// nothing here can arm or disarm the deck, press or turn a control, or send
// mouse or keyboard input: those routes are simply not in the table below.
package aiaccess

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Tool is one thing an AI tool may do.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ReadOnly    bool           `json:"readOnly"`
	InputSchema map[string]any `json:"inputSchema"`

	// build turns the arguments into the editor request.
	build func(a args) (method, path string, body any, err error)
	// post filters the editor's JSON answer (nil = as is).
	post func(a args, raw []byte) ([]byte, error)
}

type args map[string]any

func (a args) str(k string) string {
	switch v := a[k].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}

func (a args) need(keys ...string) error {
	for _, k := range keys {
		if a.str(k) == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	return nil
}

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	pApp     = prop("string", "App id: a bundle id such as com.apple.finder, or \"global\" for the Global preset. Use list_apps to see them.")
	pBank    = prop("string", "Bank (layer) id, as get_preset shows it under preset.banks, e.g. \"base\" or \"layer2\".")
	pControl = prop("string", "Control id, e.g. \"Knob1\", \"Dial3\", \"Button5\". Use list_controls.")
)

func preset(id string) string { return "/api/preset/" + url.PathEscape(id) }

// Tools is the whole AI door, in the order clients list it.
func Tools() []Tool {
	return []Tool{
		{Name: "status", ReadOnly: true,
			Description: "Engine and deck status: whether the engine runs, whether the deck is armed, the frontmost app and the preset that is active for it, sidecar state, the engine's applied-change state and macOS permission readiness. Read only.",
			InputSchema: schema(map[string]any{}),
			build:       func(a args) (string, string, any, error) { return "GET", "/api/live", nil, nil }},
		{Name: "list_apps", ReadOnly: true,
			Description: "Every app preset: id, name, action packs, how many controls are assigned, whether it is frontmost or installed. Read only.",
			InputSchema: schema(map[string]any{}),
			build:       func(a args) (string, string, any, error) { return "GET", "/api/apps", nil, nil }},
		{Name: "list_controls", ReadOnly: true,
			Description: "The deck's controls (knobs, dials, sliders, buttons) with the ids the other tools take. Read only.",
			InputSchema: schema(map[string]any{}),
			build:       func(a args) (string, string, any, error) { return "GET", "/api/deck", nil, nil },
			post:        controlsOnly},
		{Name: "get_preset", ReadOnly: true,
			Description: "One app's full preset: its banks (layers and modifier banks), every assignment (action id plus values), settings, and the effective result per control including what is inherited from Global. Read only.",
			InputSchema: schema(map[string]any{"app": pApp}, "app"),
			build: func(a args) (string, string, any, error) {
				return "GET", preset(a.str("app")), nil, a.need("app")
			}},
		{Name: "list_actions", ReadOnly: true,
			Description: "The action catalogue: every action id with its label, the gesture it suits (press, turn, both), its fields (name, type, range, default) and the app settings. Filter with pack or query to keep the answer short. Read only.",
			InputSchema: schema(map[string]any{
				"pack":  prop("string", "Only this action pack id."),
				"query": prop("string", "Only actions whose id, label or help text contains this text (case-insensitive)."),
			}),
			build: func(a args) (string, string, any, error) { return "GET", "/api/registry", nil, nil },
			post:  filterRegistry},
		{Name: "get_calibration_status", ReadOnly: true,
			Description: "Mouse-calibration status: without app, the apps that have mouse targets with how many are calibrated; with app, that app's targets and their state. Read only; calibration itself cannot be changed from here.",
			InputSchema: schema(map[string]any{"app": prop("string", "App id (optional).")}),
			build: func(a args) (string, string, any, error) {
				if a.str("app") != "" {
					return "GET", "/api/calibration/" + url.PathEscape(a.str("app")), nil, nil
				}
				return "GET", "/api/calibration", nil, nil
			}},
		{Name: "get_history", ReadOnly: true,
			Description: "The undo history: each step's label and the current position. Read only.",
			InputSchema: schema(map[string]any{}),
			build:       func(a args) (string, string, any, error) { return "GET", "/api/history", nil, nil }},

		{Name: "set_assignment",
			Description: "Assign an action to a control in an app's bank. assignment is the object the editor writes, e.g. {\"action\":\"<action id from list_actions>\", ...field values}. It is validated like a hand edit (an unknown action or a value out of range is refused with the reason; actions that run a program (generic.shell, type shell) are allowed), shows in the open editor at once and is one undo step. The deck's arm state is not touched.",
			InputSchema: schema(map[string]any{"app": pApp, "bank": pBank, "control": pControl,
				"assignment": map[string]any{"type": "object", "description": "The assignment: an action id and its field values, as get_preset shows existing ones."}}, "app", "bank", "control", "assignment"),
			build: func(a args) (string, string, any, error) {
				if err := a.need("app", "bank", "control"); err != nil {
					return "", "", nil, err
				}
				as, ok := a["assignment"].(map[string]any)
				if !ok {
					return "", "", nil, fmt.Errorf("assignment must be an object")
				}
				return "PUT", preset(a.str("app")) + "/assign", map[string]any{"bank": a.str("bank"), "control": a.str("control"), "assignment": as}, nil
			}},
		{Name: "clear_assignment",
			Description: "Remove a control's assignment in an app's bank (the app then inherits Global for it, or does nothing). One undo step.",
			InputSchema: schema(map[string]any{"app": pApp, "bank": pBank, "control": pControl}, "app", "bank", "control"),
			build: func(a args) (string, string, any, error) {
				if err := a.need("app", "bank", "control"); err != nil {
					return "", "", nil, err
				}
				return "DELETE", preset(a.str("app")) + "/assign?bank=" + url.QueryEscape(a.str("bank")) + "&control=" + url.QueryEscape(a.str("control")), nil, nil
			}},
		{Name: "set_value",
			Description: "Change one field of an existing assignment (for example a sensitivity or a step size) without rewriting the rest. key is the field name from the action's fields in list_actions. Validated like a hand edit; one undo step.",
			InputSchema: schema(map[string]any{"app": pApp, "bank": pBank, "control": pControl,
				"key": prop("string", "Field name."), "value": map[string]any{"description": "New value (number, string, boolean)."}}, "app", "bank", "control", "key", "value"),
			build: func(a args) (string, string, any, error) {
				if err := a.need("app", "bank", "control", "key"); err != nil {
					return "", "", nil, err
				}
				v, ok := a["value"]
				if !ok {
					return "", "", nil, fmt.Errorf("value is required")
				}
				return "PUT", preset(a.str("app")) + "/value", map[string]any{"bank": a.str("bank"), "control": a.str("control"), "key": a.str("key"), "value": v}, nil
			}},
		{Name: "set_setting",
			Description: "Change a preset-level setting of an app (the settings list_actions reports, e.g. sensitivity or velocity options). Validated like a hand edit; one undo step.",
			InputSchema: schema(map[string]any{"app": pApp, "key": prop("string", "Setting key."), "value": map[string]any{"description": "New value."}}, "app", "key", "value"),
			build: func(a args) (string, string, any, error) {
				if err := a.need("app", "key"); err != nil {
					return "", "", nil, err
				}
				v, ok := a["value"]
				if !ok {
					return "", "", nil, fmt.Errorf("value is required")
				}
				return "PUT", preset(a.str("app")) + "/settings", map[string]any{"key": a.str("key"), "value": v}, nil
			}},
		{Name: "undo",
			Description: "Undo the last change (the same undo the editor's Undo button does), whoever made it. Returns the label of what was undone.",
			InputSchema: schema(map[string]any{}),
			build:       func(a args) (string, string, any, error) { return "POST", "/api/undo", nil, nil }},
		{Name: "redo",
			Description: "Redo the change that was just undone.",
			InputSchema: schema(map[string]any{}),
			build:       func(a args) (string, string, any, error) { return "POST", "/api/redo", nil, nil }},
	}
}

// Find returns a tool by name.
func Find(name string) (Tool, bool) {
	for _, t := range Tools() {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Counts is how many tools exist and how many of them are read only.
func Counts() (all, readOnly int) {
	for _, t := range Tools() {
		all++
		if t.ReadOnly {
			readOnly++
		}
	}
	return
}

// controlsOnly keeps list_controls short: id, label and kind of each control.
func controlsOnly(_ args, raw []byte) ([]byte, error) {
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		return raw, nil
	}
	ctl, _ := d["controls"].([]any)
	if ctl == nil {
		return raw, nil
	}
	out := []map[string]any{}
	for _, c := range ctl {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		row := map[string]any{}
		for _, k := range []string{"id", "label", "kind", "group", "name", "type"} {
			if v, ok := m[k]; ok {
				row[k] = v
			}
		}
		out = append(out, row)
	}
	return json.Marshal(map[string]any{"controls": out})
}

// filterRegistry applies list_actions' pack and query filters.
func filterRegistry(a args, raw []byte) ([]byte, error) {
	pack, q := a.str("pack"), strings.ToLower(a.str("query"))
	if pack == "" && q == "" {
		return raw, nil
	}
	var reg struct {
		Packs    []map[string]any `json:"packs"`
		Settings any              `json:"settings"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		return raw, nil
	}
	out := []map[string]any{}
	for _, p := range reg.Packs {
		if pack != "" && p["id"] != pack {
			continue
		}
		acts, _ := p["actions"].([]any)
		keep := []any{}
		for _, x := range acts {
			m, _ := x.(map[string]any)
			hay := strings.ToLower(fmt.Sprint(m["id"], " ", m["label"], " ", m["help"]))
			if q == "" || strings.Contains(hay, q) {
				keep = append(keep, x)
			}
		}
		if len(keep) == 0 {
			continue
		}
		cp := map[string]any{}
		for k, v := range p {
			cp[k] = v
		}
		cp["actions"] = keep
		out = append(out, cp)
	}
	return json.Marshal(map[string]any{"packs": out, "settings": reg.Settings})
}

// toolNames is for error messages.
func toolNames() string {
	var n []string
	for _, t := range Tools() {
		n = append(n, t.Name)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}
