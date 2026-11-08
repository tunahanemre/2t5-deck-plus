package aiaccess

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Commands are the first words the terminal command handles; anything else is left to the caller (the app starts normally).
var Commands = map[string]bool{"mcp": true, "tools": true, "call": true, "status": true, "apps": true, "controls": true, "preset": true,
	"actions": true, "calibration": true, "history": true, "assign": true, "clear": true, "value": true, "setting": true, "undo": true, "redo": true, "ai-help": true}

const cliHelp = `2t5-deck-plus: read and edit your 2T5 DECK+ setup from the terminal (2T5 DECK+ must be running with AI access switched on).

  2t5-deck-plus mcp                                  run as an MCP server on stdio (what AI tools launch)
  2t5-deck-plus status                               engine / deck status
  2t5-deck-plus apps                                 app presets
  2t5-deck-plus controls                             the deck's control ids
  2t5-deck-plus preset <app>                         one app's banks, assignments, settings
  2t5-deck-plus actions [query] [--pack <id>]        the action catalogue
  2t5-deck-plus calibration [app]                    mouse-calibration status
  2t5-deck-plus history                              undo history
  2t5-deck-plus assign <app> <bank> <control> '<json>'   assign an action, e.g. '{"action":"..."}'
  2t5-deck-plus clear <app> <bank> <control>         remove an assignment
  2t5-deck-plus value <app> <bank> <control> <key> <json-value>
  2t5-deck-plus setting <app> <key> <json-value>
  2t5-deck-plus undo | redo
  2t5-deck-plus tools                                list the tools
  2t5-deck-plus call <tool> '<json arguments>'       any tool by name

Answers are JSON on stdout; errors go to stderr (exit 1; exit 2 when AI access is off).
`

// RunCLI runs a terminal command. It returns the process exit code.
func RunCLI(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(argv) == 0 || argv[0] == "ai-help" || argv[0] == "help" || argv[0] == "--help" {
		fmt.Fprint(stdout, cliHelp)
		return 0
	}
	c := &Client{}
	if argv[0] == "mcp" {
		if err := ServeMCP(c, stdin, stdout); err != nil {
			fmt.Fprintln(stderr, "2t5-deck-plus mcp:", err)
			return 1
		}
		return 0
	}
	if argv[0] == "tools" {
		ts, err := c.Tools()
		if errors.Is(err, ErrOff) { // the list is compiled in: show it anyway
			ts, err = Tools(), nil
		}
		if err != nil {
			return fail(stderr, err)
		}
		for _, t := range ts {
			mode := "edit"
			if t.ReadOnly {
				mode = "read"
			}
			fmt.Fprintf(stdout, "%-24s %s  %s\n", t.Name, mode, firstSentence(t.Description))
		}
		return 0
	}
	tool, a, err := cliArgs(argv)
	if err != nil {
		fmt.Fprintln(stderr, "2t5-deck-plus:", err)
		return 64
	}
	raw, err := c.Call(tool, a)
	if err != nil {
		return fail(stderr, err)
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "", "  ") != nil {
		pretty.Write(raw)
	}
	fmt.Fprintln(stdout, strings.TrimSpace(pretty.String()))
	return 0
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "2t5-deck-plus:", err)
	if errors.Is(err, ErrOff) {
		return 2
	}
	return 1
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

func jv(s string) (any, error) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return s, nil // not JSON: a plain string value
	}
	return v, nil
}

func cliArgs(argv []string) (string, map[string]any, error) {
	cmd, rest := argv[0], argv[1:]
	need := func(n int, usage string) error {
		if len(rest) < n {
			return fmt.Errorf("usage: 2t5-deck-plus %s", usage)
		}
		return nil
	}
	switch cmd {
	case "status", "apps", "controls", "history", "undo", "redo":
		name := map[string]string{"status": "status", "apps": "list_apps", "controls": "list_controls", "history": "get_history", "undo": "undo", "redo": "redo"}[cmd]
		return name, map[string]any{}, nil
	case "preset":
		if err := need(1, "preset <app>"); err != nil {
			return "", nil, err
		}
		return "get_preset", map[string]any{"app": rest[0]}, nil
	case "calibration":
		a := map[string]any{}
		if len(rest) > 0 {
			a["app"] = rest[0]
		}
		return "get_calibration_status", a, nil
	case "actions":
		a := map[string]any{}
		for i := 0; i < len(rest); i++ {
			if rest[i] == "--pack" && i+1 < len(rest) {
				a["pack"] = rest[i+1]
				i++
			} else {
				a["query"] = rest[i]
			}
		}
		return "list_actions", a, nil
	case "assign":
		if err := need(4, "assign <app> <bank> <control> '<json>'"); err != nil {
			return "", nil, err
		}
		v, _ := jv(rest[3])
		return "set_assignment", map[string]any{"app": rest[0], "bank": rest[1], "control": rest[2], "assignment": v}, nil
	case "clear":
		if err := need(3, "clear <app> <bank> <control>"); err != nil {
			return "", nil, err
		}
		return "clear_assignment", map[string]any{"app": rest[0], "bank": rest[1], "control": rest[2]}, nil
	case "value":
		if err := need(5, "value <app> <bank> <control> <key> <json-value>"); err != nil {
			return "", nil, err
		}
		v, _ := jv(rest[4])
		return "set_value", map[string]any{"app": rest[0], "bank": rest[1], "control": rest[2], "key": rest[3], "value": v}, nil
	case "setting":
		if err := need(3, "setting <app> <key> <json-value>"); err != nil {
			return "", nil, err
		}
		v, _ := jv(rest[2])
		return "set_setting", map[string]any{"app": rest[0], "key": rest[1], "value": v}, nil
	case "call":
		if err := need(1, "call <tool> '<json arguments>'"); err != nil {
			return "", nil, err
		}
		a := map[string]any{}
		if len(rest) > 1 {
			d := json.NewDecoder(strings.NewReader(rest[1]))
			d.UseNumber()
			if err := d.Decode(&a); err != nil {
				return "", nil, fmt.Errorf("arguments must be a JSON object: %v", err)
			}
		}
		return rest[0], a, nil
	}
	return "", nil, fmt.Errorf("unknown command %q (try: 2t5-deck-plus ai-help)", cmd)
}

// Main is the entry point of the terminal command (exit code returned).
func Main() int { return RunCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr) }
