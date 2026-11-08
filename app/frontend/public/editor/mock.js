// mock.js — in-page fake of the editor API (fixtures + fake event generator). Dev/demo only.
import { ApiError } from "./api.js";

const P = new URLSearchParams(location.search);
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const clone = (o) => JSON.parse(JSON.stringify(o));

/* ---------------- registry ---------------- */
const F = (key, label, kind, extra) => ({ key, label, kind, ...extra });
const KEY = (key, label) => F(key, label, "key", { help: "Press the chord, e.g. Cmd+Alt+C." });
const LABEL = F("label", "Label", "string", { help: "Shown on the HUD and in this editor." });
const REGISTRY = { packs: [
  { id: "generic", label: "Generic", actions: [
    { id: "generic.key", type: "key", label: "Press a key", fields: [KEY("key", "Key"), LABEL] },
    { id: "generic.keyDelta", type: "keyDelta", label: "Turn sends two keys", fields: [
      KEY("keyCW", "Clockwise key"), KEY("keyCCW", "Counter-clockwise key"),
      F("detentThreshold", "Ticks per step", "number", { unit: "ticks", min: 1, max: 40, step: 1, default: 2, sensitivity: true, help: "Raw encoder ticks before one key press." }), LABEL] },
    { id: "generic.scrollDelta", type: "scrollDelta", label: "Turn scrolls", fields: [
      F("linesPerDetent", "Scroll amount", "number", { unit: "lines / detent", min: 0.5, max: 40, step: 0.5, default: 3, sensitivity: true, help: "Lines scrolled for each detent." }),
      F("detentThreshold", "Ticks per detent", "number", { unit: "ticks", min: 1, max: 20, step: 1, default: 1, sensitivity: true, help: "Raw ticks needed for one detent." }),
      F("gamma", "Acceleration", "number", { unit: "x", min: 1, max: 3, step: 0.1, default: 1.4, sensitivity: true, help: "1 = linear. Higher speeds up fast turns." }),
      F("invert", "Invert direction", "bool", { default: false }), LABEL] },
    { id: "generic.shell", type: "shell", label: "Run a command", fields: [F("command", "Command", "string", { help: "Executed with /bin/sh." }), LABEL] },
    { id: "generic.layer", type: "layer", label: "Switch layer", fields: [F("layer", "Layer", "number", { min: 1, max: 4, step: 1, default: 2 }), LABEL] },
    { id: "generic.none", type: "none", label: "Disabled", fields: [] },
  ]},
  { id: "browser", label: "Browser", actions: [
    { id: "browser.scroll", type: "scrollDelta", label: "Scroll page", fields: [
      F("linesPerDetent", "Scroll amount", "number", { unit: "lines / detent", min: 0.5, max: 40, step: 0.5, default: 3, sensitivity: true, help: "Lines scrolled for each detent." }),
      F("detentThreshold", "Ticks per detent", "number", { unit: "ticks", min: 1, max: 20, step: 1, default: 1, sensitivity: true }),
      F("gamma", "Acceleration", "number", { unit: "x", min: 1, max: 3, step: 0.1, default: 1.4, sensitivity: true }), LABEL] },
    { id: "browser.tab", type: "keyDelta", label: "Switch tab", fields: [
      KEY("keyCW", "Next tab"), KEY("keyCCW", "Previous tab"),
      F("detentThreshold", "Ticks per tab", "number", { unit: "ticks", min: 1, max: 20, step: 1, default: 3, sensitivity: true }), LABEL] },
    { id: "browser.zoom", type: "keyDelta", label: "Zoom page", fields: [KEY("keyCW", "Zoom in"), KEY("keyCCW", "Zoom out"), F("detentThreshold", "Ticks per step", "number", { unit: "ticks", min: 1, max: 20, step: 1, default: 2, sensitivity: true }), LABEL] },
    { id: "browser.back", type: "key", label: "Back / Forward", fields: [KEY("key", "Key"), LABEL] },
  ]},
  { id: "finder", label: "Finder", actions: [
    { id: "finder.tag", type: "finderTag", label: "Colour-tag selection", fields: [F("tag", "Tag colour", "enum", { options: ["Red", "Orange", "Yellow", "Green", "Blue", "Purple", "Gray"], default: "Red" }), LABEL] },
  ]},
  { id: "resolve", label: "DaVinci Resolve", actions: [
    { id: "resolve.jog", type: "jog", label: "Scrub in frames", fields: [
      F("base", "Frames per tick", "number", { unit: "frames", min: 0.1, max: 30, step: 0.1, default: 1, sensitivity: true, help: "Playhead frames moved per slow tick." }),
      F("gamma", "Acceleration curve", "number", { unit: "x", min: 1, max: 4, step: 0.1, default: 1.6, sensitivity: true }),
      F("maxDelta", "Max jump", "number", { unit: "frames", min: 1, max: 5000, step: 1, default: 600, sensitivity: true }), LABEL] },
    { id: "resolve.jogSeconds", type: "jogSeconds", label: "Scrub in seconds", fields: [
      F("secondsPerStep", "Seconds per step", "number", { unit: "seconds", min: 0.1, max: 600, step: 0.5, default: 1, sensitivity: true, help: "Timeline distance per detent." }),
      F("detentThreshold", "Ticks per step", "number", { unit: "ticks", min: 1, max: 20, step: 1, default: 1, sensitivity: true }), LABEL] },
    { id: "resolve.property", type: "property", label: "Clip property", fields: [
      F("property", "Property", "string", { help: "Resolve TimelineItem property name, e.g. Pan." }),
      F("gang", "Gang of properties", "stringList", { help: "Several properties bumped together, e.g. ZoomX and ZoomY. Overrides Property." }),
      F("sens", "Sensitivity", "number", { unit: "units / tick", min: 0.01, max: 5, step: 0.01, default: 0.25, sensitivity: true }),
      F("gamma", "Acceleration", "number", { unit: "x", min: 1, max: 3, step: 0.1, default: 1.3, sensitivity: true }), LABEL] },
    { id: "resolve.marker", type: "marker", label: "Add marker", fields: [
      F("markerColor", "Colour", "enum", { options: ["Blue", "Cyan", "Green", "Yellow", "Red", "Pink"], default: "Blue" }),
      F("markerDurationSeconds", "Duration", "number", { unit: "seconds", min: 0, max: 600, step: 1, default: 0 }), LABEL] },
  ]},
]};
const ACTION = {};
for (const pk of REGISTRY.packs) for (const a of pk.actions) ACTION[a.id] = { ...a, pack: pk.id };
const byType = (t) => Object.values(ACTION).find((a) => a.type === t);

