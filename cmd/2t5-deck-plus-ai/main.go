// Command 2t5-deck-plus-ai is the AI-access terminal command and MCP server on its own
// (the packaged app answers the same commands when started as `2t5-deck-plus mcp`, `2t5-deck-plus apps` ...).
// It is kept as a separate build for machines or CI without the app bundle: it shares every line
// with the app's own `mcp` / terminal entry (internal/aiaccess), so there is nothing to fold in.
// It talks only to the running app's local AI door; it opens no MIDI port.
package main

import (
	"os"

	"2t5-deck-plus/internal/aiaccess"
)

func main() { os.Exit(aiaccess.Main()) }
