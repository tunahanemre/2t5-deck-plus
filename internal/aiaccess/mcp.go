package aiaccess

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Version is reported to MCP clients; it follows the app version (app/version.go, checked by app/version_test.go).
const Version = "0.1.2"

var mcpVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// ServeMCP speaks MCP (JSON-RPC 2.0, one message per line) over in/out until in closes.
// Minimal on purpose: initialize, ping, tools/list, tools/call; every tool is a call to the AI door through c.
func ServeMCP(c *Client, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	enc := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
	}
	reply := func(id json.RawMessage, result any) {
		enc(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	fail := func(id json.RawMessage, code int, msg string) {
		enc(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	}
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r rpcReq
		if err := json.Unmarshal(line, &r); err != nil {
			fail(json.RawMessage("null"), -32700, "parse error")
			continue
		}
		isNote := len(r.ID) == 0
		switch r.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(r.Params, &p)
			v := mcpVersions[0]
			for _, s := range mcpVersions {
				if s == p.ProtocolVersion {
					v = s
				}
			}
			reply(r.ID, map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":   map[string]any{"name": "2t5-deck-plus", "version": Version},
				"instructions": "Reads and edits the 2T5 DECK+ (Loupedeck) setup. Edits go through the editor: they show there at once and each is one undo step (use the undo tool to take one back). The tools cannot arm the deck or press, turn or send input. AI access must be switched on in the 2T5 DECK+ app."})
		case "ping":
			reply(r.ID, map[string]any{})
		case "tools/list":
			// the compiled-in list: the handshake works even while the app's door is closed
			ts := Tools()
			reply(r.ID, map[string]any{"tools": ts})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(r.Params, &p); err != nil || p.Name == "" {
				fail(r.ID, -32602, "tools/call needs a name")
				continue
			}
			if _, ok := Find(p.Name); !ok {
				fail(r.ID, -32602, "unknown tool "+p.Name)
				continue
			}
			raw, err := c.Call(p.Name, p.Arguments)
			if err != nil {
				reply(r.ID, map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": err.Error()}}})
				continue
			}
			var pretty bytes.Buffer
			if json.Indent(&pretty, raw, "", "  ") != nil {
				pretty.Reset()
				pretty.Write(raw)
			}
			reply(r.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": pretty.String()}}})
		default:
			if !isNote {
				fail(r.ID, -32601, "method not found: "+r.Method)
			}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