/* ---------------- fixtures ---------------- */
const RES = "com.blackmagic-design.DaVinciResolve", FIND = "com.apple.finder", CHR = "com.google.Chrome";
const presets = {};
const mk = (id, name, packs, banks, settings) => (presets[id] = { schema: 1, id, name, packs, banks, settings: settings || {} });
const knobs = ["Exposure", "Blacks", "Whites", "Saturation", "Vibrance", "Temperature", "Tint", "Highlights", "Shadows", "Clarity", "Contrast"];
const col = {};
for (const k of knobs) col[k] = { action: "resolve.property", type: "property", sens: 0.25, label: k };
col.D1 = { action: "resolve.jogSeconds", type: "jogSeconds", secondsPerStep: 60, detentThreshold: 1, label: "Scrub 1 min" };
col.D2 = { action: "resolve.jog", type: "jog", base: 1, gamma: 1.6, maxDelta: 600, label: "Scrub frames" };
col.C1 = { action: "resolve.marker", type: "marker", markerColor: "Blue", markerDurationSeconds: 5, label: "Marker" };
col.C2 = { action: "generic.key", type: "key", key: "Alt+P", label: "Play around" };
col.C3 = { action: "generic.key", type: "key", key: "Cmd+Alt+F", label: "Clean feed on" };
col.C4 = { action: "generic.key", type: "key", key: "Cmd+Alt+G", label: "Clean feed off" };
col.Undo = { action: "generic.key", type: "key", key: "Cmd+Z", label: "Undo" };
col.Redo = { action: "generic.key", type: "key", key: "Cmd+Shift+Z", label: "Redo" };
for (let i = 1; i <= 8; i++) col["P" + i] = { action: "resolve.property", type: "property", sens: 0.5, label: "Wheel " + i };
mk(RES, "DaVinci Resolve", ["resolve", "generic"], { col, edit: { D1: { ...col.D1, secondsPerStep: 10 }, D2: col.D2 }, "edit-shift": {}, "col-fn": { C3: col.C3, C4: col.C4 }, "col-2": {} }, { hudLevel: 2 });
mk("_global", "Global", ["generic"], { base: {
  D1: { action: "generic.scrollDelta", type: "scrollDelta", linesPerDetent: 3, detentThreshold: 1, gamma: 1.4, label: "Scroll" },
  D2: { action: "generic.keyDelta", type: "keyDelta", keyCW: "Cmd+=", keyCCW: "Cmd+-", detentThreshold: 2, label: "Zoom" },
  Undo: { action: "generic.key", type: "key", key: "Cmd+Z", label: "Undo" },
  Redo: { action: "generic.key", type: "key", key: "Cmd+Shift+Z", label: "Redo" },
  Copy: { action: "generic.key", type: "key", key: "Cmd+C", label: "Copy" },
  Paste: { action: "generic.key", type: "key", key: "Cmd+V", label: "Paste" },
  Tab: { action: "generic.key", type: "key", key: "Cmd+Tab", label: "App switch" },
  Left: { action: "generic.key", type: "key", key: "Left", label: "Left" }, Right: { action: "generic.key", type: "key", key: "Right", label: "Right" },
  Up: { action: "generic.key", type: "key", key: "Up", label: "Up" }, Down: { action: "generic.key", type: "key", key: "Down", label: "Down" },
}, shift: {}, cmd: {} });
mk(FIND, "Finder", ["finder", "generic"], { base: Object.fromEntries(["Red", "Orange", "Yellow", "Green", "Blue", "Purple", "Gray"].map((c, i) => ["P" + (i + 1) + "btn", { action: "finder.tag", type: "finderTag", tag: c, label: c }])) });
mk(CHR, "Google Chrome", ["browser", "generic"], { base: {
  D1: { action: "browser.scroll", type: "scrollDelta", linesPerDetent: 5, label: "Scroll page" },
  D2: { action: "browser.tab", type: "keyDelta", keyCW: "Cmd+Alt+Right", keyCCW: "Cmd+Alt+Left", detentThreshold: 3, label: "Switch tab" },
  C1: { action: "generic.key", type: "key", key: "Cmd+T", label: "New tab" },
  C2: { action: "generic.key", type: "key", key: "Cmd+W", label: "Close tab" },
  Left: { action: "browser.back", type: "key", key: "Cmd+[", label: "Back" }, Right: { action: "browser.back", type: "key", key: "Cmd+]", label: "Forward" },
}, shift: {}, "layer-2": {} });

