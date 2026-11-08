# AI access

AI access lets an AI tool you already use (Claude Code, Codex, Gemini CLI, Cursor and any other tool that speaks MCP) read your 2T5 DECK+ setup and change it for you: "put volume on the left dial in Resolve", "show me every control that has no assignment".

It is **off by default**. Nothing listens until you switch it on, and switching it off closes it again.

## What an AI tool can and cannot do

It can read apps, presets, layers and assignments, the action catalogue, calibration status, engine status and the undo history. It can change assignments and preset settings, and use Undo and Redo.

Every change it makes goes through the same path as a change you make by hand in the editor: the same checks (an unknown action, a value out of range or a bad control is refused with the reason), the same trust rules, the same history. A change shows in the open editor at once and is **one undo step**, so Undo (or the `undo` tool) takes it back.

It cannot arm or disarm the deck, press or turn a control, send mouse or keyboard input, change mouse calibration, import or restore backups, or add or remove apps. Those tools do not exist.

## Switch it on

1. In the editor choose the **...** menu (top right), then **AI access**.
2. Turn the switch on.
3. Copy the setup line shown in the panel and paste it into a terminal.

For Claude Code the line looks like this (the path is where your app is installed):

```
claude mcp add 2t5-deck-plus -- '/Applications/2T5 DECK+.app/Contents/MacOS/2t5-deck-plus' mcp
```

Tools that are set up by a file (Cursor, Gemini CLI, Codex and others) take the JSON block from the same panel:

```json
{ "mcpServers": { "2t5-deck-plus": { "command": "/Applications/2T5 DECK+.app/Contents/MacOS/2t5-deck-plus", "args": ["mcp"] } } }
```

The command and the server name are `2t5-deck-plus` (the executable inside the app; it was `ld-deck` before the app was renamed to 2T5 DECK+). A setup made with the old name keeps working only if it points at an executable that still exists, so run the setup line again after updating.

The app must be running while the AI tool works. If AI access is off, or the app is closed, the tool gets one clear message saying so.

## The terminal command

The same executable is a command line tool. Add it to your PATH if you like, for example `ln -s "/Applications/2T5 DECK+.app/Contents/MacOS/2t5-deck-plus" /usr/local/bin/2t5-deck-plus`.

```
2t5-deck-plus status                                   engine and deck status
2t5-deck-plus apps                                     app presets
2t5-deck-plus controls                                 control ids
2t5-deck-plus preset <app>                             banks, assignments, settings
2t5-deck-plus actions [query] [--pack <id>]            the action catalogue
2t5-deck-plus calibration [app]                        mouse-calibration status
2t5-deck-plus history                                  undo history
2t5-deck-plus assign <app> <bank> <control> '<json>'   e.g. '{"action":"global.volumeUp"}'
2t5-deck-plus clear <app> <bank> <control>
2t5-deck-plus value <app> <bank> <control> <key> <json-value>
2t5-deck-plus setting <app> <key> <json-value>
2t5-deck-plus undo | redo
2t5-deck-plus tools                                    list the tools
2t5-deck-plus call <tool> '<json arguments>'
```

Answers are JSON on stdout. Errors go to stderr; exit code 1 is a refusal (the message says why), 2 means AI access is off.

## Tools

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

## How it is protected

- The listener is on `127.0.0.1` only, so nothing outside your Mac can reach it. The port is 47860, or a free one if that is taken; the clients find it themselves.
- Every request needs a private token. It is created when you switch AI access on, stored in `~/Library/Application Support/2t5-deck-plus/ai-access.json` (readable only by you) and replaced each time you switch it on. The first start under the new name copies the old `~/Library/Application Support/ld-deck` folder across and leaves the old one untouched. Switching off removes it.
- Web pages cannot use it: requests from a browser, or for any other host name, are refused.
- The switch itself is only in the editor window. An AI tool cannot turn AI access on or off.
- Each change an AI tool makes is also written to the editor's recent activity list.

Anything running as your macOS user can read the token file, the same as any other file of yours. Switch AI access off when you are not using it.

## Names, folders and environment variables

The app is **2T5 DECK+**. Names that cannot hold a "+" use `deck-plus`:

| What | Name |
|---|---|
| Executable, terminal command, MCP server name | `2t5-deck-plus` |
| Bundle id | `com.2t5.deck-plus` |
| Built bundle / installed copy | `app/build/bin/2t5-deck-plus.app` / `/Applications/2T5 DECK+.app` |
| Per-user folder (trust store, `ai-access.json`) | `~/Library/Application Support/2t5-deck-plus` |
| Data root (config, logs, state) | `$DECKPLUS_DATA_DIR` |
| Log file | `<data root>/logs/2t5-deck-plus.log` |
| Signing certificate | "2T5 DECK+ Dev" (login keychain) |

**Old names still work where it matters.** Environment variables are now `DECKPLUS_*`; the old `LD_DECK_*` name of each is read when the new one is unset (the new one wins when both are set): `DATA_DIR`, `STATE_DIR`, `AI_ACCESS_DIR`, `ARMED`, `START_DISARMED`, `LED_OUT`, and for `scripts/build-app.sh` `INSTALL_PATH`, `NO_INSTALL`, `BUILD_STAMP`.

**Data root.** `DECKPLUS_DATA_DIR` (then `LD_DECK_DATA_DIR`) sets it and always wins. Otherwise the app uses the WS-01 root (`/Volumes/2T5-WS-01/01-APPS/2t5-deck-plus/data`) when that volume is there, and on a Mac that has no such volume and never used it, `~/Library/Application Support/2t5-deck-plus/data` (with `config/`, `logs/`, `state/` below it). The choice is sticky: the first time the WS-01 root is reachable the app records it in `~/Library/Application Support/2t5-deck-plus/data-root` (two lines, kind and path, mode 0600). While that marker says `external` (or an older build's per-user folder exists without a marker) and the volume is missing, the app refuses to start its data tree (`unreachable` error) and creates nothing, instead of seeding a second config tree on the boot disk. A data root on an external volume is never created unless the volume is mounted: nothing is created unless the root's parent folder already exists. The Python sidecar gets the resolved root from the app (`DECKPLUS_DATA_DIR` in its environment) and applies the same rules if it ever runs alone.

**First start under the new name** (logged once, in the log file): if `~/Library/Application Support/2t5-deck-plus` is missing and `.../ld-deck` exists, the old folder is COPIED across (modes kept; the old folder is never moved or deleted). Inside the data root's `state/`, `ld-deck-state.json` and `ld-deck-highlight.json` are copied to `2t5-deck-plus-state.json` and `2t5-deck-plus-highlight.json` the same way, when the new file is missing.

**Presets export.** Exports now carry `"kind": "2t5-deck-plus-presets"`; exports made earlier (`"ld-deck-presets"`) still import.
