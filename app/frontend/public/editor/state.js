// state.js: shared editor state and the pure lookups every module needs. No DOM.
import { bankLabel } from "./util.js";
export const GLOBAL = "_global";
export const RESOLVE_BANKS = ["col", "edit", "edit-shift", "col-fn", "col-2"];
export const APP_BANKS = ["base", "shift", "cmd", "alt", "ctrl", "fn", "layer-2", "layer-3"];

export const S = {
    profile: null, registry: null, actions: {}, packs: [], apps: [], live: null,
    app: null, bank: null, control: null, pr: null, // pr = {raw, eff}
    since: 0, apiDown: 0, draft: {}, lastEv: null,
    flags: { callouts: false, allEdges: false },
    editBase: false, // true: the P1-P8 inspector edits the base assignment, whatever the slider mode
    sMode: "hue", // the slider mode being edited (Resolve): hue | sat | lum. Latched by clicking Hue / Sat / Lum on the deck.
    liveMode: null, customOn: false, // what the deck itself is in right now (LEDs): slider mode from the engine, Custom Mode on/off
    unit: {}, // per-field unit preference (seconds | minutes)
    noRestore: new Set(), // apps the engine said have no shipped preset (learned from one 400, kept for the session)
    seeded: new Set(),
    restored: new Set(), // apps restored this session: the Restore button stays on for them
    ep: {},   // feature-detected optional endpoints: name -> true | false | undefined
};

export const appInfo = (id) => S.apps.find((a) => a.id === id);
export const isGlobalApp = () => S.app === GLOBAL;
export const appName = (id) => { const a = appInfo(id); return a ? (a.isGlobal ? "Global" : a.name) : id === GLOBAL ? "Global" : id || "?"; };
export const isResolve = () => !!S.app && (S.app.includes("DaVinciResolve") || (appInfo(S.app)?.packs || []).includes("resolve"));
/** an eff() with no bank is the VIEW of a control: a P1-P8 slider or button shows what the latched slider mode gives it (its own assignment, else the base one, flagged baseFallback); any other call reads the named bank as stored */
export const eff = (bank, ctl = S.control) => (bank === undefined ? effv(ctl) : S.pr?.eff?.[bank]?.[ctl] || null);
export const PM_RE = /^(?:(.*)-)?pm-(hue|sat|lum)$/;
/** the mode layer bank of a page: Resolve {page}-pm-{mode}, every other app and Global pm-{mode} */
export const modeBank = (mode, page = parseBank(S.bank).page) => (isResolve() ? `${page}-pm-${mode}` : `pm-${mode}`);
/** does the slider-mode latch apply to this control right now? Only P1-P8 and P1btn-P8btn, not while a modifier layer is latched (a held key wins over the mode), not while editing the base */
export const modeOn = (ctl) => !!S.sMode && !S.editBase && isPControl(ctl) && !parseBank(S.bank).mod && !!S.pr;
export function effv(ctl) {
    const base = S.pr?.eff?.[S.bank]?.[ctl] || null;
    if (!modeOn(ctl)) return base;
    const own = S.pr?.eff?.[modeBank(S.sMode)]?.[ctl];
    if (own) return own;
    return base ? { ...base, baseFallback: true, baseSource: base.source, source: "global" } : null; // "global" keeps the inherited behaviour of the inspector (dimmed values, Override); the words say base
}
/** where a write for this control goes: its mode layer while a mode is latched, else the bank on show */
export const wbank = (ctl = S.control) => (modeOn(ctl) ? modeBank(S.sMode) : S.bank);
/** every control with something to show in the current bank, mode-aware: id -> effective */
export function bankView() {
    const out = {}, base = S.pr?.eff?.[S.bank] || {};
    for (const id of Object.keys(base)) out[id] = effv(id);
    if (S.sMode && !S.editBase && !parseBank(S.bank).mod) for (const id of Object.keys(S.pr?.eff?.[modeBank(S.sMode)] || {})) out[id] = effv(id);
    return out;
}
export const allBanks = () => [...banks(), ...Object.keys(S.pr?.eff || {}).filter((b) => PM_RE.test(b)).sort()];
export const ctlMeta = (id) => S.profile?.controls?.find((c) => c.id === id);
export const actionDef = (id) => S.actions[id] || null;
export function banks() {
    const have = Object.keys(S.pr?.raw?.banks || {}).filter((b) => !PM_RE.test(b)); // the mode layers have no door of their own: the Hue | Sat | Lum switch is it
    const base = isResolve() ? RESOLVE_BANKS : APP_BANKS;
    return [...new Set([...base, ...have])];
}
/* A bank id is PAGE or PAGE-MODIFIER: "col", "col-shift", "edit-shift", "col-2", "shift" (= base page held with Shift). Two axes: the page, and the key held. */
export const MODS = [{ id: "shift", label: "Shift", key: "Shift" }, { id: "ctrl", label: "Ctrl", key: "Ctrl" }, { id: "cmd", label: "\u2318", name: "Cmd", key: "Cmd" }, { id: "alt", label: "Alt", key: "Alt" }, { id: "fn", label: "Fn", key: "Fn" }];
export function parseBank(b) { const pm = PM_RE.exec(String(b)); if (pm) return { page: pm[1] || "base", mod: null, pm: pm[2] }; const m = /^(?:(.*)-)?(shift|ctrl|cmd|alt|fn)$/.exec(String(b)); return m ? { page: m[1] || "base", mod: m[2] } : { page: String(b), mod: null }; }
export const composeBank = (page, mod) => (!mod ? page : page === "base" ? mod : page + "-" + mod);
export const modGlyph = { shift: "\u21E7", ctrl: "\u2303", cmd: "\u2318", alt: "\u2325", fn: "fn" };
export const bankName = (b) => { const { page, mod, pm } = parseBank(b), m = MODS.find((x) => x.id === mod); return bankLabel(page) + (m ? ` + ${m.name || m.label}` : "") + (pm ? ` + ${smodeLabel(pm)} mode` : ""); };
/** pages are layered: "col", "col-2", "col-3" (root col, layer 1-3); an app's "base", "layer-2", "layer-3" (root base). A deck key of type Switch layer picks the layer inside the current root. */
export const layerOf = (page) => { const m = /(?:^|-)(\d)$/.exec(String(page)); return m ? +m[1] : 1; };
export const rootOf = (page) => { const p = String(page); return /^layer-\d$/.test(p) ? "base" : p.replace(/-\d$/, ""); };
export const pageFor = (root, n) => (n <= 1 ? root : root === "base" ? "layer-" + n : root + "-" + n);
export const pages = () => [...new Set(banks().map((b) => parseBank(b).page))];
export const modsFor = (page) => MODS.filter((m) => banks().includes(composeBank(page, m.id))).map((m) => m.id);
export const bankCount = (b) => Object.keys(S.pr?.eff?.[b] || {}).filter((c) => S.pr.eff[b][c].source === "app").length;
export const bankAssigned = (b) => Object.keys(S.pr?.eff?.[b] || {}).length;
export function visiblePacks() {
    const have = new Set(appInfo(S.app)?.packs || S.pr?.raw?.packs || ["generic"]);
    return S.packs.filter((p) => p.always || have.has(p.id));
}