let RC_DONE = false;
let ARMED = !P.has("extstart"), FIXED = false;
const PEND = P.has("pending") ? [
  { id: "3fa9c1e07b21", preset: "com.google.Chrome", file: "com.google.Chrome.json", kind: "change", changes: ["C1 in Google Chrome (base) would run a program", "setting dodeeMostGuard would switch off the project-name guard", "the panic key C8 in Global (base) would be removed or changed"], risks: ["shell", "guard-settings", "panic-removed"], detectedAt: Date.now() - 240e3 },
  { id: "7b21e07c3fa9", preset: "com.apple.finder", file: "com.apple.finder.json", kind: "removal", changes: ["Finder would lose all 7 assignments", "C2 in Finder (base) ran a command that deletes files"], risks: ["destructive"], detectedAt: Date.now() - 90e3 }] : [];
let FRONT = P.get("front") === "resolve" ? RES : CHR;
const RUNNING = [["com.apple.Safari", "Safari"], [CHR, "Google Chrome"], ["com.apple.finder", "Finder"], ["com.figma.Desktop", "Figma"], ["com.apple.Terminal", "Terminal"], ["com.microsoft.VSCode", "Visual Studio Code"], ["company.thebrowser.Browser", "Arc"], [RES, "DaVinci Resolve"]].map(([id, name]) => ({ id, name }));

/* ---------------- effective view (inheritance) ---------------- */
const FIELD_SRC = (a) => (a ? a.fields : []);
const globalBank = (p, bank) => presets._global.banks[bank] ? bank : "base";
function keyText(a, v) { return v.key || (v.keyCW ? v.keyCW + " / " + v.keyCCW : ""); }
function effectiveOne(p, bank, ctl) {
  const own = p.banks[bank] && p.banks[bank][ctl];
  const gl = presets._global.banks[globalBank(p, bank)] && presets._global.banks[globalBank(p, bank)][ctl];
  if (!own && !gl) return null;
  if (!own || p.id === "_global") {
    const src = own || gl; const a = ACTION[src.action] || byType(src.type);
    const fields = {};
    for (const f of FIELD_SRC(a)) fields[f.key] = f.key in src ? { value: src[f.key], source: "app" } : { value: f.default, source: "default" };
    return { source: own ? "app" : "global", action: src.action, summary: sum(a, src), fields };
  }
  const a = ACTION[own.action] || byType(own.type); const fields = {};
  for (const f of FIELD_SRC(a)) {
    if (f.key in own) fields[f.key] = { value: own[f.key], source: "app" };
    else if (gl && gl.type === own.type && f.key in gl) fields[f.key] = { value: gl[f.key], source: "global" };
    else fields[f.key] = { value: f.default, source: "default" };
  }
  const eff = Object.fromEntries(Object.entries(fields).map(([k, v]) => [k, v.value]));
  return { source: "app", action: own.action, summary: sum(a, { ...own, ...eff }), fields };
}
function sum(a, v) { return v.label ? String(v.label) : keyText(a, v) || (a ? a.label : "?"); }
function effective(p) {
  const out = {}; const names = new Set([...Object.keys(p.banks), ...Object.keys(presets._global.banks)]);
  for (const bank of names) {
    out[bank] = {};
    const ctls = new Set([...Object.keys(p.banks[bank] || {}), ...Object.keys(presets._global.banks[globalBank(p, bank)] || {})]);
    for (const c of ctls) { const e = effectiveOne(p, bank, c); if (e) out[bank][c] = e; }
  }
  return out;
}
const assignedCount = (p) => Object.values(p.banks).reduce((n, b) => n + Object.keys(b).length, 0);

