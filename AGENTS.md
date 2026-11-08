# AGENTS.md: operating 2T5 DECK+ from an AI agent

You are reading the manual for AI agents (Claude Code, Codex, Gemini CLI, Cursor and anything else that speaks MCP or can run a shell). **2T5 DECK+** is a Mac app that turns a Loupedeck+ into one deck for every app. A person owns the deck and the setup. You help them read it and change it, through the same checks and the same undo history they use by hand.

Read this file fully before you touch anything. The person's plain-language guide is [README.md](README.md); the long reference for AI access is [docs/ai-access.md](docs/ai-access.md).

What the app needs from a Mac (runtimes, permissions, Resolve settings, hardware, other apps that must be closed) is one data file, [docs/dependencies.json](docs/dependencies.json). Read it instead of guessing, and change it there only: the editor's Requirements panel, the README table and the website all load from it.

## The ground rules

1. **AI access is off by default and only the person can switch it on.** You cannot turn it on or off. If it is off, say so and tell them the steps below. Do not look for a way around it.
2. **The app must be running.** If it is closed or AI access is off, every call returns one clear message saying so (CLI exit code 2).
3. **Everything you change is one undo step** and shows live in their open editor. Say what you changed, and that Undo (or your `undo` tool) takes it back.
4. **You cannot arm or disarm the deck, press or turn a control, send keyboard or mouse input, change mouse calibration, import or restore backups, or add or remove apps.** Those tools do not exist. Never tell the person you did any of these, and never try to reach them another way (editing files in the app's folders, scripting the UI, killing the app). If the person wants one of those, tell them where to do it in the app.
5. **Never edit the app's files directly** (presets, trust store, `ai-access.json`). Go through the tools, so the checks and history apply.
6. **Never print or store the token** in `ai-access.json`. You do not need it: the MCP server and the CLI find it themselves.
7. **Assignments can run programs.** An action that runs a shell command only runs when the person presses that control while the deck is armed, but treat it as the riskiest thing you can assign. Prefer built-in actions; if you must assign a command, show the person the exact command first and wait for a yes. Actions carry risk marks in `list_actions`; read them.
8. **Read before you write.** Call `get_preset` for the app first, change the minimum, then read it back to confirm.

## Switch AI access on (the person does this)

1. In the editor choose the **AI Agent** button in the top bar (or the **...** menu, then **AI access**).
2. Turn the switch on.
3. Copy the setup line from the panel and paste it into a terminal.

For Claude Code the line is:

```
claude mcp add 2t5-deck-plus -- '/Applications/2T5 DECK+.app/Contents/MacOS/2t5-deck-plus' mcp
```

For tools that use a settings file (Cursor, Gemini CLI, Codex and others), the panel shows this block (the path is where the app is installed):

```json
{ "mcpServers": { "2t5-deck-plus": { "command": "/Applications/2T5 DECK+.app/Contents/MacOS/2t5-deck-plus", "args": ["mcp"] } } }
```

The server name and the command are both `2t5-deck-plus`. After the app is updated or moved, run the setup line again.

## The tools (MCP)

| Tool | Does | Changes anything |
|---|---|---|
| `status` | Engine running, armed state, frontmost app, active preset, sidecar, permissions | no |
| `list_apps` | App presets with packs and assigned counts | no |
| `list_controls` | The deck's control ids | no |
| `get_preset` | One app's banks, assignments, settings and the effective result per control | no |
| `list_actions` | Action catalogue (filter with `pack`, `query`) | no |
| `get_calibration_status` | Mouse-calibration state, all apps or one | no |
| `get_history` | Undo history | no |
| `set_assignment` | Assign an action to a control in a bank | yes, one undo step |
| `clear_assignment` | Remove an assignment | yes, one undo step |
| `set_value` | Change one field of an assignment | yes, one undo step |
| `set_setting` | Change a preset setting | yes, one undo step |
| `undo`, `redo` | Step the history | yes |

A refused change (unknown action, value out of range, bad control id) comes back with the reason. Read it, fix the call, do not retry blindly.

## The terminal command

The same executable is a command line tool. If you have no MCP connection, use it. Put the path in a variable, since it contains a space and a plus sign:

```
DECK="/Applications/2T5 DECK+.app/Contents/MacOS/2t5-deck-plus"

"$DECK" status                                   # engine and deck status
"$DECK" apps                                     # app presets
"$DECK" controls                                 # control ids
"$DECK" preset <app>                             # banks, assignments, settings
"$DECK" actions [query] [--pack <id>]            # the action catalogue
"$DECK" calibration [app]                        # mouse-calibration status
"$DECK" history                                  # undo history
"$DECK" assign <app> <bank> <control> '<json>'   # e.g. '{"action":"global.volumeUp"}'
"$DECK" clear <app> <bank> <control>
"$DECK" value <app> <bank> <control> <key> <json-value>
"$DECK" setting <app> <key> <json-value>
"$DECK" undo | redo
"$DECK" tools                                    # list the tools
"$DECK" call <tool> '<json arguments>'
```

Answers are JSON on stdout. Errors go to stderr. **Exit code 1 is a refusal** (the message says why); **exit code 2 means AI access is off or the app is not running.**

## How to work well

**Start with orientation.** Call `status`, `list_apps`, then `get_preset` for the app the person means. `status` tells you whether the deck is armed, which app is in front, and whether the permissions the features need are granted.

**Understand the model before you assign.**
- A deck setup is a set of **app presets** plus a **Global** preset. An app preset overrides Global control by control; anything unset falls back to Global.
- Controls live in **banks** (for example the layer a person is on, or a modifier held). `get_preset` shows the banks and the effective result per control, including where each value comes from.
- Every assignment is an **action** plus values. Find actions with `list_actions` (use `query` and `pack`); never invent an action id.

**Typical jobs.**
- *"Put volume on the left dial in Resolve."* `get_preset` for Resolve, find the dial's control id with `list_controls`, `list_actions` with `query: "volume"`, `set_assignment`, then `get_preset` again and report what the dial now does.
- *"Show me every control that has no assignment."* `get_preset` and list the unassigned controls, per bank. Change nothing.
- *"Make the knob feel slower."* `set_value` on that assignment (sensitivity and similar fields; `get_preset` shows which exist). Say the old and new value.
- *"Undo that."* `undo`, then confirm with `get_history`.

**When something is wrong.** `status` first. Common answers: AI access off (person switches it on), app not running (person opens it), no Accessibility permission (nothing fires; person grants it in System Settings), deck disarmed (nothing fires by design; person arms it). Report what you found; do not try to fix permissions or arming yourself.

**Report like a colleague.** After a change, say which app, which bank and control, what it did before and what it does now, and that Undo reverts it.

## How AI access is protected

- The listener is on `127.0.0.1` only, so nothing outside the Mac can reach it. The port is 47860, or a free one if that is taken; clients find it themselves.
- Every request needs a private token, created when AI access is switched on, stored in `~/Library/Application Support/2t5-deck-plus/ai-access.json` (readable only by the person) and replaced each time it is switched on. Switching off removes it.
- Web pages cannot use it: requests from a browser, or for any other host name, are refused.
- The switch itself is only in the editor window.
- Each change an AI tool makes is also written to the editor's recent activity list.

Anything running as the person's macOS user can read the token file, like any other file of theirs. Do not read it.

## If you are working on this repository (not operating the app)

- The app is Go + Wails with a vanilla JS editor in `app/frontend/public/editor`. The editor dev server (`go run ./cmd/deck-editor-dev --static app/frontend/public/editor --sim`, then `http://127.0.0.1:2541/`) opens no MIDI port and arms nothing: use it for any UI work.
- **Do not run anything that opens the real MIDI port or posts real input while a person's deck is in use.** `DECKPLUS_ARMED=1` must never be set for `go test`: the key, mouse and Finder test packages fail on purpose if it is, because real keystrokes would reach the frontmost app. Never kill the running app or its sidecar with `pkill`/`killall`: quit it from its menu.
- Build with `scripts/build-app.sh` (see the README). `DECKPLUS_NO_INSTALL=1` builds without touching the installed app.
- Environment variables use the `DECKPLUS_` prefix (`DECKPLUS_DATA_DIR`, `DECKPLUS_START_DISARMED`, ...). The old `LD_DECK_` names of each are still read when the new one is unset; keep that, it is deliberate.
- Names that cannot hold a "+" use `deck-plus`: executable and MCP server `2t5-deck-plus`, bundle id `com.2t5.deck-plus`, per-user folder `~/Library/Application Support/2t5-deck-plus`.