/* ---------- icons per pack / action (Lucide names, see addendum 2 A2.1) */
export const PACK_ICON = { generic: "shapes", resolve: "clapperboard", browser: "globe", finder: "folder", global: "command" };
const ACTION_ICON = {
    "generic.key": "keyboard", "generic.keyDelta": "arrow-left-right", "generic.scrollDelta": "mouse", "generic.shell": "terminal", "generic.layer": "layers",
    "generic.modifierHold": "command", "generic.none": "ban", "generic.toggleHud": "panel-top",
    "resolve.scrubFrames": "film", "resolve.scrubSeconds": "timer", "resolve.snapMinute": "timer-reset", "resolve.wheel": "palette", "resolve.balance": "palette",
    "resolve.cdl": "sliders-horizontal", "resolve.property": "sliders-vertical", "resolve.addMarker": "bookmark", "resolve.markerColor": "bookmark", "resolve.markerAtPlayhead": "bookmark",
    "resolve.selectClip": "mouse-pointer-click", "resolve.selectClipVert": "mouse-pointer-click", "resolve.selectClipHoriz": "mouse-pointer-click", "resolve.selectClipClear": "mouse-pointer-click",
    "resolve.fineMode": "zoom-in", "resolve.quickExport": "upload",
    "browser.scroll": "arrow-up-down", "browser.tabs": "app-window", "browser.zoom": "scan-search", "browser.back": "arrow-left-right", "browser.forward": "arrow-left-right", "browser.history": "arrow-left-right",
    "browser.reload": "rotate-ccw", "browser.newTab": "app-window", "browser.closeTab": "app-window", "browser.reopenTab": "app-window", "browser.zoomReset": "scan-search",
    "finder.tag": "tag",
    "global.copy": "copy", "global.cut": "square-dashed", "global.paste": "download", "global.undo": "undo-2", "global.redo": "redo-2", "global.undoRedo": "undo-2",
    "global.print": "printer", "global.refresh": "rotate-ccw", "global.zoomIn": "zoom-in", "global.zoomOut": "minus", "global.zoom": "scan-search",
    "global.missionControl": "layout-grid", "global.appExpose": "layout-grid", "global.switchApp": "app-window", "global.showDesktop": "square",
    "global.desktopLeft": "arrow-left-right", "global.desktopRight": "arrow-left-right", "global.desktops": "arrow-left-right", "global.minimize": "minus",
    "global.closeWindow": "x", "global.newTab": "plus", "global.closeTab": "x", "global.nextTab": "app-window", "global.prevTab": "app-window", "global.tabs": "arrow-left-right",
    "global.screenCapture": "scan-eye", "global.screenCaptureArea": "square-dashed", "global.search": "search", "global.launchpad": "layout-grid", "global.lock": "lock", "global.emoji": "sparkles",
    "global.volumeUp": "gauge", "global.volumeDown": "gauge", "global.mute": "ban",
};
export const actionIcon = (id) => ACTION_ICON[id] || (String(id).startsWith("resolve.") ? "puzzle" : "puzzle");