/* ---------------- undo ---------------- */
const undo = [], redo = [];
function commit(label, fn) { undo.push({ label, snap: clone(presets) }); if (undo.length > 200) undo.shift(); redo.length = 0; fn(); return { ok: true, label }; }
function restore(snap) { for (const k of Object.keys(presets)) delete presets[k]; Object.assign(presets, snap); }

/* ---------------- validation ---------------- */
const MODS = ["Cmd", "Ctrl", "Alt", "Shift", "Fn"];
function validKey(s) {
  const parts = String(s).split("+"); const k = parts.pop();
  if (!k) return false; if (!parts.every((m) => MODS.includes(m))) return false;
  return /^([A-Za-z0-9]|F\d{1,2}|Left|Right|Up|Down|Space|Tab|Return|Escape|Delete|[=\-\[\]\;',./`])$/.test(k);
}
function validate(a, key, value) {
  const f = a.fields.find((x) => x.key === key);
  if (!f) throw new ApiError(`${a.label} has no field "${key}".`, key, 400);
  if (value === null) return;
  if (f.kind === "key" && !validKey(value)) throw new ApiError(`"${value}" is not a valid key chord (try Cmd+Alt+C).`, key, 400);
  if (f.kind === "number") {
    if (typeof value !== "number" || Number.isNaN(value)) throw new ApiError(`${f.label} must be a number.`, key, 400);
    if (f.min != null && value < f.min) throw new ApiError(`${f.label} must be at least ${f.min}.`, key, 400);
    if (f.max != null && value > f.max) throw new ApiError(`${f.label} must be at most ${f.max}.`, key, 400);
  }
}

/* ---------------- events ---------------- */
const events = []; let seq = 0;
function emit(control, kind, ticks, tps) {
  const p = presets[FRONT]; const bank = Object.keys(p.banks).includes("col") ? "col" : "base";
  let e = effectiveOne(p, bank, control); let handler = p.id;
  if (e && e.source === "global") handler = "_global";
  const ev = { seq: ++seq, t: Date.now(), type: "input", control, kind, rawTicks: ticks, ticksPerSec: tps, accel: 1, out: null, fine: false, presetId: handler, source: e ? e.source : "app", bank, action: e ? e.action : "" };
  if (e && kind === "turn") {
    const v = Object.fromEntries(Object.entries(e.fields).map(([k, f]) => [k, f.value]));
    const gamma = v.gamma || 1; ev.accel = +(Math.max(1, Math.pow(Math.max(tps, 1) / 20, gamma - 1 + 0.001) * (tps > 20 ? 1 : 1))).toFixed(2);
    const t = ACTION[e.action] ? ACTION[e.action].type : "";
    const n = Math.abs(ticks) * ev.accel;
    if (t === "scrollDelta") { const val = +(n * v.linesPerDetent / (v.detentThreshold || 1)).toFixed(1); ev.out = { text: `${ticks < 0 ? "up" : "down"} ${val} lines`, value: val, unit: "lines" }; }
    else if (t === "jogSeconds") { const val = +(n * v.secondsPerStep / (v.detentThreshold || 1)).toFixed(1); ev.out = { text: `${ticks < 0 ? "-" : "+"}${val} s`, value: val, unit: "seconds" }; }
    else if (t === "jog") { const val = +(n * v.base).toFixed(1); ev.out = { text: `${ticks < 0 ? "-" : "+"}${val} frames`, value: val, unit: "frames" }; }
    else if (t === "property") { const val = +(n * v.sens).toFixed(2); ev.out = { text: `${ticks < 0 ? "-" : "+"}${val}`, value: val, unit: "units" }; }
    else if (t === "keyDelta") ev.out = { text: ticks > 0 ? v.keyCW : v.keyCCW, value: null, unit: "key" };
  } else if (e && kind === "press") { const a = ACTION[e.action]; ev.out = { text: e.summary, value: null, unit: a ? a.type : "" }; }
  events.push(ev); if (events.length > 600) events.shift();
}
export function fire(control, kind, ticks, tps) { emit(control, kind || "turn", ticks == null ? 1 : ticks, tps == null ? 12 : tps); }
/** test hooks: an event the editor swallowed ("editor focused, not fired"), and one nothing handled */
function fireSpecial(control, kind, special) { emit(control, kind, kind === "turn" ? 3 : 0, kind === "turn" ? 20 : 0); const ev = events[events.length - 1]; if (special === "focused") ev.out = { text: "editor focused, not fired", value: null, unit: "" }; else { ev.presetId = ""; ev.action = ""; ev.out = { text: "+0 units", value: 0, unit: "units" }; } }
window.__mock = { fire, fireSpecial, setFront: (id) => { FRONT = id; } };
/** import preview with risk (a bundle that runs programs), for the in-page risk panel */
export function importReport(dry, confirm, skip) {
  const files = P.has("unk") ? [{ id: "com.test.Odd", name: "Odd", status: "new", added: 3, changed: 0, removed: 0, risk: { shell: 0, keys: 0, destructive: 0, unknown: 2, panic: 1, panicOverrides: 1 }, unknown: 2, needsConfirm: true, runsProgram: [], overrides: ["Resolve edit.C8"] }] : [{ id: "com.test.Shelly", name: "Shelly", status: "new", added: 6, changed: 0, removed: 0, risk: { shell: 2, keys: 3, destructive: 1 }, runsProgram: ["/usr/bin/open -a Terminal", "/bin/sh -c 'rm -rf ~/Library/Caches/2t5-deck-plus-tmp'"] },
    { id: "com.apple.finder", name: "Finder", status: "changed", added: 1, changed: 2, removed: 0, risk: { shell: 0, keys: 4, destructive: 0 }, runsProgram: [] }];
  if (P.has("longprogs")) { // a very long program list (40 lines, some with one unbroken 300-character token) for the risk-sheet scroll and wrap check
    files[0].runsProgram = [...Array(40)].map((_, i) => i % 7 === 3 ? "/usr/bin/curl https://example.invalid/" + "a".repeat(300) : `/bin/sh -c 'rm -rf ~/Library/Caches/2t5-deck-plus-tmp-${i}'`);
    files[0].risk = { shell: 40, keys: 3, destructive: 6 };
  }
  if (dry) return { files, ok: true };
  if (!confirm && !skip) throw Object.assign(new ApiError("This bundle runs programs. Confirm to import.", null, 409), { report: { files } });
  if (confirm && P.has("riskchanged") && !RC_DONE) { RC_DONE = true; const nf = [...files, { id: "com.test.Extra", name: "Extra", status: "new", added: 1, changed: 0, removed: 0, risk: { shell: 1, keys: 0, destructive: 1 }, runsProgram: ["base.C3: rm -rf ~/Documents/Old"] }]; throw Object.assign(new ApiError("import refused: what it would install changed since you reviewed it", null, 409), { report: { files: nf, riskChanged: true, needsConfirm: true } }); }
  return { ok: true, imported: files.length };
}

if (P.get("auto") !== "0") {
  const picks = ["D1", "D2", "Contrast", "D1", "Exposure"]; let n = 0, burst = null;
  setInterval(() => {
    if (!burst && Math.random() < 0.06) {
      const cc = Math.random() < 0.25 ? ["Undo", "C1", "C2"][n % 3] : picks[n % picks.length]; n++;
      burst = ["Undo", "C1", "C2"].includes(cc) ? { c: cc, press: true, left: 2 } : { c: cc, left: 14 + Math.floor(Math.random() * 12), i: 0, dir: Math.random() < 0.8 ? 1 : -1 };
    }
    if (!burst) return;
    if (burst.press) { emit(burst.c, burst.left === 2 ? "press" : "release", 0, 0); if (--burst.left <= 0) burst = null; return; }
    const prog = burst.i++ / 22; const tps = Math.round(4 + 56 * Math.sin(Math.min(1, prog) * Math.PI));
    emit(burst.c, "turn", burst.dir * Math.max(1, Math.round(tps / 12)), Math.max(2, tps));
    if (--burst.left <= 0) burst = null;
  }, 85);
}

/* ---------------- router ---------------- */
export async function handle(method, path, body) {
  await wait(25);
  const url = new URL(path, "http://x"); const p = url.pathname; const qs = url.searchParams;
  let m;
  if (method === "GET" && p === "/api/deck") return fetch(new URL("./mock-deck.json", import.meta.url)).then((r) => r.json());
  if (method === "GET" && p === "/api/registry") return clone(REGISTRY);
  if (method === "GET" && p === "/api/apps") return Object.values(presets).map((x) => ({ id: x.id, name: x.name, packs: x.packs, isGlobal: x.id === "_global", assigned: assignedCount(x), frontmost: x.id === FRONT })).sort((a, b) => (b.isGlobal - a.isGlobal) || (a.id === RES ? -1 : b.id === RES ? 1 : a.name.localeCompare(b.name)));
  if (method === "POST" && p === "/api/apps") {
    if (!body || !/^[\w.-]+\.[\w.-]+/.test(body.id || "")) throw new ApiError("Bundle id looks like com.vendor.App.", "id", 400);
    if (presets[body.id]) throw new ApiError("That app already has a preset.", "id", 409);
    const browser = /chrome|safari|arc|firefox|brave|browser/i.test(body.id + body.name);
    return commit("Add app " + (body.name || body.id), () => mk(body.id, body.name || body.id, body.packs || (browser ? ["browser", "generic"] : ["generic"]), { base: {} }));
  }
  if (method === "POST" && p === "/api/apps/capture") {
    const r = RUNNING.find((x) => x.id === "com.figma.Desktop");
    if (presets[r.id]) throw new ApiError(`${r.name} already has a preset.`, null, 409);
    commit("Add app " + r.name, () => mk(r.id, r.name, ["generic"], { base: {} })); return { id: r.id, name: r.name };
  }
  if (method === "GET" && p === "/api/backups") return { backups: [{ name: "presets-2026-11-04.zip", size: 18432, time: Date.now() - 3600e3, reason: "daily", trusted: true }, ...(P.has("untrusted") ? [{ name: "presets-planted-2026-11-04.zip", size: 9216, time: Date.now() - 600e3, trusted: false }] : [])] };
  if (method === "POST" && (m = p.match(/^\/api\/backups\/([^/]+)\/restore$/))) {
    if (/planted/.test(decodeURIComponent(m[1])) && !qs.has("confirmRisk") && !qs.has("skipRisky")) throw Object.assign(new ApiError("This backup was not made by this app and holds risky actions.", null, 409), { data: { needsConfirm: true }, report: { needsConfirm: true, files: importReport(true).files, risk: { shell: 2, keys: 3, destructive: 1 }, inherited: [], dropped: [] } });
    return { ok: true, dropped: qs.has("skipRisky") ? [{}, {}, {}] : [], seq: 9 };
  }
  if (method === "GET" && p === "/api/pending-external") return { pending: clone(PEND) };
  if (method === "POST" && (m = p.match(/^\/api\/pending-external\/([^/]+)\/(approve|reject)$/))) {
    const i = PEND.findIndex((x) => x.id === decodeURIComponent(m[1])); if (i < 0) throw new ApiError("That change is no longer pending.", "id", 404);
    if (m[2] === "approve" && !(body && body.confirmRisk)) throw new ApiError("Confirm the risk first.", "confirmRisk", 400);
    PEND.splice(i, 1); return { ok: true, seq: 10 };
  }
  if (method === "GET" && p === "/api/preflight") return { checks: [{ id: "accessibility", label: "Accessibility access", status: "ok", detail: "Granted" }, { id: "pendingExternal", label: "Outside changes", status: PEND.length ? "warn" : "ok", detail: PEND.length ? `${PEND.length} preset file change${PEND.length === 1 ? " was" : "s were"} held back` : "None waiting" }] };
  if (method === "GET" && p === "/api/dependencies") {
    const D = (id, name, required, feature, status, extra = {}) => ({ id, name, kind: "runtime", required, feature, howToGet: "Mock: see docs/dependencies.json.", howToCheck: "Mock.", without: "Mock.", status, detail: "", fixAction: "", copyText: "", informational: false, ...extra });
    const items = [D("accessibility", "Accessibility permission", true, "Keys, clicks and scrolling", "ok"), D("loupedeck", "Loupedeck+ plugged in", true, "The deck itself", "ok"),
      D("python3", "Python 3", false, "DaVinci Resolve control", "missing", { fixAction: "copy-text", copyText: "brew install python" }), D("shell-commands", "Programs your Run command keys call", false, "Keys you set to run a command", "info", { informational: true })];
    return { ok: true, items, counts: {}, requiredMissing: [] };
  }
  if (method === "POST" && p === "/api/open-settings") return { ok: true, pane: body && body.pane };
  if (method === "GET" && p === "/api/about") return { name: "2T5 DECK+", by: "2T5 Film", version: "0.1.1", canOpen: false, links: [{ id: "site", label: "2t5.co", url: "https://2t5.co" }, { id: "instagram", label: "instagram.com/tunahanemre", url: "https://instagram.com/tunahanemre" }, { id: "sponsors", label: "Donate", url: "https://github.com/sponsors/tunahanemre" }] };
  if (method === "POST" && p === "/api/open-link") return { ok: true, id: body && body.id };
  if (method === "GET" && p === "/api/running") return clone(RUNNING);
  if (method === "GET" && p === "/api/live") { const f = presets[FRONT]; return { frontmost: { id: f.id, name: f.name }, activeApp: f.id, engineRunning: true, sidecar: "connected", seq: 0, armed: ARMED, ...(P.has("extstart") ? { armedAtStart: false, startReason: "external-changes" } : {}), ...(P.get("cov") === "none" ? {} : { armCoverage: P.get("cov") === "partial" ? ["keys", "mouse", "scroll"] : ["keys", "mouse", "scroll", "shell", "sidecar-writes"] }), permissions: P.has("noperm") ? { accessibility: false, inputMonitoring: true } : { accessibility: true, inputMonitoring: true }, engine: { applied: 1, saved: P.has("diverge") ? 2 : 1, readonly: P.has("readonly"), composeError: P.has("composeerr") ? "preset Chrome: unknown bank col-9" : "" } }; }
  if (method === "GET" && p === "/api/arm") return { armed: ARMED };
  if (method === "POST" && p === "/api/arm") { ARMED = !!body.armed; return { ok: true, armed: ARMED }; }
  if (method === "GET" && p === "/api/templates") return [{ id: "chrome", name: "Google Chrome", bundleId: "com.google.Chrome", description: "Scroll, tabs, zoom" }, { id: "safari", name: "Safari", bundleId: "com.apple.Safari", description: "Scroll, tabs, history" }, { id: "blender", name: "Blender", bundleId: "org.blenderfoundation.blender", description: "Viewport and timeline" }, { id: "terminal", name: "Terminal", bundleId: "com.apple.Terminal", description: "Scroll and tabs" }];
  if (method === "POST" && p === "/api/apps/from-template") { const t = { chrome: ["com.google.Chrome", "Google Chrome"], safari: ["com.apple.Safari", "Safari"], blender: ["org.blenderfoundation.blender", "Blender"], terminal: ["com.apple.Terminal", "Terminal"] }[body.template]; if (!t) throw new ApiError("Unknown template.", "template", 404); if (presets[t[0]]) throw new ApiError("That app already has a preset.", "id", 409); commit("Add app " + t[1], () => mk(t[0], t[1], ["generic"], { base: {} })); return { id: t[0], name: t[1] }; }
  if (method === "DELETE" && (m = p.match(/^\/api\/apps\/([^/]+)$/))) { const id = decodeURIComponent(m[1]); if (!presets[id] || id === "_global") throw new ApiError("Unknown app.", "id", 404); return commit("Remove app " + presets[id].name, () => { delete presets[id]; }); }
  if (method === "GET" && p === "/api/warnings") { const extra = P.has("warns") ? [{ severity: "warn", kind: "settings-range", app: "com.blackmagic-design.DaVinciResolve", field: "fineFactor", message: "fineFactor 4 is above the safe maximum 1" }, { severity: "warn", kind: "seed-fallback", app: "com.google.Chrome", message: "used the shipped preset" }, { severity: "warn", kind: "disarm-marker", message: "the deck was left disarmed" }, ...(PEND.length ? [{ severity: "warn", kind: "external-pending", app: "com.google.Chrome", field: "com.google.Chrome.json", message: "an outside change to com.google.Chrome.json was NOT applied" }] : [])] : []; return { counts: { warn: P.has("damaged") ? 1 : 0, hint: 0 }, warnings: [...extra, ...(P.has("damaged") && !FIXED ? [{ severity: "warn", kind: "unreadable-file", field: "com.google.Chrome.json", app: CHR, message: "unreadable" }] : [])] }; }
  if (method === "POST" && /restore-last-good$/.test(p)) { FIXED = true; return { ok: true }; }
  if (method === "GET" && p === "/api/search") { const q = (qs.get("q") || "").toLowerCase(); const hits = []; for (const pr of Object.values(presets)) for (const [bk, b] of Object.entries(pr.banks)) for (const [c, v] of Object.entries(b)) { const a = ACTION[v.action]; if ((c + " " + (v.label || "") + " " + (a ? a.label : "") + " " + pr.name).toLowerCase().includes(q)) hits.push({ app: pr.id, appName: pr.name, bank: bk, control: c, action: v.action, label: v.label || "", summary: "" }); } return { q, truncated: false, hits: hits.slice(0, +qs.get("limit") || 40) }; }
  if (method === "GET" && p === "/api/defaults/diff") return { app: qs.get("app"), controls: [{ bank: "col", control: "D1", change: "changed" }, { bank: "col", control: "Exposure", change: "changed" }, { bank: "base", control: "D1", change: "changed" }] };
  if (method === "GET" && p === "/api/events") { const since = +qs.get("since") || 0; return { next: seq + 1, events: events.filter((e) => e.seq >= since) }; }
  if (method === "POST" && p === "/api/undo") { const u = undo.pop(); if (!u) return { ok: false, label: "Nothing to undo" }; redo.push({ label: u.label, snap: clone(presets) }); restore(u.snap); return { ok: true, label: u.label }; }
  if (method === "POST" && p === "/api/redo") { const u = redo.pop(); if (!u) return { ok: false, label: "Nothing to redo" }; undo.push({ label: u.label, snap: clone(presets) }); restore(u.snap); return { ok: true, label: u.label }; }
  if (method === "POST" && p === "/api/simulate") { emit(body.control, body.kind || "turn", body.ticks || 1, body.ticksPerSec || 12); return { ok: true }; }
  if ((m = p.match(/^\/api\/preset\/([^/]+)(\/[\w-]+)?$/))) {
    const id = decodeURIComponent(m[1]); const pr = presets[id]; const sub = m[2];
    if (!pr) throw new ApiError("Unknown app " + id, null, 404);
    if (method === "GET" && !sub) return { preset: clone(pr), effective: effective(pr) };
    if (method === "POST" && sub === "/reset-defaults") {
      // mirrors the engine: a template app restores its template, Resolve its seed, anything else has nothing shipped to go back to
      const TPL = { "com.google.Chrome": "Google Chrome", "com.apple.Safari": "Safari", "org.blenderfoundation.blender": "Blender", "com.apple.Terminal": "Terminal" };
      if (TPL[id]) return commit(`Restore ${pr.name} defaults`, () => {}) && { ok: true, seq: 0, label: `Restore ${pr.name} defaults`, restoredTo: { source: "template", templateId: id, templateName: TPL[id], controls: assignedCount(pr) } };
      if (id === RES) return { ok: true, seq: 0, label: "Restore defaults", restoredTo: { source: "resolve-seed", controls: assignedCount(pr) } };
      const er = new ApiError("no shipped preset for this app", "id", 400); er.data = { error: er.message, field: "id", reason: "no-shipped-preset" }; throw er;
    }
    if (method === "PUT" && sub === "/assign") {
      const a = ACTION[body.assignment.action];
      if (!a) throw new ApiError("Unknown action " + body.assignment.action, "action", 400);
      if (!pr.packs.includes(a.pack)) throw new ApiError(`${a.label} is not available for ${pr.name}.`, "action", 400);
      return commit(`${pr.name}: ${body.control} = ${a.label}`, () => { (pr.banks[body.bank] = pr.banks[body.bank] || {})[body.control] = { ...body.assignment, type: a.type }; });
    }
    if (method === "DELETE" && sub === "/assign") return commit(`${pr.name}: clear ${qs.get("control")}`, () => { delete (pr.banks[qs.get("bank")] || {})[qs.get("control")]; });
    if (method === "PUT" && sub === "/value") {
      let cur = (pr.banks[body.bank] || {})[body.control];
      if (!cur) { const g = effectiveOne(pr, body.bank, body.control); if (!g) throw new ApiError("Control has no assignment.", null, 404); cur = null; }
      const a = cur ? ACTION[cur.action] : ACTION[effectiveOne(pr, body.bank, body.control).action];
      validate(a, body.key, body.value);
      return commit(`${pr.name}: ${body.control} ${a.fields.find((f) => f.key === body.key).label}${body.value === null ? " reset" : " = " + body.value}`, () => {
        const b = (pr.banks[body.bank] = pr.banks[body.bank] || {});
        if (!b[body.control]) b[body.control] = { action: a.id, type: a.type };
        if (body.value === null) delete b[body.control][body.key]; else b[body.control][body.key] = body.value;
      });
    }
    if (method === "PUT" && sub === "/settings" && P.has("range") && body.value > 0.9) throw new ApiError("fineFactor must be between 0.05 and 0.9", "fineFactor", 400);
    if (method === "PUT" && sub === "/settings") return commit(`${pr.name}: setting ${body.key}`, () => { if (body.value === null) delete pr.settings[body.key]; else pr.settings[body.key] = body.value; });
  }
  throw new ApiError(`mock: no route ${method} ${p}`, null, 404);
}

/* ---------------- fake Wails bindings (packaged-app flow), ?native=ok|cancel|risk|fail|expired  &nexport=ok|cancel|fail ---------------- */
if (P.has("native")) {
  const mode = P.get("native"), ex = P.get("nexport") || "ok";
  const plain = [{ id: "com.google.Chrome", name: "Google Chrome", status: "changed", added: 2, changed: 1, removed: 0, risk: { shell: 0, keys: 3, destructive: 0 }, runsProgram: [] }];
  const risky = [{ id: "com.test.Shelly", name: "Shelly", status: "new", added: 6, changed: 0, removed: 0, risk: { shell: 2, keys: 3, destructive: 1 }, runsProgram: ["base.C1: /usr/bin/open -a Terminal", "base.C2: /bin/sh -c 'rm -rf ~/Library/Caches/2t5-deck-plus-tmp'"] }, ...plain];
  window.go = { main: { App: {
    ExportPresets: async () => { await wait(150); if (ex === "cancel") return ""; if (ex === "fail") throw new Error("could not save the export: permission denied"); return "/Users/you/Desktop/2t5-deck-plus-presets.zip"; },
    ImportPresetsFromDialog: async () => { await wait(150); if (mode === "fail") throw new Error("could not open the file: no such file"); if (mode === "cancel") return { token: "", dryRun: true, files: [], risk: { shell: 0, keys: 0, destructive: 0 }, needsConfirm: false };
      const files = mode === "risk" ? risky : plain; return { token: "tok-1", name: "bundle.zip", dryRun: true, files, risk: { shell: mode === "risk" ? 2 : 0, keys: 3, destructive: mode === "risk" ? 1 : 0 }, needsConfirm: mode === "risk" }; },
    ApplyImport: async () => { await wait(150); if (mode === "expired") throw new Error("this import is no longer pending: choose the file again"); if (mode === "risk") throw new Error("import refused: it installs actions that run programs or are destructive; confirm the risk to import it"); return { ok: true, dryRun: false, files: plain, seq: 3 }; },
    ApplyImportSkippingRisky: async () => { await wait(150); return { ok: true, dryRun: false, files: plain, dropped: 2, seq: 5 }; },
    ApplyImportConfirmed: async () => { await wait(150); return { ok: true, dryRun: false, files: risky, seq: 4 }; },
  } } };
}