/** Which actions fit a control kind: knobs take turn actions, buttons take press actions, sliders/dial take anything. */
export function compat(a, ctlKind) {
    const k = a.kind;
    if (!k || k === "both") return true;
    if (ctlKind === "knob") return k === "turn";
    if (ctlKind === "button") return k === "press";
    return true;
}
export const compatLabel = (a) => (a.kind === "turn" ? "Knobs and dials" : a.kind === "press" ? "Keys" : "Any control");

/* ---------- field grouping for the essentials inspector and the drawers */
const SENS_KEYS = new Set(["detentThreshold", "sens", "gamma", "base", "linesPerDetent", "secondsPerStep", "maxDelta", "maxDrag", "jogSustainMs", "jogSustainSeconds"]);
const ADV_KEYS = new Set(["flags", "press", "gang", "point", "target", "min", "max"]);
export const isSensField = (f) => (SENS_KEYS.has(f.key) || /^jogTier/.test(f.key) || f.sensitivity === true || f.group === "sensitivity");
export function groupFields(a) {
    const fields = a ? a.fields : [];
    const turn = !!a && (a.kind === "turn" || a.kind === "both");
    const sens = turn ? fields.filter(isSensField) : [];
    const sensSet = new Set(sens);
    const flagged = fields.filter((f) => f.primary === true);
    let primary;
    if (flagged.length) primary = flagged.slice(0, 3);
    else {
        const rest = fields.filter((f) => !sensSet.has(f) && !ADV_KEYS.has(f.key));
        const body = rest.filter((f) => f.key !== "label"), label = rest.filter((f) => f.key === "label");
        primary = [...(sens.length ? [sens[0]] : []), ...body, ...label].slice(0, 3);
    }
    const pset = new Set(primary);
    const adv = fields.filter((f) => !pset.has(f) && !sensSet.has(f));
    return { primary, sens, adv, all: fields };
}
export const curveFn = (v) => {
    const step = v.secondsPerStep ?? v.linesPerDetent ?? v.sens ?? v.base ?? 1;
    const g = v.gamma ?? 1, ref = 20, thr = Math.max(1, v.detentThreshold ?? 1);
    return (t) => (t <= 0 ? 0 : (Math.abs(step) / thr) * t * (t > ref ? Math.pow(t / ref, Math.max(0, g - 1)) : 1));
};

/* ---------- Resolve slider mode: Hue / Sat / Lum change what the P row does on the real deck */
export const SMODES = [{ value: "hue", label: "Hue" }, { value: "sat", label: "Sat" }, { value: "lum", label: "Lum" }];
export const smodeLabel = (m) => (SMODES.find((x) => x.value === m) || {}).label || "";
export const isPControl = (id) => /^P[1-8](btn)?$/.test(String(id));
