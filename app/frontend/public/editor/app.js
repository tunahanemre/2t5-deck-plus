// app.js: 2T5 DECK+ deck editor. State, API orchestration, app picker, page and held-key tabs, top bar, live events.
// Presentation lives in ui.js / stage.js / inspector.js / drawers.js / dock.js; this file wires them.
import { api, ApiError, MOCK, iconUrl, events, clog } from "./api.js";
import { $, $$, h, icon, clamp, reduced, anim, EASE_OUT, EASE_IN, store, debounce, bankLabel, fmt, willChange, setReduceOverride, setContrastOverride, springTo, springSet } from "./util.js";
import { SMODES, smodeLabel, isPControl, effv, wbank, modeOn, modeBank, bankView, allBanks, PM_RE, S, GLOBAL, appInfo, appName, isGlobalApp, isResolve, eff, ctlMeta, actionDef, banks, bankCount, bankAssigned, MODS, modGlyph, bankName, parseBank, composeBank, pages, modsFor, layerOf, rootOf, pageFor, visiblePacks, actionIcon } from "./state.js";
import { HOT } from "./hotspots.js";
import { initStageBg } from "./stagebg.js";
import { openMenu, popover, initMiniTips, toast, clearToasts, appTile, switchCtl, textField, keycaps, stateChip, KIND, confirmButton, closeAllPopovers, segmented } from "./ui.js";
import { createStage } from "./stage.js";
import { createDock } from "./dock.js";
import { renderInspector, perClick } from "./inspector.js";
import { createDrawers } from "./drawers.js";
import { openPalette } from "./palette.js";
import { createList } from "./list.js";
import { createAppPicker, orderedApps } from "./appPicker.js";
import { plain, plainControl, plainChange, restoreApp } from "./plain.js";
import { commandItems, rankCommands, score as fuzzy } from "./commands.js";
import { GROUP_OF } from "./hotspots.js";
import { decide, afterDismiss, needsFor, summarize as summarizeDeps } from "./depsLogic.js";

const QS = new URLSearchParams(location.search);
{ const ac = QS.get("accent") || store.get("accent", "c"); document.documentElement.dataset.accent = ["a", "b", "c"].includes(ac) ? ac : "c"; if (QS.get("accent")) store.set("accent", ac); }
const SPRING_SMOOTH = "linear(0,.053,.173,.316,.459,.587,.696,.783,.85,.901,.937,.963,.98,.991,.998,1.002,1.004,1.004,1.004,1.004,1.003,1.003,1)";

/* ================================================================== shared context for the modules */
const ctx = {
    rows: [], drows: [], curve: null, stage: null, dock: null, drawers: null,
    queueValue: (k, v) => queueValue(k, v), setPinned: (v) => setPinned(v), saveNow: (k, v) => { clearTimeout(timers[k]); delete pending[k]; S.draft[k] = v; return saveValue(k, v); }, ghostFor: (k) => ghostFor(k), ghostValues: () => ghostValues(), paintNotices: () => renderNotice(), loadApps: () => loadApps(), selectApp: (id) => selectApp(id), heldNote: (id) => heldNote(id), sliderMode: () => sliderModeName(), modeAvail: () => !!S.sMode && !parseBank(S.bank).mod, setSliderMode: (m) => setSliderMode(m), renderAppPicker: () => renderAppPicker(), resetValue: (k) => resetValue(k), resetMany: (ks) => resetMany(ks),
    pickAction: (id) => pickAction(id), assignTo: (c, a) => assignTo(c, a), commitPending: () => commitPending(), cancelPending: () => cancelPending(), overrideWhole: () => overrideWhole(), clearAssign: () => clearAssign(),
    openDrawer: (n, from) => ctx.drawers.open(n, from), loadPending: () => loadPending(), doUndo: () => doUndo(), doRedo: () => doRedo(), reloadAll: () => reloadAll(),
    fail: (e) => fail(e), epChanged: () => {}, doReset: null,
    onDrawerChange: (n) => onDrawerChange(n), onDrawerClosing: () => syncInsp(), onLiveChange: (o, hgt) => onLiveChange(o, hgt),
    list: null, selectRow: (b, c) => selectRow(b, c), unassignRow: (b, c) => unassignRow(b, c), setOwnership: (m) => setOwnership(m),
};

/* ================================================================== data */
async function loadPreset() {
    const r = await api.preset(S.app);
    S.pr = { raw: r.preset || r.raw || r, eff: r.effective || {} };
    if (S.app !== GLOBAL) { try { const g = await api.preset(GLOBAL); S.gl = { raw: g.preset || g.raw || g, eff: g.effective || {} }; } catch (e) { S.gl = null; } } else S.gl = null;
}
let appsSeq = 0; // latest request wins: an older /api/apps answer that lands late (overlapping reloads) must not put a stale count back on a tab
async function loadApps() { const my = ++appsSeq; const list = await api.apps(); if (my !== appsSeq) return; S.apps = list.map((a) => ({ ...a, iconUrl: iconUrl(a.id) })); }
function netDown(e) { if (e instanceof ApiError && e.network) { showBanner(); revertUnsaved(); return true; } return false; }
/** offline: an edit that never reached the disk must not stay on screen. Every field goes back to the value the engine last confirmed. */
function revertUnsaved() {
    S.draft = {};
    const e = eff(); if (!e) return;
    for (const row of [...ctx.rows, ...ctx.drows]) { const fv = e.fields[row.key]; if (fv) row.patch(fv, true); }
    ctx.curve && ctx.curve.update();
}
function readOnly(e) { if (e instanceof ApiError && e.status === 423) { notices.readonly = true; renderNotice(); return true; } return false; }
function fail(e) { if (!netDown(e) && !readOnly(e)) toast({ msg: e.message || "Something went wrong.", tone: "bad" }); }
const ctlLabel = (id) => plainControl(ctlMeta(id)?.label || id);

/* ================================================================== notices (one row, takes layout space above the tab strip), status, safety */
let bannerTimer = 0, bannerCount = 0, offline = false, justRecovered = false, noticeShown = false;
const notices = { compose: null, readonly: false, damaged: null, canary: null, startup: null, external: null };
const editable = () => !offline && !notices.readonly;
function syncInert() {
    const off = !editable();
    const P = $("#panel"), D = $("#drawer");
    if (off || ctx.drawers?.current) P.setAttribute("inert", ""); else P.removeAttribute("inert");
    if (off) D.setAttribute("inert", ""); else D.removeAttribute("inert");
    P.setAttribute("aria-disabled", String(off));
}
/* the notice floats in the free part of the app row; when the tabs leave too little room it spans the row instead (it never moves the layout) */
function placeBanner() {
    const b = $("#banner"); if (!b || b.hidden) return;
    const room = innerWidth - 32; // the notice floats top-centre under the header
    /* measure the notice at its natural width: when the free space cannot hold it whole (text and buttons), it moves to its own row instead of cutting its words */
    b.classList.remove("over"); b.style.setProperty("--room", "9999px");
    const need = b.getBoundingClientRect().width;
    b.classList.toggle("over", need > room); b.style.setProperty("--room", Math.max(0, Math.floor(room)) + "px");
}
addEventListener("resize", () => placeBanner());
/* chips come and go (add / remove app): re-measure the free space so the notice never lands on the app row */
if (window.ResizeObserver) { const ro = new ResizeObserver(() => placeBanner()); const a = document.getElementById("appPick"); if (a) ro.observe(a); }
function renderNotice() {
    const b = $("#banner");
    let n = null;
    if (offline) n = { tone: "bad", ic: "plug", text: "Can\u2019t reach the engine. Edits are paused.", retry: true };
    else if (justRecovered) n = { tone: "ok", ic: "check", text: "Connected. Edits are back." };
    else if (notices.startup) n = { tone: "warn", ic: "triangle-alert", text: notices.startup };
    else if (notices.external) { const k = notices.external.length; n = { tone: "warn", ic: "triangle-alert", text: k === 1 ? "A preset file changed outside the editor and was not applied." : `${k} preset files changed outside the editor and were not applied.`, act: { label: "Review", fn: () => ctx.drawers.open("external", $("#bannerAct")) } }; }
    else if (notices.canary) { const c = notices.canary, l = (c.lost || [])[0]; n = { tone: "warn", ic: "triangle-alert", text: l ? `${appName(l.preset) || l.preset} had ${l.before} assignments and has ${l.after} now. Probably an edit made outside the editor.` : "Some assignments went missing since the last clean run.", tip: "Open backups shows saved copies that still hold them, and restoring one is a single undo step. Dismiss only hides this message; nothing is changed.", act: { label: "Open backups", fn: () => { S.hiBackup = c.backup || null; ctx.drawers.open("io", $("#bannerAct")); } }, act2: { label: "Dismiss", fn: dismissCanary } }; }
    else if (notices.damaged) n = { tone: "warn", ic: "triangle-alert", text: `${appName(notices.damaged) || notices.damaged}\u2019s preset file is damaged. Your last good copy is safe.`, act: { label: "Restore last good", fn: restoreLastGood } };
    else if (notices.compose) n = { tone: "warn", ic: "triangle-alert", text: `The engine couldn\u2019t apply your presets: ${notices.compose}` };
    else if (notices.readonly) n = { tone: "warn", ic: "lock", text: "Read-only: the engine isn\u2019t accepting edits right now." };
    document.body.classList.toggle("offline", offline);
    document.body.classList.toggle("readonly", !offline && notices.readonly);
    syncInert();
    if (!n) {
        if (!noticeShown) return;
        noticeShown = false;
        const a = b.classList.contains("over")
            ? anim(b, [{ opacity: 1, height: "36px", marginTop: "8px" }, { opacity: 0, height: "0px", marginTop: "0px" }], { duration: 165, easing: EASE_IN, fill: "forwards" })
            : anim(b, [{ opacity: 1, transform: "none" }, { opacity: 0, transform: "translateY(-4px)" }], { duration: 165, easing: EASE_IN, fill: "forwards" });
        const done = () => { if (!noticeShown) { b.hidden = true; b.getAnimations().forEach((x) => x.cancel()); } };
        if (a) a.onfinish = done; else done();
        return;
    }
    b.getAnimations().forEach((x) => x.cancel());
    b.hidden = false; b.className = "banner " + n.tone;
    $("#bannerIcon").replaceChildren(icon(n.ic, 20)); $("#bannerText").textContent = n.text; $("#bannerText").setAttribute("data-tip", n.text);
    $("#retryBtn").hidden = !n.retry; $("#bannerCount").hidden = !n.retry;
    const act = $("#bannerAct"); act.hidden = !n.act; if (n.act) { act.textContent = n.act.label; act.onclick = n.act.fn; }
    const act2 = $("#bannerAct2"); act2.hidden = !n.act2; if (n.act2) { act2.textContent = n.act2.label; act2.onclick = n.act2.fn; }
    placeBanner();
    if (!noticeShown) {
        /* floating: fade in place. Own row (narrow): the row opens by height, nothing else moves */
        if (b.classList.contains("over")) anim(b, [{ opacity: 0, height: "0px", marginTop: "0px", marginBottom: "0px" }, { opacity: 1, height: "36px", marginTop: "8px", marginBottom: "0px" }], { duration: 220, easing: EASE_OUT });
        else anim(b, [{ opacity: 0, transform: "translateY(-4px)" }, { opacity: 1, transform: "none" }], { duration: 220, easing: EASE_OUT });
    }
    noticeShown = true;
}
function showBanner() {
    if (offline) return;
    offline = true; flushTimers(true); revertUnsaved();
    renderNotice(); setStatus("bad", "Engine unreachable"); paintArmSeg();
    bannerCount = 5; tickBanner();
}
function tickBanner() {
    clearTimeout(bannerTimer);
    $("#bannerCount").textContent = bannerCount > 0 ? `retrying in ${bannerCount}s` : "retrying\u2026";
    if (bannerCount-- <= 0) { retry(); return; }
    bannerTimer = setTimeout(tickBanner, 1000);
}
async function retry() {
    clearTimeout(bannerTimer);
    try {
        await api.live(); offline = false; S.apiDown = 0; justRecovered = true; queueHist();
        await reloadAll(); renderNotice();
        setTimeout(() => { justRecovered = false; renderNotice(); }, 1500);
    } catch (e) { offline = true; bannerCount = 5; tickBanner(); }
}
/* header pieces repaint only when their content changes: replacing an icon or a text node on every poll / event made the header flicker */
const setText = (el, t) => { if (el && el.textContent !== t) el.textContent = t; };
const setAttr = (el, k, v) => { if (el && el.getAttribute(k) !== v) el.setAttribute(k, v); };
function setStatus(tone, text) { const d = $("#sdot"), c = "sdot " + tone; if (d.className !== c) d.className = c; setText($("#statusText"), text); }
/* what the arm switch really pauses: the engine reports `armCoverage` (keys, mouse, scroll, shell, sidecar-writes). Never claim more than it lists. */
const COVER_ALL = ["keys", "mouse", "scroll", "shell", "sidecar-writes", "finder-tags"];
const COVER_WORD = { keys: "keys", mouse: "mouse", scroll: "scroll", shell: "commands", "finder-tags": "Finder tags", "sidecar-writes": "Resolve writes", sidecar: "Resolve writes", writes: "Resolve writes" };
function coverage() {
    const c = S.live && S.live.armCoverage;
    if (!Array.isArray(c) || !c.length) return null;
    const words = [...new Set(c.map((x) => COVER_WORD[x] || String(x)))];
    const keys = new Set(c.map((x) => (x === "sidecar" || x === "writes" ? "sidecar-writes" : x)));
    const rest = COVER_ALL.filter((x) => !keys.has(x)).map((x) => COVER_WORD[x]);
    return { words, rest, full: !rest.length };
}
const pausedText = () => { const c = coverage(); return c ? `Paused: ${c.words.join(", ")}` : "Disarmed"; };
function paintStatus() {
    if (offline) return;
    const l = S.live; if (!l) return;
    const fm = l.frontmost ? l.frontmost.name : "";
    if (missingPerms(l.permissions).length) setStatus("warn", "Needs access");
    else if (!l.engineRunning) setStatus("warn", "Engine stopped");
    else if (l.sidecar && l.sidecar !== "connected" && fm && /resolve/i.test(fm)) setStatus("warn", "Waiting for Resolve\u2026");
    else setStatus(notices.startup ? "warn" : "ok", "Engine running" + (fm ? ` \u00b7 ${fm} in front` : ""));
}
/** everything the live poll can tell us about safety: armed, permissions, engine divergence, read-only */
function paintArmSeg() {
    const l = S.live || {}, seg = $("#armSeg"), proj = $("#projSeg");
    if (notices.startup) { seg.hidden = false; seg.className = "st-seg off"; seg._sig = "startup"; seg.replaceChildren(h("i", { class: "sdot warn" }), "Disarmed (paused)"); seg.setAttribute("data-tip", "Presets could not be opened, so the deck is paused."); return; }
    const known = typeof l.armed === "boolean" && S.ep.arm !== false && !offline; // offline: the last known state would be a guess
    seg.hidden = !known;
    if (known) {
        const c = coverage(), on = l.armed;
        const cls = "st-seg " + (on ? "on" : "off"); if (seg.className !== cls) seg.className = cls;
        if (seg._sig !== "armed:" + on) { seg._sig = "armed:" + on; seg.replaceChildren(on ? icon("shield-check", 16) : h("i", { class: "sdot warn" }), on ? "Armed" : "Disarmed"); }
        setAttr(seg, "data-tip", on ? (c ? `Armed. Disarming would hold back ${c.words.join(", ")}.` : "Armed: the deck fires.") : `${pausedText()}. Click the status pill to arm.`);
    }
    const name = l.resolveProject && (l.resolveProject.name || l.resolveProject), res = isResolve() || /resolve/i.test((l.frontmost && l.frontmost.name) || "");
    proj.hidden = !(name && typeof name === "string" && res);
    if (!proj.hidden) { proj.textContent = `Project: ${name}`; proj.setAttribute("data-tip", "The DaVinci Resolve project the deck is controlling"); }
}
function paintSafety() {
    const l = S.live || {};
    paintArmSeg();
    const arm = $("#armBtn");
    arm.hidden = typeof l.armed !== "boolean" || S.ep.arm === false;
    if (!arm.hidden) { arm.setAttribute("aria-pressed", String(l.armed)); setText($("#armText"), l.armed ? "Armed" : "Disarmed"); arm.firstChild.firstChild?.setAttribute("href", l.armed ? "#i-shield-check" : "#i-shield-alert");
        const c = coverage();
        setText($("#armText"), l.armed ? "Armed" : c ? "Paused" : "Disarmed");
        arm.setAttribute("data-tip", l.armed ? (c ? `Click to pause: ${c.words.join(", ")}` : "Click to disarm the deck") : c ? `${pausedText()}.${c.full ? " Nothing will fire." : ` Still active: ${c.rest.join(", ")}.`} Click to arm.` : "Disarmed. Click to arm the deck");
        arm.setAttribute("aria-label", `${l.armed ? "Armed" : pausedText()}. ${l.armed ? "Click to disarm" : "Click to arm"}`); }
    const e = l.engine || {};
    const div = (e.appliedSeq != null && e.lastSeq != null && e.appliedSeq < e.lastSeq) || (e.applied != null && e.saved != null && e.applied !== e.saved);
    $("#diverge").hidden = !div;
    const prevC = notices.compose, prevR = notices.readonly;
    notices.compose = e.composeError ? String(e.composeError) : null; notices.readonly = !!e.readonly;
    const prevK = JSON.stringify(notices.canary); notices.canary = e.canary && (e.canary.lost || []).length ? e.canary : null; S.canaryHint = notices.canary && notices.canary.hint ? String(notices.canary.hint) : "";
    if (prevC !== notices.compose || prevR !== notices.readonly || prevK !== JSON.stringify(notices.canary)) renderNotice();
    const miss = missingPerms(l.permissions);
    const pb = $("#permBtn"); pb.hidden = !miss.length; pb.dataset.miss = miss.join(","); if (!lastMiss) lastMiss = new Set(miss);
}
const PERM = {
    accessibility: { name: "Accessibility", pane: "Accessibility", why: "Without it the deck can\u2019t press keys or scroll for you." },
    screenRecording: { name: "Screen Recording", pane: "Screen & System Audio Recording", why: "Without it the deck can\u2019t read what is on screen." },
    automationFinder: { name: "Finder automation", pane: "Automation", why: "Without it Finder tags can\u2019t be set." },
    inputMonitoring: { name: "Input Monitoring", pane: "Input Monitoring", why: "Without it the deck can\u2019t see its own controls." },
    input_monitoring: { name: "Input Monitoring", pane: "Input Monitoring", why: "Without it the deck can\u2019t see its own controls." },
    automation: { name: "Automation", pane: "Automation", why: "Without it the deck can\u2019t drive other apps." },
};
const permBad = (v) => v === false || v === "denied" || v === "missing" || v === "notGranted" || (v && typeof v === "object" && (v.granted === false || v.ok === false));
/** [{key, ok}] for every permission the engine reports (granted ones too, so the popover can show the whole picture) */
function permList(p) {
    if (!p) return [];
    if (Array.isArray(p)) return p.map((x) => ({ key: x.name || x.id, ok: !permBad(x.granted ?? x.ok ?? x.status) })).filter((x) => x.key);
    return Object.entries(p).map(([k, v]) => ({ key: k, ok: !permBad(v), unknown: v === "unknown" }));
}
const missingPerms = (p) => permList(p).filter((x) => !x.ok).map((x) => x.key);
const permInfo = (k) => PERM[k] || { name: k, pane: k, why: "Without it part of the deck can\u2019t work." };
let permPop = null, lastMiss = null;
function permSection() {
    const rows = permList(S.live && S.live.permissions).filter((x) => !x.unknown);
    if (!rows.some((r) => !r.ok)) return null;
    const firstBad = rows.find((r) => !r.ok);
    return h("section", { class: "st-sec" }, h("h4", {}, "Permissions"),
        h("p", {}, "macOS decides this, not 2T5 DECK+. Switch each one below on in System Settings, then come back: this window checks again by itself."),
        ...rows.map((r) => { const pi = permInfo(r.key), t = `System Settings \u203a Privacy & Security \u203a ${pi.pane}`;
            return h("div", { class: "perm-row" + (r.ok ? " ok" : "") },
                h("div", { class: "perm-h" }, h("i", { class: "pdot" + (r.ok ? " ok" : "") }), h("b", {}, pi.name), h("span", { class: "perm-st" }, r.ok ? "Granted" : "Needed")),
                r.ok ? null : h("p", {}, pi.why),
                r.ok ? null : h("div", { class: "copyline" }, h("code", {}, t), h("button", { type: "button", class: "icon-btn xs", "aria-label": `Copy the path to ${pi.name}`, "data-tip": "Copy path", onclick: async () => { try { await navigator.clipboard.writeText(t); toast({ msg: "Path copied", tone: "info", key: "perm" }); } catch (e) { toast({ msg: "Copy isn\u2019t available here. Select the text instead.", tone: "info", key: "perm" }); } } }, icon("copy"))),
                r === firstBad ? h("p", { class: "muted" }, "Switched it on and it still says Needed? macOS can keep a stale grant after an update. Switch 2T5 DECK+ off and on again, then restart it. Nothing here is lost.") : null);
        }));
}
/** re-read the permissions once whenever the window regains focus (the user is back from System Settings) */
let permCheck = 0;
async function recheckPerms() {
    if (offline || performance.now() - permCheck < 1500 || S.ep.live === false) return; permCheck = performance.now();
    try {
        const l = await api.live(); S.live = l; syncLive(l); paintSafety(); paintStatus();
        const now = new Set(missingPerms(l.permissions));
        if (lastMiss) for (const k of lastMiss) if (!now.has(k)) toast({ msg: `${permInfo(k).name} is on. Thanks.`, tone: "ok", key: "perm-ok" });
        lastMiss = now;
        if (permPop) permPop.paint();
    } catch (e) { /* the polls own the offline banner */ }
}
/* "Why this preset?": the current reason, the last switches, and how many focus blips were ignored. Quiet, one click away. */
const REASON = { frontmost: "The frontmost app has its own preset", "editor-focused-kept-last": "This window is in front, so the last real app stays active", "no-preset-global-fallback": "The frontmost app has no preset, so Global applies", "resolve-cascade": "DaVinci Resolve is in front and uses its bank cascade" };
const ago = (t) => { const d = Math.max(0, (Date.now() - t) / 1000); return d < 10 ? "just now" : d < 60 ? `${Math.round(d)} s ago` : d < 3600 ? `${Math.round(d / 60)} min ago` : `${Math.round(d / 3600)} h ago`; };
const KIND_WORD = { switch: "Switch", arm: "Arm", import: "Import", backup: "Backup", restore: "Restore", disarm: "Disarm" };
/* /api/warnings kinds that belong in the readiness list, in plain words (the engine's own text only adds detail) */
const warnCheck = (w) => {
    const k = String(w.kind || ""), a = w.app ? appName(w.app) : "", more = w.message ? cap1(String(w.message).replace(/([a-z])([A-Z])/g, (m, x, y) => x + " " + y.toLowerCase())) : "";
    if (/disarm-marker/.test(k)) return { id: "w-" + k, label: "Started paused", status: "warn", detail: "The deck was left disarmed when the app last closed, so it started paused. Arm it when you are ready.", fix: "" };
    if (/disarm|panic/i.test(k) && w.message) return { id: "w-" + k, label: "Panic key", status: "warn", detail: String(w.message), fix: "" };
    if (k === "external-pending") return { id: "w-" + k, label: "Outside change held back", status: "warn", detail: `${a ? a + ": " : ""}a preset file was changed from outside the editor and was not applied.`, fix: "" };
    if (k === "settings-range") return { id: "w-" + k, label: "Setting out of range", status: "warn", detail: `${a ? a + ": " : ""}a setting was outside its safe range and is not used as saved.${more ? " " + more : ""}`, fix: "" };
    if (k === "seed-fallback") return { id: "w-" + k, label: "Standard presets in use", status: "warn", detail: `The saved presets could not be used, so the built-in standard ${a ? "preset for " + a : "presets"} is running.`, fix: "" };
    return null;
};
const cap1 = (t) => (t ? t.charAt(0).toUpperCase() + t.slice(1) : t);
function preflightSection() {
    const have = new Set(((S.pre && S.pre.checks) || []).map((c) => c.id));
    const extra = (S.warns || []).map(warnCheck).filter((c) => c && !(c.id === "w-external-pending" && have.has("pendingExternal")));
    /* the deck is paused for outside changes (the banner says so): this list must say so too, with the same Review link, never "Everything checks out" */
    const held = (S.pending || []).length, heldRow = held && !extra.some((c) => c.id === "w-external-pending") && !((S.pre && S.pre.checks) || []).some((c) => c.id === "pendingExternal" && c.status !== "ok")
        ? [{ id: "pendingExternal", label: "Outside change held back", status: "warn", detail: held === 1 ? "A preset file changed outside the editor and was not applied. The deck is paused until you review it." : `${held} preset files changed outside the editor and were not applied. The deck is paused until you review them.`, fix: "" }] : [];
    /* a missing macOS permission is a thing that needs attention too: the chip says Needs access, so this list must not say "Everything checks out" */
    const permMiss = S.live ? missingPerms(S.live.permissions) : [], permRow = permMiss.length && !((S.pre && S.pre.checks) || []).some((c) => c.status !== "ok" && /perm|access/i.test(c.id))
        ? [{ id: "perm-missing", label: "Permissions", status: "warn", detail: `${permMiss.map((k) => permInfo(k).name).join(" and ")} ${permMiss.length === 1 ? "is" : "are"} still needed. See Permissions below.`, fix: "" }] : [];
    const pre = S.pre && { ...S.pre, checks: [...(S.pre.checks || []).filter((c) => !(heldRow.length && c.id === "pendingExternal")), ...permRow, ...extra, ...heldRow] }; if (!pre || !(pre.checks || []).length) return null;
    const bad = pre.checks.filter((c) => c.status === "warn" || c.status === "fail");
    const icn = (c) => (c.status === "ok" ? icon("check", 16, "pf-ok") : c.status === "unknown" ? icon("circle-dashed", 16, "pf-un") : icon("triangle-alert", 16, "pf-bad"));
    const row = (c) => h("div", { class: "pf-check " + c.status },
        icn(c), h("div", { class: "pf-txt" }, h("b", {}, c.label), c.detail ? h("span", {}, c.detail) : null,
            (c.id === "pendingExternal" || c.id === "w-external-pending") && c.status !== "ok" && (S.pending || []).length ? h("button", { type: "button", class: "btn sm pf-review", onclick: (e) => ctx.drawers.open("external", e.currentTarget) }, "Review") : null,
            c.fix && c.status !== "ok" ? h("div", { class: "copyline" }, h("code", {}, c.fix), h("button", { type: "button", class: "icon-btn xs", "aria-label": `Copy the fix for ${c.label}`, "data-tip": "Copy", onclick: async () => { try { await navigator.clipboard.writeText(c.fix); toast({ msg: "Copied", tone: "info", key: "pf" }); } catch (e) { toast({ msg: "Copy isn\u2019t available here. Select the text instead.", tone: "info", key: "pf" }); } } }, icon("copy"))) : null));
    return h("section", { class: "st-sec" }, h("h4", {}, "Ready to go?"),
        h("p", { class: bad.length ? "" : "muted" }, bad.length ? `${bad.length} thing${bad.length === 1 ? " needs" : "s need"} attention before the deck is dependable.` : "Everything checks out."),
        ...bad.map(row),
        h("details", { class: "pf-all" }, h("summary", {}, `All ${pre.checks.length} checks`), ...pre.checks.filter((c) => !bad.includes(c)).map(row)));
}
function activitySection(list) {
    if (!list || !list.length) return null;
    return h("section", { class: "st-sec" }, h("h4", {}, "Recent activity"),
        h("div", { class: "why-log", role: "list", "aria-label": "Recent activity" }, ...list.slice(0, 8).map((x) => h("div", { class: "why-row", role: "listitem" }, h("span", { class: "why-t mono" }, ago(x.t)), h("span", { class: "why-n" }, h("b", {}, (KIND_WORD[x.kind] || x.kind || "") + (x.text ? ": " : "")), x.kind === "switch" ? (x.detail && x.detail.to ? switchWords(x.detail) : String(x.text || "").replace(/[\w.-]+\.[\w.-]+/g, (id) => appName(id) || id).replace("->", "\u2192")) : plain(String(x.text || "")))))));
}
async function loadPreflight() {
    if (S.ep.preflight === false) return null;
    try { S.pre = await api.preflight(); S.ep.preflight = true; return S.pre; } catch (e) { if (e.status === 404) S.ep.preflight = false; return null; }
}
function openWhy(anchor) {
    const body = h("div", { class: "why-pop" });
    const paint = () => {
        const w = (S.live && S.live.why) || {}, log = (w.switchLog || []).slice(0, 8), blips = w.ignoredBlips || 0, l = S.live || {}, c = coverage();
        const armed = l.armed !== false;
        const sw = switchCtl({ checked: armed, live: true, label: "Deck armed", text: armed ? "Armed" : "Paused", onChange: async () => { await toggleArm(); paint(); } });
        const atStart = l.armed === false && l.armedAtStart === false;
        const startNote = atStart ? h("p", { class: "start-note" }, l.startReason === "external-changes" ? "Started disarmed because preset files changed while the app was closed. Review them, then arm when ready." : /first/i.test(String(l.startReason || "first-run")) ? "Started disarmed (first run): arm when ready." : `Started disarmed${l.startReason ? ` (${String(l.startReason).replace(/[-_]/g, " ")})` : ""}: arm when ready.`) : null;
        const armBtn = atStart ? h("button", { type: "button", class: "btn primary sm", onclick: async () => { await toggleArm(); paint(); } }, icon("shield-check"), "Arm the deck") : null;
        const deck = typeof l.armed === "boolean" && S.ep.arm !== false ? h("section", { class: "st-sec" }, h("h4", {}, "Deck"), startNote, h("div", { class: "st-row" }, h("span", {}, armed ? (c ? `Armed. Pausing would hold back ${c.words.join(", ")}.` : "Armed: the deck fires.") : `${pausedText()}.${c && !c.full ? ` Still active: ${c.rest.join(", ")}.` : c ? " Nothing will fire." : ""}`), armBtn || sw.el)) : null;
        const pfs = preflightSection(), pfBad = !!(S.pre && (S.pre.checks || []).some((c) => c.status === "warn" || c.status === "fail"));
        body.replaceChildren(...(pfBad && pfs ? [pfs] : []), h("section", { class: "st-sec" }, h("h4", {}, "Why this preset?"),
            h("p", {}, REASON[w.reason] || "The engine picks the preset of the frontmost app."),
            w.activeId ? h("div", { class: "why-now" }, appTile(appInfo(w.activeId) || { id: w.activeId, name: appName(w.activeId) }, { size: 16, url: iconUrl(w.activeId) }), h("b", {}, appName(w.activeId))) : null,
            log.length ? h("div", { class: "why-log", role: "list", "aria-label": "Recent switches" }, ...log.map((x) => h("div", { class: "why-row", role: "listitem" }, h("span", { class: "why-t mono" }, ago(x.t)), h("span", { class: "why-n" }, switchWords(x))))) : h("p", { class: "muted" }, "No switches yet."),
            blips ? h("p", { class: "muted" }, `${blips} focus blip${blips === 1 ? "" : "s"} ignored`) : null),
            ...[deck, pfBad ? null : pfs, depsLink(), activitySection(S.activity), permSection()].filter(Boolean));
    };
    paint();
    loadPreflight().then(() => { if (permPop) permPop.paint(); });
    if (S.ep.eventslog !== false) api.eventsLog().then((r) => { S.ep.eventslog = true; S.activity = (r.events || []).slice().sort((a, b) => b.t - a.t); if (permPop) permPop.paint(); }).catch((e) => { if (e.status === 404) S.ep.eventslog = false; });
    const pop = popover({ anchor, el: body, minWidth: 340, maxHeight: 580, placement: "bottom-start", onClose: () => { permPop = null; } });
    permPop = { pop, paint };
}
async function toggleArm() {
    const next = !(S.live?.armed);
    try { await api.arm(next); S.live = { ...S.live, armed: next }; paintSafety(); paintStatus(); toast({ msg: next ? "Armed: the deck is live" : pausedText(), tone: "info", key: "arm" }); }
    catch (e) { if (e.status === 404) { $("#armBtn").hidden = true; } else fail(e); }
}
/* templates that need something outside the deck (e.g. Blender: Emulate Numpad): a calm, dismissible note for that app */
async function loadTemplates() {
    if (S.ep.templates === false) return;
    try { const r = await api.templates(); S.ep.templates = true; S.tpls = Array.isArray(r) ? r : r.templates || []; } catch (e) { if (e.status === 404) S.ep.templates = false; }
}
const reqKey = (app, id) => `req.${app}.${id}`;
function requirementsFor(app) {
    const t = (S.tpls || []).filter((x) => (x.bundleId || x.bundle || x.appId) === app && (x.requires || []).length);
    const out = []; for (const x of t) for (const r of x.requires) if (!store.get(reqKey(app, r.id), false) && !out.some((o) => o.id === r.id)) out.push(r);
    return out;
}
/* ================================================================== requirements: first run, "comes back", inline "Needs X" (rules in depsLogic.js, list in docs/dependencies.json) */
let depsBusy = false, depsErr = "", lastDrawerName = null, needsSig = "";
const DEPS_KEY = "deps.dismissed";
async function loadDeps(fresh) {
    if (S.ep.deps === false || offline) return null;
    if (fresh) { depsBusy = true; if (ctx.drawers) ctx.drawers.refreshNamed("setup"); }
    try { S.deps = await api.dependencies(fresh); S.ep.deps = true; depsErr = ""; } catch (e) { if (e.status === 404) S.ep.deps = false; else depsErr = "Could not check this Mac. Try again in a moment."; }
    depsBusy = false;
    if (ctx.drawers) ctx.drawers.refreshNamed("setup");
    paintNeeds(); maybeOpenDeps();
    return S.deps;
}
ctx.depsState = () => ({ items: S.deps && S.deps.items, error: depsErr, busy: depsBusy });
ctx.depsLoad = (f) => loadDeps(f);
ctx.needsNote = (feature) => { const hs = S.deps ? needsFor(S.deps.items, feature) : []; return hs.length ? h("div", { class: "req-note", role: "note" }, ...hs.map(hintRow)) : null; };
ctx.depsFix = async (item, fix) => {
    if (fix.type === "copy") {
        try { await navigator.clipboard.writeText(fix.text); toast({ msg: "Copied. Paste it into Terminal.", tone: "info", key: "dep" }); } catch (e) { toast({ msg: "Copy isn’t available here. Select the text instead.", tone: "info", key: "dep" }); }
        return;
    }
    try { await api.openSettings(fix.pane); toast({ msg: "System Settings is open. Switch it on, then come back: this checks again by itself.", tone: "info", key: "dep" }); }
    catch (e) { toast({ msg: `Could not open System Settings. ${item.howToGet}`, tone: "info", key: "dep" }); }
};
function openSetup(from) { ctx.drawers.open("setup", from || $("#status")); loadDeps(true); }
function maybeOpenDeps() {
    if (!S.deps || !ctx.drawers || document.body.classList.contains("loading") || $(".tour")) return;
    const busy = !!ctx.drawers.current || ctx.drawers.closing;
    const prev = store.get(DEPS_KEY, {});
    const d = decide({ items: S.deps.items, dismissed: prev, firstRun: !!S.deps.firstRun, busy });
    if (JSON.stringify(d.dismissed) !== JSON.stringify(prev)) store.set(DEPS_KEY, d.dismissed);
    if (d.open) { S.depsAuto = true; ctx.drawers.open("setup", $("#status")); }
}
/** the person closed the Requirements panel (or opened another drawer over it): stay quiet about what is missing right now, and tell the app this Mac has seen it */
function setupClosed() {
    if (!S.deps) return;
    store.set(DEPS_KEY, afterDismiss(S.deps.items, store.get(DEPS_KEY, {})));
    S.deps.firstRun = false;
    api.setupDone().then(() => loadDeps(false)).catch(() => {});
}
const depHints = (app) => (S.deps ? needsFor(S.deps.items, String(app).includes("DaVinciResolve") ? "resolve" : app === "com.apple.finder" ? "finder-tags" : "-") : []);
const hintRow = (hnt) => h("div", { class: "req-row need-row", "data-dep": hnt.id }, icon("info", 16, "c-ic"), h("div", { class: "req-t" }, h("b", {}, hnt.text), h("span", {}, hnt.item.without)),
    h("button", { type: "button", class: "btn sm", onclick: (e) => openSetup(e.currentTarget) }, "How to fix"));
/** repaint the places that carry a "Needs X" hint when the set of hints changes */
function paintNeeds() {
    const sig = S.deps ? JSON.stringify(["resolve", "finder-tags"].map((f) => needsFor(S.deps.items, f).map((x) => x.id))) : "";
    if (sig === needsSig) return; needsSig = sig;
    try { const ea = $("#emptyApp"); if (ea) ea.dataset.app = ""; if (S.pr) { renderPanel(false); paintEmptyApp(); } } catch (e) { /* the page is still starting */ }
}
setInterval(() => { if (!document.hidden) loadDeps(false); }, 15000);
let depsFocusAt = 0;
addEventListener("focus", () => { if (performance.now() - depsFocusAt < 3000) return; depsFocusAt = performance.now(); loadDeps(!!(S.deps && (S.deps.requiredMissing || []).length) || (ctx.drawers && ctx.drawers.current === "setup")); });
const depsLink = () => {
    if (!S.deps || S.ep.deps === false) return null;
    const s = summarizeDeps(S.deps.items);
    return h("section", { class: "st-sec" }, h("h4", {}, "Requirements"), h("p", { class: s.tone === "ok" ? "muted" : "" }, s.text + "."),
        h("button", { type: "button", class: "btn sm", onclick: (e) => { if (permPop) permPop.pop.close({ noFocus: true }); openSetup($("#status")); } }, icon("plug", 16), "See what the deck needs"));
};
S.reqNote = (app) => {
    const rs = requirementsFor(app), hints = depHints(app); if (!rs.length && !hints.length) return null;
    return h("div", { class: "req-note", role: "note" }, ...hints.map(hintRow), ...rs.map((r) => h("div", { class: "req-row" }, icon("info", 16, "c-ic"), h("div", { class: "req-t" }, h("b", {}, r.label || r.id), r.help ? h("span", {}, r.help) : null),
        h("button", { type: "button", class: "icon-btn xs", "aria-label": `Dismiss: ${r.label || r.id}`, "data-tip": "Dismiss for this app", onclick: () => { store.set(reqKey(app, r.id), true); const ea = $("#emptyApp"); if (ea) ea.dataset.app = ""; renderPanel(false); paintEmptyApp(); } }, icon("x")))));
};
async function loadWarnings() {
    if (S.ep.warnings === false) return;
    try {
        const r = await api.warnings(); S.ep.warnings = true; S.warns = r.warnings || [];
        const bad = (r.warnings || []).find((w) => w.kind === "unreadable-file");
        const id = bad ? (bad.app || String(bad.field || "").replace(/\.json$/, "")) : null;
        if (id !== notices.damaged) { notices.damaged = id; renderNotice(); }
    } catch (e) { if (e.status === 404) S.ep.warnings = false; }
}
async function loadPending() {
    if (S.ep.pending === false || offline) return;
    let list;
    try { const r = await api.pendingExternal(); S.ep.pending = true; list = Array.isArray(r && r.pending) ? r.pending : []; } catch (e) { if (e.status === 404) S.ep.pending = false; return; }
    const sig = (l) => JSON.stringify((l || []).map((x) => x.id));
    const changed = sig(list) !== sig(S.pending);
    S.pending = list; notices.external = list.length ? list : null;
    if (changed) { renderNotice(); if (picker) picker.render(); ctx.drawers && ctx.drawers.refreshNamed && ctx.drawers.refreshNamed("external"); if (S.pre) loadPreflight(); }
}
setInterval(() => { if (!document.hidden) loadPending(); }, 8000);
async function dismissCanary() {
    try { await api.canaryAck(); notices.canary = null; renderNotice(); } catch (e) { if (e.status === 404) { notices.canary = null; renderNotice(); } else fail(e); }
}
async function restoreLastGood() {
    const id = notices.damaged; if (!id) return;
    try { await api.restoreLastGood(id); notices.damaged = null; renderNotice(); await reloadAll(); toast({ msg: `${appName(id) || id} restored from the last good copy`, undo: () => doUndo() }); }
    catch (e) { if (e.status === 404) toast({ msg: "This engine can\u2019t restore automatically yet. The backup is in Import, export, backup.", tone: "info" }); else fail(e); }
}

/* ================================================================== app picker (one dropdown instead of a tab per app) */
/** card copy that ends cleanly: whole sentences while they fit, else a word boundary and an ellipsis (never mid-sentence, never mid-word) */
function teaser(text, max = 84) {
    const t = String(text || "").trim(); if (t.length <= max) return t;
    const sent = t.match(/[^.!?]+[.!?]+(?:\s+|$)/g) || [];
    let out = ""; for (const x of sent) { if ((out + x).trim().length > max) break; out += x; }
    if (out.trim()) return out.trim();
    const cut = t.slice(0, max + 1), sp = cut.lastIndexOf(" ");
    return t.slice(0, sp > 24 ? sp : max).replace(/[\s,;:.\u2013-]+$/, "") + "\u2026";
}
/** one calm line per standard preset; the template's own notes stay in the tooltip */
const TEMPLATE_HINTS = {
    "adobe-after-effects": "Wheel steps frames, Exposure jumps keyframes. Easy Ease is F9; macOS may take it.",
    "adobe-illustrator": "Arrow buttons arrange objects: Up to front, Down to back, Left and Right one step.",
    "adobe-lightroom": "Develop knobs stay unassigned: Lightroom has no slider shortcuts.",
    "adobe-lightroom-classic": "Slider knobs: Temperature picks a slider, Exposure and Contrast nudge it.",
    "adobe-photoshop": "Brush size and hardness on the knobs; the wheel steps through history.",
    "adobe-premiere-pro": "Wheel steps frames, Exposure shuttles with J and L. Lift and Extract need a hold.",
    "blender": "Turn on Emulate Numpad in Blender\u2019s Input preferences so the view keys work.",
    "design-preview": "Page stepping and zoom for PDFs and CAD views; add CAD apps separately.",
    "google-chrome-comfyui": "Queue and cancel are left unbound on purpose; assign them yourself.",
};
const templateHint = (t) => TEMPLATE_HINTS[t.id || t.template] || (t.notes ? teaser(t.notes, 90) : "");
let picker = null;
const renderAppPicker = () => picker && picker.render();
/** Mouse calibration: which apps have mouse targets, and how many need attention (404 = this host has none). */
let calSeq = 0;
async function loadCal() {
    const my = ++calSeq; let r;
    try { r = await api.calibApps(); } catch (e) { r = null; }
    if (my !== calSeq) return;
    ctx.calApps = {}; for (const x of (r && r.apps) || []) ctx.calApps[x.id] = x;
    for (const x of S.apps) x.cal = ctx.calApps[x.id] || null;
    renderAppPicker(); paintAppActs();
    if (ctx.calApps[S.app]) { try { ctx.calOv = await api.calib(S.app); } catch (e) { ctx.calOv = null; } } else ctx.calOv = null;
    if (my === calSeq && S.control) { const hn = $("#panel .cal-chip"); if (hn || ctx.calChip(S.bank, S.control)) renderPanel(false); }
}
/** inspector chip for a control that uses a mouse target: its worst status */
ctx.calChip = () => null; /* no per-control calibration chip either: the status is shown only in Mouse calibration */
ctx._calChipUnused = (bank, control) => {
    const ov = ctx.calOv; if (!ov || ov.app.id !== S.app) return null;
    const ts = (ov.targets || []).filter((t) => (t.usedBy || []).some((u) => u.control === control && (u.bank === bank || bank.startsWith(u.bank))));
    if (!ts.length) return null;
    const worst = ["needs_calibration", "unknown", "stale", "legacy", "imported", "calibrated"].find((s) => ts.some((t) => t.status === s));
    return { status: worst, key: ts.find((t) => t.status === worst).key };
};
/** Standard preset (apps made from a shipped template): the name sits quietly beside the app picker, with Compare. */
async function loadStd() {
    const id = S.app; S.std = null; paintStd();
    if (!id || isGlobalApp()) return;
    try { const d = await api.diff(id); if (id !== S.app) return; S.std = d && d.seedSource === "template" ? { app: id, id: d.templateId, name: d.templateName || d.name, counts: d.counts } : null; } catch (e) { S.std = null; }
    paintStd();
}
function paintStd() {
    const n = $("#stdNote"), b = $("#stdCompare"); if (!n || !b) return;
    const on = !!(S.std && S.std.app === S.app);
    n.hidden = b.hidden = !on;
    if (on) { n.textContent = `Standard preset: ${S.std.name}`; n.setAttribute("data-tip", `This app started from the ${S.std.name} standard preset`); }
}
function compareStd(anchor) { S.autoCompare = true; ctx.drawers.open("profile", anchor); }
function openAppMenu(anchor) {
    openMenu({ anchor, minWidth: 240, ariaLabel: "App menu", menu: true, groups: [{ items: [...(ctx.calApps && ctx.calApps[S.app] ? [{ value: "cal", label: "Mouse calibration\u2026", icon: "mouse-pointer-click" }] : []), ...(S.std && S.std.app === S.app ? [{ value: "std", label: `Standard preset: ${S.std.name}`, icon: "shield-check", disabled: true }, { value: "cmp", label: "Compare with standard…", icon: "git-compare" }] : []), { value: "profile", label: "App profile…", icon: "app-window" }, ...(S.ep.io !== false ? [{ value: "io", label: "Import, export, backup…", icon: "archive" }] : [])] }], onPick: (v) => (v === "cmp" ? compareStd(anchor) : v === "cal" ? ctx.drawers.open("calibration", anchor) : ctx.drawers.open(v, anchor)) });
}

async function selectApp(id, opts = {}) {
    const prev = S.app;
    if (!opts.auto) S.follow = !(S.live && S.live.activeApp) || id === S.live.activeApp; // a pick of any other app pins the editor to it until "Follow front app" is clicked
    if (id !== prev) clearToasts();
    S.app = id; S.control = null; S.pending = null;
    try { await loadPreset(); } catch (e) { S.app = prev; return fail(e); }
    if (S.flags && S.flags.lens) await loadDiff();
    const bl = banks();
    S.bank = isResolve() && bl.includes("col") ? "col" : bl.find((b) => bankCount(b) > 0) || bl[0];
    renderAppPicker(); loadStd(); loadCal();
    renderLayerState(); renderDeck(true); renderPanel(true);
    ctx.drawers.rebuild();
    paintFollow();
}

/** The editor follows the engine's active preset (the frontmost app's, else Global, Resolve's when Resolve is in front). The editor window itself never counts as in front: /api/live keeps the last real app. A manual pick pins it; the chip in the top bar says so and one click follows again. Waits while something is being edited. */
let followBusy = false;
async function followFront() {
    paintFollow();
    followLayer();
    const l = S.live, t = l && l.activeApp;
    if (followBusy || S.follow === false || offline || !t || t === S.app || !S.apps.some((a) => a.id === t) || busyEditing()) return;
    followBusy = true;
    try { clog(`follow: front=${(l.frontmost && l.frontmost.name) || "-"} active=${t}; editor ${S.app} -> ${t}`); await selectApp(t, { auto: true }); } finally { followBusy = false; }
}
/** The editor follows the engine's active layer (L1 / L2 / L3, pressed on the deck) the same way it follows the preset: at once on the press, paused while something is being edited, and a manual page or layer pick holds only until the deck changes layer again. When the app has no bank for that layer the layer keys still show it. */
function followLayer() {
    const n = S.live && S.live.layer;
    if (!(n >= 1 && n <= 3) || offline || !S.pr) return;
    if (S.layerPinAt != null) { if (n === S.layerPinAt) return; S.layerPinAt = null; }
    if (busyEditing()) return;
    const cur = parseBank(S.bank), page = pageFor(rootOf(cur.page), n);
    if (layerOf(cur.page) === n && !S.layerShown) return;
    if (pages().includes(page)) { switchBank(composeBank(page, modsFor(page).includes(cur.mod) ? cur.mod : null), true); return; }
    if (S.layerShown !== n && layerOf(cur.page) !== n) { S.layerShown = n; paintHeld(); }
    else if (S.layerShown && layerOf(cur.page) === n) { S.layerShown = null; paintHeld(); }
}
function paintFollow() {
    const b = $("#followBtn"); if (!b) return;
    const t = S.live && S.live.activeApp, pinned = S.follow === false && !!t && t !== S.app;
    b.hidden = !pinned; if (!pinned) return;
    setText($("#followText"), `Pinned \u00b7 follow ${appName(t)}`);
    b.setAttribute("data-tip", `You are editing ${appName(S.app)}, but the deck is using ${appName(t)} right now. Click to follow the app in front again.`);
}

/* ---- add app popover (standard presets, running apps, bundle id; the server decides what is valid) */
function openAddApp(btn) {
    const el = h("div", { class: "addpop" });
    let running = [], templates = []; const have = () => new Set(S.apps.map((a) => a.id));
    const err = h("p", { class: "fr-err", role: "alert", hidden: true });
    const showErr = (m) => { err.hidden = !m; err.replaceChildren(m ? icon("triangle-alert") : "", m || ""); };
    const doAdd = async (fn) => { try { const known = new Set(S.apps.map((x) => x.id)); const r = await fn(); await loadApps(); renderAppPicker(); const id = r.id || r.app?.id; pop.close({ noFocus: true }); const nm = r.name || r.app?.name || (id && appName(id)) || "App";
        if (id) await selectApp(id); // switch first: switching clears the old app's toast
        if (id && known.has(id)) toast({ msg: `${nm} is already in your apps: nothing changed`, tone: "info" }); // nothing was added, so there is nothing to undo
        else toast({ msg: `${nm} added${r.controls ? ` with ${r.controls} controls` : ""}`, undo: () => doUndo() }); } catch (e) { if (!netDown(e)) showErr(e.message); } };
    const capture = h("button", { type: "button", class: "btn block", onclick: () => doAdd(() => api.capture()) }, icon("plus"), "Capture current app");
    const tplBox = h("div", { class: "tpl-grid" }), tplSec = h("div", { class: "addsec", hidden: true }, h("div", { class: "pop-sep" }, "Standard presets"), tplBox);
    const filter = textField({ value: "", placeholder: "Filter running apps", label: "Filter running apps", onInput: () => paintRun() });
    const runList = h("div", { class: "run-list" });
    const runSec = h("div", { class: "addsec", hidden: true });
    runSec.append(h("div", { class: "pop-sep" }, "or pick a running app"), filter.el, runList);
    const id = textField({ value: "", placeholder: "com.vendor.App", label: "App ID (advanced)", mono: true, onInput: () => showErr(null) });
    const nm = textField({ value: "", placeholder: "Name (optional)", label: "App name", onInput: () => {} });
    const add = h("button", { type: "button", class: "btn", onclick: () => {
        const v = id.input.value.trim();
        if (!v) { showErr("Enter a bundle id, for example com.vendor.App."); id.input.focus(); return; }
        doAdd(() => api.addApp(v, nm.input.value.trim() || undefined));
    } }, "Add");
    function paintTpl() {
        tplSec.hidden = !templates.length; tplBox.replaceChildren();
        const have2 = (t) => t.exists || ((t.bundleId || t.bundle || t.appId) && have().has(t.bundleId || t.bundle || t.appId));
        for (const t of [...templates].sort((a, b) => (have2(a) ? 1 : 0) - (have2(b) ? 1 : 0))) {
            const bid = t.bundleId || t.bundle || t.appId, added = have2(t);
            tplBox.append(h("button", { type: "button", class: "tpl" + (added ? " added" : ""), disabled: !!added, onclick: () => doAdd(() => api.fromTemplate(t.id || t.template, bid)) },
                appTile({ id: bid || t.id, name: t.name || t.id }, { size: 32, url: iconUrl(bid || t.id) }),
                h("span", { class: "tpl-t" }, h("span", {}, t.name || t.label || t.id), t.installed === false ? h("span", { class: "tpl-ni" }, "Not installed") : null, (() => { const full = t.summary || t.description || bid || ""; const tx = added ? "Already added" : teaser(full); return h("small", tx !== full && !added ? { "data-tip": full } : {}, tx); })(), (!added && templateHint(t) ? h("small", { class: "tpl-note", ...(t.notes ? { "data-tip": t.notes } : {}) }, icon("info", 16, "c-ic"), h("span", {}, templateHint(t))) : null), ...(t.requires || []).map((r) => h("span", { class: "chip req-chip", "data-tip": r.help || "" }, icon("info", 16, "c-ic"), h("span", {}, r.label || r.id))))));
        }
    }
    function paintRun() {
        const f = filter.input.value.toLowerCase(); runList.replaceChildren();
        const items = running.filter((r) => !f || (r.name + r.id).toLowerCase().includes(f));
        runSec.hidden = !running.length;
        if (!items.length) runList.append(h("div", { class: "muted pad" }, "No matching running apps."));
        for (const r of items) {
            const added = have().has(r.id);
            runList.append(h("button", { type: "button", class: "m-row", disabled: added, onclick: () => doAdd(() => api.addApp(r.id, r.name)) }, appTile({ id: r.id, name: r.name }, { size: 32 }), h("span", { class: "m-txt" }, h("span", { class: "m-name" }, r.name), h("span", { class: "m-sub" }, added ? "Already added" : r.id))));
        }
    }
    const manual = h("div", { class: "manual", hidden: true }, id.el, nm.el, add);
    manual.addEventListener("keydown", (e) => { if (e.key === "Enter" && e.target.tagName === "INPUT" && !e.isComposing) { e.preventDefault(); add.click(); } }); // Enter in the App ID or Name field adds, like the button
    const disc = h("button", { type: "button", class: "disc-row", "aria-expanded": "false", onclick: () => { const on = disc.getAttribute("aria-expanded") !== "true"; disc.setAttribute("aria-expanded", String(on)); manual.hidden = !on; if (on) id.input.focus(); } }, h("span", {}, "Type an App ID (advanced)"), icon("chevron-down", 16, "disc-chev"));
    el.append(h("div", { class: "addpop-scroll" }, capture, tplSec, runSec),
        h("div", { class: "addpop-foot" }, disc, manual, err));
    const pop = popover({ anchor: btn, el, minWidth: 320, maxHeight: 560, placement: "bottom-start", gap: 8, caret: true });
    const sc0 = $(".addpop-scroll", el), fade = () => el.classList.toggle("more", sc0.scrollTop + sc0.clientHeight < sc0.scrollHeight - 4); sc0.addEventListener("scroll", fade, { passive: true }); setTimeout(fade, 60); new ResizeObserver(fade).observe(sc0);
    api.running().then((r) => { running = r; paintRun(); }).catch(() => { running = []; paintRun(); });
    if (S.ep.templates !== false) api.templates().then((r) => { S.ep.templates = true; templates = Array.isArray(r) ? r : r.templates || []; paintTpl(); }).catch((e) => { if (e.status === 404) S.ep.templates = false; });
    capture.focus();
}

/* ================================================================== pages and held keys, chosen on the deck picture; the title over the deck says where you are */
/** A bank is a page plus the key held. Pages (Col / Col 2 / Edit) are chosen by the deck's Switch layer keys; held keys by the modifier keys. Both latch on the picture. */
const pageLabel = (p) => bankLabel(p);
/** Which deck keys hold a layer. Read from the preset (controls whose action is Held key on the page's base layer), plus the standard key for any layer the page has but no key claims yet. Only ONE is latched at a time: the latch is the bank itself, so a second latched key later is a change to composeBank, not to this UI. */
function holdKeys() {
    const page = parseBank(S.bank).page, m = new Map(), base = S.pr?.eff?.[composeBank(page, null)] || {}, have = modsFor(page);
    for (const [id, e] of Object.entries(base)) { const mod = e?.action === "generic.modifierHold" && e.fields?.modifier?.value; if (mod && have.includes(mod) && HOT[id]) m.set(id, mod); }
    for (const x of MODS) if (have.includes(x.id) && ![...m.values()].includes(x.id) && HOT[x.key]) m.set(x.key, x.id);
    return m;
}
/** the deck keys that switch the page: controls whose action is Switch layer, with the layer (1-3) they pick */
function pageKeys() {
    const m = new Map(), page = parseBank(S.bank).page, eff0 = S.pr?.eff || {};
    const base = eff0[composeBank(page, null)] || eff0[composeBank(pageFor(rootOf(page), 1), null)] || {};
    for (const [id, e] of Object.entries(base)) { const n = e?.action === "generic.layer" && Number(e.fields?.layer?.value); if (n >= 1 && n <= 3 && HOT[id]) m.set(id, n); }
    return m;
}
/** pages no deck key reaches from here (another Resolve page, a layer without a key): only then the title offers a menu */
function offKeyPages() {
    const cur = parseBank(S.bank).page, root = rootOf(cur), n = new Set(pageKeys().values());
    return pages().filter((p) => p !== cur && !(rootOf(p) === root && n.has(layerOf(p))));
}
const heldMod = () => parseBank(S.bank).mod;
let titleSig = "";
function renderLayerState() { paintHeld(); paintLayerTitle(); }
/** the title over the deck: the page, with the held key on top ("Col page \u00B7 Fn layer"). Base state reads as cleanly as a latched one, so it never appears from nowhere; content swaps with a short fade. */
function paintLayerTitle() {
    const el = $("#layerTitle"); if (!el) return;
    if (!S.pr) { el.replaceChildren(); titleSig = ""; return; }
    const cur = parseBank(S.bank), m = MODS.find((x) => x.id === cur.mod), n = bankAssigned(S.bank), cv = S.cov && S.cov.bank === S.bank ? S.cov : null;
    const count = cv ? `${cv.set} of ${cv.total} controls assigned` : `${n} control${n === 1 ? "" : "s"} assigned`;
    const off = offKeyPages(), mn = sliderModeName(), sig = S.app + "|" + S.bank + "|" + count + "|" + off.join(",") + "|" + (mn || "") + (S.customOn ? "|custom" : ""); if (sig === titleSig) return;
    const swap = titleSig.split("|").slice(0, 2).join("|") !== sig.split("|").slice(0, 2).join("|"); titleSig = sig;
    el.classList.toggle("on", !!m);
    const dead = cv && cv.dead.length ? cv.dead : null;
    const cnt = dead ? h("button", { type: "button", class: "lt-cnt", "data-tip": "Show the unassigned controls", onclick: (e) => openDead(e.currentTarget, dead) }, count) : h("span", {}, count);
    const label = `${pageLabel(cur.page)} page` + (mn ? ` \u00B7 ${mn}` : "") + (S.customOn ? " \u00B7 Custom Mode on" : "") + (m ? ` \u00B7 ${m.name || m.label} layer` : "");
    const ttl = off.length ? h("button", { type: "button", class: "lt-t lt-pg", "aria-haspopup": "menu", "aria-expanded": "false", "data-tip": "Go to another page", onclick: (e) => openPageMenu(e.currentTarget) }, h("span", {}, label), icon("chevron-down", 16, "lt-chev")) : h("div", { class: "lt-t" }, label);
    el.replaceChildren(...[
        m ? h("kbd", { class: "cap lt-cap", "aria-hidden": "true" }, modGlyph[m.id]) : h("span", { class: "lay-ic", "aria-hidden": "true" }, icon("layers", 20)),
        h("div", { class: "lt-txt" }, ttl, h("div", { class: "lt-s" }, `${appName(S.app)} \u00B7 `, cnt)),
        m ? h("button", { type: "button", class: "lt-x", "data-tip": "Leave this layer", "aria-label": `Leave the ${m.name || m.label} layer`, onclick: () => switchHold(null) }, icon("x", 16), h("kbd", { class: "cap sm" }, "Esc")) : null].filter(Boolean));
    if (swap && !reduced()) anim(el, [{ opacity: 0.2 }, { opacity: 1 }], { duration: 120, easing: EASE_OUT });
}
/** the fallback page menu: only when some page has no deck key */
function openPageMenu(anchor) {
    const cur = parseBank(S.bank).page;
    openMenu({ anchor, minWidth: 220, ariaLabel: "Pages", groups: [{ items: pages().map((p) => ({ value: p, label: `${pageLabel(p)} page`, count: S.pr ? bankAssigned(p) : 0 })) }], value: cur, onPick: (p) => switchPage(p) });
}
/** the deck's own keys light up (edge only): the page key of the page shown, the held key while its layer is on, and, while a layer is latched, every control assigned on it */
function paintHeld() {
    $$("#zone .ctl.held, #zone .ctl.lay").forEach((g) => g.classList.remove("held", "lay"));
    const cur = parseBank(S.bank), light = (id) => $(`#zone .ctl[data-id="${id}"]`)?.classList.add("held");
    for (const [id, n] of pageKeys()) if (n === (S.layerShown || layerOf(cur.page))) light(id);
    if (sliderModeName()) for (const [id, md] of modeKeys()) if (md === S.sMode) light(id);
    if (!cur.mod) return;
    for (const [id, e] of Object.entries(S.pr?.eff?.[S.bank] || {})) if (e && e.action !== "generic.none") $(`#zone .ctl[data-id="${id}"]`)?.classList.add("lay");
    for (const [id, v] of holdKeys()) if (v === cur.mod) light(id);
}
function switchPage(p) { const cur = parseBank(S.bank); if (p === cur.page) return; const ml = modsFor(p); switchBank(composeBank(p, ml.includes(cur.mod) ? cur.mod : null)); }
function switchHold(mod) { switchBank(composeBank(parseBank(S.bank).page, mod)); }
/** Resolve only: the deck keys that choose the slider mode (action Slider mode), with the mode each picks. Falls back to the printed Hue / Sat / Lum keys. */
function modeKeys() {
    const m = new Map();
    const page = parseBank(S.bank).page, eff0 = S.pr?.eff || {}, base = eff0[composeBank(page, null)] || eff0[composeBank(pageFor(rootOf(page), 1), null)] || {};
    if (isResolve()) for (const [id, e] of Object.entries(base)) { const v = e?.action === "resolve.pmode" && e.fields?.pmode?.value; if (v && HOT[id]) m.set(id, v); }
    for (const [id, v] of [["Hue", "hue"], ["Sat", "sat"], ["Lum", "lum"]]) if (HOT[id] && !m.has(id) && ![...m.values()].includes(v)) m.set(id, v);
    return m;
}
/** "Hue mode" while a Resolve preset is open (the engine starts in Hue), else null */
const sliderModeName = () => (S.sMode && !S.editBase && !parseBank(S.bank).mod ? `${smodeLabel(S.sMode)} mode` : null);
/** latch a slider mode for editing: the title, the key's edge, the inspector and the callouts all follow */
function setSliderMode(md) {
    const base = md === "base";
    if (!base && !SMODES.some((x) => x.value === md)) return;
    if (base ? S.editBase : (S.sMode === md && !S.editBase)) return;
    if (base) S.editBase = true; else { S.sMode = md; S.editBase = false; }
    if (!base && !S.liveAuth) { S.liveMode = md; paintLeds(); } // simulator: choosing a mode (key on the picture or the Base | Hue | Sat | Lum switch) is what the engine would report, so the LED follows; with the engine reporting, its LED state stays the truth
    S.pending = null; paintHeld(); paintLayerTitle(); renderDeck(true); ctx.stage.refreshLabels();
    if (S.control && isPControl(S.control)) renderPanel(true);
    if (S.view === "list") ctx.list.refresh();
}
/** The LEDs show the deck's LIVE state, not the editing latch: the slider mode the engine is in (Hue / Sat / Lum, one lit) and Custom Mode (its own on/off). The latch is the key's edge. */
function paintLeds() {
    if (!ctx.stage) return;
    const m = S.liveMode || "hue";
    ctx.stage.setLeds({ hue: m === "hue", sat: m === "sat", lum: m === "lum", custom: !!S.customOn });
    paintLayerTitle();
}
/** live state from the engine poll (real) or from the input events (sim and real): a Hue / Sat / Lum press sets the mode, a Custom Mode press flips it */
function ledEvent(ev) {
    if (S.liveAuth || ev.kind !== "press") return; // with the engine reporting, its state is the truth; only the simulator is tracked by hand
    const e = S.pr?.eff?.[ev.bank || S.bank]?.[ev.control], md = ev.action === "resolve.pmode" ? e?.fields?.pmode?.value || modeKeys().get(ev.control) : null;
    if (md) { S.liveMode = md; paintLeds(); followMode(); } else if (ev.control === "CustomMode") { S.customOn = !S.customOn; paintLeds(); }
}
/** the editor's mode layer follows the deck: when the engine's slider mode changes (deck-state event or /api/live) the title, the Base | Hue | Sat | Lum switch and the mode key's edge move to it. A control selected for editing is left alone, except the mode key that was selected because it was the latched mode: that selection moves with the mode. */
function followMode() {
    const m = S.liveMode; if (!m || !S.pr || S.followedMode === m) return;
    S.followedMode = m;
    if (!SMODES.some((x) => x.value === m) || (S.sMode === m && !S.editBase)) return;
    const keys = modeKeys(), prevKey = [...keys].find(([, md]) => md === S.sMode)?.[0], sel = S.control;
    setSliderMode(m);
    if (sel && sel === prevKey) { const nk = [...keys].find(([, md]) => md === m)?.[0]; if (nk) selectControl(nk); }
}
/** pushed by the engine the moment Hue / Sat / Lum / Custom Mode changes (long-polled /api/events): paint on the very next frame */
function deckState(ev) {
    if (ev.mode) { S.liveAuth = true; S.liveMode = ev.mode; }
    if (typeof ev.custom === "boolean") S.customOn = ev.custom;
    if (ev.layer) { S.live = Object.assign({}, S.live, { layer: ev.layer }); followLayer(); } // the layer rides the same push as the mode: no poll, no extra request
    followMode();
    S.ledAt = performance.now(); S.ledPushT = ev.t;
    if (ctx.stage) requestAnimationFrame(() => { paintLeds(); S.ledPaintT = Date.now(); }); // a display cannot show it sooner than the next frame
}
/** every /api/live read: when the engine reports a slider mode, the LEDs mirror ITS state (slider mode, Custom Mode) */
function syncLive(l) {
    if (l && l.sliderMode) { S.liveAuth = true; S.liveMode = l.sliderMode; S.customOn = !!l.customMode; paintLeds(); followMode(); } else S.liveAuth = false;
}
/** a write to the engine's deck state (mode / Custom Mode). Outside mock mode a click ALWAYS sends it (whether or not /api/live has reported a slider mode yet: that flag flaps and must not decide). Never silent: a failure shows an error toast with the reason and `revert` puts the screen back; a dev server without --engine-state (404) falls back to the local simulation. Every click leaves a [client] log line saying what it did. */
function deckStateWrite(state, what, revert, simulate) {
    clog(`deck-state click: ${what} -> POST /api/deck-state ${JSON.stringify(state)} (liveAuth=${S.liveAuth}, liveMode=${S.liveMode || "-"}, custom=${S.customOn})`);
    api.setDeckState(state).then(() => clog(`deck-state click: ${what} accepted`)).catch((e) => {
        if (e && e.status === 404 && simulate) { clog(`deck-state click: ${what}: no /api/deck-state here (404), simulated locally`); simulate(); return; }
        const why = (e && e.message) || "the engine did not accept it";
        clog(`deck-state click: ${what} FAILED: ${why}`);
        toast({ msg: `Could not switch ${what}: ${why}`, tone: "bad", key: "deck-state" });
        try { revert && revert(); } catch (err) { /* the toast already said it */ }
    });
}
/** a click on a held-key or page key on the deck picture latches it; the same held key again releases it, the same page key again goes back to the first layer. The key is also selected, so its own assignment is editable like any other. */
function onDeckKey(id) {
    if (id === "CustomMode") { /* the engine owns the state: ask it, then render from the deck-state event that comes back */
        if (MOCK || !api.setDeckState) { S.customOn = !S.customOn; paintLeds(); selectControl(id); return true; }
        const next = !S.customOn;
        deckStateWrite({ customMode: next }, "Custom Mode", () => paintLeds(), () => { S.customOn = next; paintLeds(); });
        selectControl(id); return true;
    } // simulator: Custom Mode is a toggle on the picture; with the engine reporting, the LED follows the real deck and the click only selects
    const md = modeKeys().get(id); if (md) {
        const prevMode = S.sMode, prevBase = S.editBase, prevSel = S.control, prevFollow = S.followedMode;
        if (!MOCK && api.setDeckState) deckStateWrite({ sliderMode: md }, smodeLabel(md) + " mode", () => { // refused: go back to what was there, and say so
            S.followedMode = prevFollow; if (prevBase) setSliderMode("base"); else if (prevMode) setSliderMode(prevMode);
            if (prevSel && prevSel !== id) selectControl(prevSel); else if (S.control === id) deselectControl(); paintLeds();
        }, () => { S.liveMode = md; paintLeds(); });
        S.followedMode = md; setSliderMode(md); selectControl(id); return true;
    }
    const mod = holdKeys().get(id), cur = parseBank(S.bank);
    if (mod) { switchHold(cur.mod === mod ? null : mod); selectControl(id); return true; }
    const n = pageKeys().get(id); if (!n) return false;
    const want = n === layerOf(cur.page) && n > 1 ? 1 : n, tgt = pageFor(rootOf(cur.page), want);
    if (pages().includes(tgt)) switchPage(tgt); else toast({ msg: `${pageLabel(tgt)} page has no assignments yet`, tone: "info", key: "page" });
    selectControl(id); return true;
}
/** inspector note for a layer key or page key: what it is, and how to leave */
function heldNote(id) {
    const md = modeKeys().get(id);
    if (md) return h("div", { class: "banner-row held-note" }, icon("sliders-vertical", 16), h("span", {}, h("b", {}, `Slider mode key: ${smodeLabel(md)}`), md === S.sMode ? " is the mode you are editing. Sliders and P buttons on the deck are shown in this mode." : ". Click it on the deck to edit the sliders in this mode."));
    const mod = holdKeys().get(id), cur = parseBank(S.bank);
    if (mod) {
        const m = MODS.find((x) => x.id === mod), on = cur.mod === mod, nm = `${m.name || m.label}`;
        return h("div", { class: "banner-row held-note" }, icon("layers", 16),
            h("span", {}, h("b", {}, `Held key: ${nm} layer`), on ? " is on. Everything you click now is edited on this layer." : ". Click it on the deck to edit the layer it opens."),
            on ? h("button", { type: "button", class: "btn ghost sm", onclick: () => { switchHold(null); } }, "Leave layer") : null);
    }
    const n = pageKeys().get(id); if (!n) return null;
    const tgt = pageFor(rootOf(cur.page), n), on = n === layerOf(cur.page);
    return h("div", { class: "banner-row held-note" }, icon("layers", 16),
        h("span", {}, h("b", {}, `Page key: ${pageLabel(tgt)} page`), on ? " is showing. Everything you click now is edited on this page." : ". Click it on the deck to show that page."));
}
function stepBank(d) { const pl = pages(), cur = parseBank(S.bank), i = pl.indexOf(cur.page); if (i < 0) return; switchPage(pl[(i + d + pl.length) % pl.length]); }
let bankSeq = 0;
function switchBank(b, auto) {
    if (b === S.bank) return;
    S.layerShown = null; // a real bank is shown now
    if (!auto) S.layerPinAt = (S.live && S.live.layer) || 0; // a manual page / layer pick holds until the deck changes layer again
    const seq = ++bankSeq;
    S.bank = b; S.pending = null;
    if (S.control && !eff(b, S.control)) { S.control = null; ctx.stage.setSelected(null); }
    renderLayerState();
    const co = $(".callouts");
    if (ctx.stage.calloutsOn && !reduced()) anim(co, [{ opacity: 1 }, { opacity: 0 }], { duration: 100, easing: EASE_IN });
    setTimeout(() => { if (seq !== bankSeq) return; renderDeck(false, true); if (co) anim(co, [{ opacity: 0 }, { opacity: 1 }], { duration: 1, fill: "none" }); }, reduced() ? 0 : 60);
    renderPanel(true);
    ctx.drawers.rebuild();
}
function openViewMenu() {
    const btn = $("#viewMenuBtn");
    const el = h("div", { class: "menu viewmenu" });
    const row = (label, flag, kbd) => { const sw = switchCtl({ checked: S.flags[flag], label, onChange: (v) => setFlag(flag, v) }); return h("div", { class: "m-row sw-row" }, h("span", { class: "m-name" }, label), kbd ? h("kbd", { class: "cap sm" }, kbd) : null, sw.el); };
    el.append(row("Callouts", "callouts", "L"), row("All edges", "allEdges"));
    const sw2 = (label, on, fn) => { const sw = switchCtl({ checked: on, label, onChange: fn }); return h("div", { class: "m-row sw-row" }, h("span", { class: "m-name" }, label), sw.el); };
    el.append(h("div", { class: "pop-sep sp" }, "Accessibility"), sw2("Reduce motion", reduced(), (v) => { setReduceOverride(v); toast({ msg: v ? "Motion is reduced" : "Motion is back", tone: "info", key: "motion" }); }), sw2("High contrast", document.documentElement.getAttribute("data-contrast") === "more" || matchMedia("(prefers-contrast: more)").matches, (v) => { setContrastOverride(v); }));
    popover({ anchor: btn, el, minWidth: 240, placement: "bottom-end" });
}
/** Callouts are on by default whenever the stage has room for them; a manual choice (S.coUser) always wins and is remembered per viewer. */
const roomForCallouts = () => { const z = $("#zone"); return !!z && z.clientWidth >= 860 && z.clientHeight >= 520 && (!ctx.stage || ctx.stage.fitsCallouts(S.flags.callouts ? 0 : 40)); };
/** the stage cannot give the deck its minimum readable scale with the bands: Callouts drop to off by themselves (the labelled toggle brings them back), and return when there is room again unless the viewer chose */
function onStageFit() { /* Callouts only ever change with the toggle: no automatic on or off */ }
let coOffT = 0;
/** Callouts on: the deck scales to its callout size (one ease-out, 280 ms, no overshoot) while the callout layer stays hidden; the boxes are already at their final places when the whole layer fades in (opacity only, 160 ms). Off is the reverse: the layer fades out completely, then the deck scales back. Reduced motion: instant. */
function applyCallouts(v) {
    clearTimeout(coOffT); S.flags.callouts = !!v; paintLegend(); paintViewBtn();
    const wrap = document.querySelector(".callouts"), rm = reduced();
    if (v) {
        const from = ctx.stage.deckState();
        if (wrap) { wrap.style.transition = "none"; wrap.style.opacity = rm ? "" : "0"; wrap.style.pointerEvents = "none"; }
        ctx.stage.withoutGlide(() => { ctx.stage.setCallouts(true); ctx.stage.layout({ callouts: true }); ctx.stage.buildCallouts(true, calloutItems()); });
        const reveal = () => {
            if (!wrap) return;
            if (!rm && ctx.stage.calloutsOn) ctx.stage.coIntro(); // the deck has settled: the boxes arrive one by one, leaders drawing out from their controls
            wrap.style.transition = ""; wrap.style.opacity = ""; wrap.style.pointerEvents = "";
        };
        ctx.stage.morph(from, 280, reveal); return;
    }
    const go = () => {
        const from = ctx.stage.deckState();
        ctx.stage.withoutGlide(() => { ctx.stage.setCallouts(false); ctx.stage.layout({ callouts: false }); });
        if (wrap) { wrap.style.transition = ""; wrap.style.opacity = ""; wrap.style.pointerEvents = ""; }
        ctx.stage.morph(from, 280);
    };
    if (rm || !wrap || !wrap.childElementCount) { go(); return; }
    wrap.style.pointerEvents = "none"; ctx.stage.coOutro(); // boxes fade 120 ms, leaders retract 150 ms, all at once; then the deck scales back
    coOffT = setTimeout(go, 160);
}
const paintViewBtn = () => { const b = $("#viewBtn"); if (b) b.setAttribute("aria-pressed", String(!!S.flags.callouts)); $("#viewMenuBtn")?.classList.toggle("active", !!S.flags.allEdges); };
function setFlag(k, v) {
    S.flags[k] = !!v;
    if (k === "callouts") { S.coUser = !!v; ctx.stage && ctx.stage.setForced(S.coUser === true); }
    store.set("flags", { ...S.flags, coUser: S.coUser });
    if (k === "callouts") { applyCallouts(v); }
    if (k === "allEdges") ctx.stage.setAllEdges(v);
    if (k === "lens") { applyLens(); }
    paintViewBtn();
}
function paintLegend() { const lg = $("#coLegend"); lg.hidden = !(S.flags.callouts || peekOn); }

/* ---- overrides lens: edge highlights only (overridden = accent edge, inherited = faint, unassigned = none) */
let lensDiff = false, diffMap = null, peekOn = false;
async function loadDiff() {
    diffMap = null; covMap = null; if (!S.app) return;
    // preferred: /api/coverage carries overridden + changedFromDefault per control; fallback: /api/defaults/diff, then the effective data we already hold
    if (S.ep.coverage !== false) {
        try {
            const r = await api.coverage(S.app); S.ep.coverage = true; covMap = {}; let has = false;
            for (const b of r.banks || []) for (const c of b.controls || []) { if (typeof c.changedFromDefault === "boolean") has = true; (covMap[b.bank] = covMap[b.bank] || {})[c.id] = c; }
            if (!has) covMap = null;
        } catch (e) { if (e.status === 404) S.ep.coverage = false; covMap = null; }
    }
    if (covMap || S.ep.diff === false) return;
    try { const r = await api.diff(S.app); S.ep.diff = true; diffMap = {}; for (const c of r.controls || []) (diffMap[c.bank] = diffMap[c.bank] || {})[c.control] = c.change; }
    catch (e) { if (e.status === 404) S.ep.diff = false; diffMap = null; }
}
let covMap = null;
function lensMap() {
    const m = {}; const bank = S.pr?.eff?.[S.bank] || {};
    for (const id of Object.keys(bank)) {
        const own = bank[id].source === "app";
        if (lensDiff && covMap) m[id] = covMap[S.bank]?.[id]?.changedFromDefault ? "chg" : "inh";
        else if (lensDiff && diffMap) { const ch = diffMap[S.bank]?.[id]; m[id] = ch && ch !== "unchanged" ? "chg" : "inh"; }
        else m[id] = (covMap ? covMap[S.bank]?.[id]?.overridden : own) ? "ov" : "inh";
    }
    return m;
}
function applyLens() {
    const on = !!S.flags.lens;
    const b = $("#lensBtn"); b.setAttribute("aria-pressed", String(on)); b.classList.toggle("active", on);
    const pill = $("#lensPill"); pill.hidden = !on;
    if (!ctx.stage) return;
    if (!on || !S.pr) { ctx.stage.setLens(null); return; }
    const m = lensMap(); ctx.stage.setLens(m);
    const n = Object.values(m).filter((v) => v === "ov" || v === "chg").length;
    /* one segmented control: Overrides | Changes. The count lives in its accessible name and tooltip, not in extra chrome. */
    const can = S.ep.diff !== false || !!covMap, g = isGlobalApp();
    const key = (can ? "seg" : "one") + (g ? "g" : "a");
    const what = lensDiff && (covMap || diffMap) ? "changed from the default" : g ? "set in Global" : "overridden here";
    const tip = `${n} control${n === 1 ? "" : "s"} ${what}`;
    if (!pill.firstChild || pill.dataset.k !== key) {
        pill.dataset.k = key;
        if (can) {
            const sg = segmented({ options: [{ value: "ov", label: g ? "Set" : "Overrides" }, { value: "chg", label: "Changes" }], value: lensDiff ? "chg" : "ov", label: "Overrides lens", onChange: async (v) => { lensDiff = v === "chg"; if (lensDiff) await loadDiff(); applyLens(); } });
            sg.el.classList.add("lens-seg"); pill.replaceChildren(sg.el); pill._seg = sg;
        } else pill.replaceChildren(h("span", { class: "lens-one" }, g ? "Set in Global" : "Overrides"));
    }
    pill._seg?.set(lensDiff && (covMap || diffMap) ? "chg" : "ov");
    pill.setAttribute("data-tip", tip); (pill.firstChild).setAttribute("aria-label", tip);
}
function toggleLens() { S.flags.lens = !S.flags.lens; store.set("flags", S.flags); if (S.flags.lens) loadDiff().then(applyLens); applyLens(); }

/* ================================================================== view (deck | list), ownership, empty app */
let viewSeg = null;
function buildViewToggle() {
    const lab = (ic, t) => h("span", { class: "vs" }, icon(ic, 16), t);
    viewSeg = segmented({ options: [{ value: "deck", label: lab("layout-grid", "Deck") }, { value: "list", label: lab("list", "List") }], value: S.view, label: "View", onChange: (v) => setView(v) });
    viewSeg.el.classList.add("view-seg");
    $("#stageBar").insertBefore(viewSeg.el, $("#lensBtn"));
}
function setView(v, focus) {
    if (v !== "deck" && v !== "list") return;
    S.view = v; store.set("view", v);
    document.body.classList.toggle("view-list", v === "list");
    if (viewSeg) viewSeg.set(v);
    const lv = $("#listView"); lv.hidden = v !== "list";
    for (const id of ["lensBtn"]) { const b = $("#" + id); b.disabled = v === "list"; }
    if (v === "list") { ctx.list.refresh(); if (focus) ctx.list.focus(); }
    else { if (focus) (S.control ? ctx.stage.focus(S.control) : ctx.stage.firstHit()?.focus()); paintEmptyApp(); }
    paintEmptyApp();
}
async function selectRow(bank, control) {
    const pm = PM_RE.exec(String(bank)); if (pm) { const pg = isResolve() ? pm[1] : null; if (pg && pg !== parseBank(S.bank).page && banks().includes(pg)) switchBank(pg); setSliderMode(pm[2]); selectControl(control); return; }
    if (bank !== S.bank && banks().includes(bank)) switchBank(bank);
    selectControl(control);
}
async function unassignRow(bank, control) {
    const pm = PM_RE.exec(String(bank)); if (pm) { try { const r = await api.unassign(S.app, bank, control); await afterWrite(r, `${ctlLabel(control)} cleared for ${smodeLabel(pm[2])} mode`, { full: true }); } catch (er) { fail(er); } return; }
    if (bank !== S.bank && banks().includes(bank)) switchBank(bank);
    await unassignControl(control);
}
/** Delete on a focused control: unassign it with the usual toast + Undo. Only an assignment made here can be removed. */
async function unassignControl(id) {
    const e = effv(id);
    if (!e) { toast({ msg: `${ctlLabel(id)} has nothing assigned.`, tone: "info", key: "del" }); return; }
    if (e.source !== "app") { toast({ msg: `${ctlLabel(id)} uses ${e.baseFallback ? "the base assignment" : isGlobalApp() ? "the default" : "Global"}. Choose Off here to silence it in ${appName(S.app)}.`, tone: "info", key: "del" }); return; }
    if (S.control !== id) { S.control = id; S.draft = {}; ctx.stage.setSelected(id); }
    await clearAssign();
}
const unassignSelected = () => (S.control ? unassignControl(S.control) : toast({ msg: "Select a control first.", tone: "info", key: "del" }));
/** Inherit Global | Custom | Off here */
async function setOwnership(mode) {
    const e = eff(), own = !!e && e.source === "app", off = own && e.action === "generic.none";
    if (mode === "inherit") { if (own) await clearAssign(); return; }
    if (mode === "off") { if (!off && actionDef("generic.none")) await pickAction("generic.none"); return; }
    if (mode === "custom") {
        if (own && !off) return;
        if (e && !own && e.action && e.action !== "generic.none") { await overrideWhole(); return; }
        $(".picker")?.click();
    }
}
function paintEmptyApp() {
    const el = $("#emptyApp"); if (!el) return;
    const a = appInfo(S.app);
    const own = S.pr ? Object.values(S.pr.raw?.banks || {}).reduce((t, b) => t + Object.keys(b || {}).length, 0) : 1;
    const empty = !!a && !a.isGlobal && S.pr && own === 0 && S.view !== "list" && !S.control;
    el.hidden = !empty;
    if (!empty) return;
    if (el.dataset.app === S.app && el.firstChild) return;
    el.dataset.app = S.app;
    const nm = a.name;
    const rows = h("div", { class: "ea-rows" });
    const tpl = h("button", { type: "button", class: "btn sm", hidden: true }, icon("sparkles"), "Start from a template");
    const canCopy = S.ep.copy !== false && copySources().length > 0;
    const cp = h("button", { type: "button", class: "btn sm", hidden: !canCopy, onclick: (e) => copyFromMenu(e.currentTarget) }, icon("copy"), "Copy from another app");
    let hasTpl = false;
    const sub = h("div", { class: "ea-s" });
    // the sentence names only the actions this card really offers (template appears once the engine says one exists for this app)
    const paintSub = () => { const alt = [hasTpl ? "start from a template" : null, canCopy ? "copy another app" : null].filter(Boolean); sub.textContent = "Turn on Test, then press a key on the Loupedeck." + (alt.length ? ` Or ${alt.join(", or ")}.` : ""); };
    paintSub();
    const tr = h("button", { type: "button", class: "btn sm", onclick: () => toggleTest() }, icon("activity"), "Press a control");
    rows.append(tpl, cp, tr);
    const rn = S.reqNote(S.app);
    el.replaceChildren(h("div", { class: "ea-t" }, `Nothing set for ${nm} yet`), ...(rn ? [rn] : []), sub, rows);
    if (S.ep.templates !== false) api.templates().then((r) => {
        S.ep.templates = true; const list = Array.isArray(r) ? r : r.templates || [];
        const t = list.find((x) => (x.bundleId || x.bundle || x.appId) === S.app); if (!t) return;
        tpl.hidden = false; hasTpl = true; paintSub(); tpl.firstChild.nextSibling.textContent = `Start from the ${t.name} template`;
        tpl.onclick = async () => {
            // an existing empty app has nothing to lose: swap it for the template (two history steps, both undone together)
            try { await api.removeApp(S.app); const r2 = await api.fromTemplate(t.id || t.template, S.app); await loadApps(); renderAppPicker(); await selectApp(r2.id || S.app); toast({ msg: `Started from the ${t.name} template`, undo: async () => { await api.undo(); await api.undo(); await reloadAll(); } }); }
            catch (er) { if (!netDown(er)) { toast({ msg: er.message || "The template couldn\u2019t be applied.", tone: "bad" }); await reloadAll(); } }
        };
    }).catch((er) => { if (er.status === 404) S.ep.templates = false; });
}
/** apps this one can copy from: never itself, and never Resolve (the engine refuses a copy into or out of Resolve) */
function copySources() { const res = (a) => String(a.id).includes("DaVinciResolve") || (a.packs || []).includes("resolve"); return isResolve() ? [] : S.apps.filter((a) => a.id !== S.app && !res(a)); }
function copyFromMenu(anchor) {
    const others = copySources();
    if (!others.length) { toast({ msg: "There is no other app to copy from yet.", tone: "info" }); return; }
    openMenu({ anchor, minWidth: 240, ariaLabel: "Copy from", groups: [{ label: "Copy every assignment from", items: others.map((a) => ({ value: a.id, label: a.isGlobal ? "Global" : a.name, count: a.assigned })) }], placement: "top-start",
        onPick: async (id) => {
            const doCopy = async () => { try { const r = await api.copyFrom(S.app, { from: id, scope: "all", mode: "merge" }); S.ep.copy = true; await reloadAll(); toast({ msg: `Copied ${r.copied} assignment${r.copied === 1 ? "" : "s"} from ${appName(id)}`, undo: () => doUndo() }); } catch (er) { fail(er); } };
            try {
                const r = await api.copyFrom(S.app, { from: id, scope: "all", mode: "merge", dryRun: true });
                if (!r.dryRun) { await doCopy(); return; }
                const el = $("#emptyApp"), sk = (r.skipped || []).length, reasons = Object.entries(r.skippedByReason || {}).map(([k, n]) => `${n} ${plain(k)}`).join(", ");
                el.dataset.app = ""; // rebuilt by paintEmptyApp afterwards
                el.replaceChildren(h("div", { class: "ea-t" }, r.copied ? `Copy ${r.copied} assignment${r.copied === 1 ? "" : "s"} from ${appName(id)}?` : `Nothing to copy from ${appName(id)}`), h("div", { class: "ea-s ea-show" }, sk ? `Skips ${sk}${reasons ? ` (${reasons})` : ""}.` : "Nothing is skipped."),
                    h("div", { class: "ea-rows" }, h("button", { type: "button", class: "btn sm ghost", onclick: () => { paintEmptyApp(); } }, "Cancel"), r.copied ? h("button", { type: "button", class: "btn sm primary", onclick: doCopy }, `Copy ${r.copied}`) : null));
            } catch (er) { if (er.status === 404) await doCopy(); else fail(er); }
        } });}

/* ================================================================== deck + panel rendering */
function stageInfo(id) {
    const e = effv(id), m = ctlMeta(id), hs = HOT[id];
    const a = e && actionDef(e.action);
    const state = !e ? "none" : e.source === "app" ? "set" : "inh";
    return { name: plainControl(m?.label || id), kind: hs?.kind || "key", action: a ? a.label : "", state, stateText: !e ? "Unassigned" : e.baseFallback ? "Inherits base" : e.source === "app" && e.action === "generic.none" ? `Off in ${appName(S.app)}` : e.source === "app" ? (isGlobalApp() ? "Set in Global" : "Set here") : "Inherited from Global", summary: e ? plain(String(e.summary || "")) : "", layers: layersFor(id) };
}
/** every layer a control does something on, in page order then key order: [{page, mod, glyph, action, chord, current}] */
function layersFor(id) {
    const rows = [], ordered = [];
    for (const p of pages()) { ordered.push({ b: composeBank(p, null) }); for (const m of MODS) if (banks().includes(composeBank(p, m.id))) ordered.push({ b: composeBank(p, m.id) }); }
    if (isPControl(id)) { // the slider-mode layers of a P1-P8 control: [Sat] + [P1]
        const pgs = isResolve() ? pages() : ["base"];
        for (const p of pgs) for (const md of SMODES) { const b = isResolve() ? `${p}-pm-${md.value}` : `pm-${md.value}`; if (S.pr?.eff?.[b]?.[id]) ordered.push({ b, pm: md.value, page: p }); }
    }
    for (const { b, pm, page: pg } of ordered) {
        const e = S.pr?.eff?.[b]?.[id]; if (!e) continue;
        const { page, mod } = pm ? { page: pg, mod: null } : parseBank(b), off = e.action === "generic.none";
        if (off && mod) continue; // nothing on that layer
        const a = actionDef(e.action), k = e.fields?.key?.value;
        const lab = e.fields?.label?.value || ""; rows.push({ page, mod, pm: pm || null, glyph: mod ? modGlyph[mod] : pm ? smodeLabel(pm) : "", action: off ? "Off" : k && lab.toLowerCase().includes(String(k).toLowerCase()) ? "" : lab || (a ? a.label : e.summary || ""), chord: !off && k ? String(k) : "", off, current: pm ? pm === S.sMode && !S.editBase && !parseBank(S.bank).mod && page === parseBank(S.bank).page : b === S.bank && !(modeOn(id) && S.pr?.eff?.[modeBank(S.sMode)]?.[id]) });
    }
    return rows;
}
const GLYPH = { Cmd: "\u2318", Shift: "\u21E7", Alt: "\u2325", Ctrl: "\u2303", Return: "\u23CE", Escape: "\u238B", Tab: "\u21E5", Delete: "\u232B", Left: "\u2190", Right: "\u2192", Up: "\u2191", Down: "\u2193" };
const glyphChord = (t) => String(t).split("+").map((x) => GLYPH[x] || x).join("");
function calloutItems() {
    const m = bankView();
    return Object.keys(m).filter((id) => ctx.stage.has(id) && m[id] && m[id].action !== "generic.none").map((id) => {
        const e = m[id], a = actionDef(e.action);
        const rk = String((e && e.risk) || (a && a.risk) || "safe").toLowerCase(); // safe | keys | shell | destructive
        return { id, label: calloutText(e, a), mode: isPControl(id) ? sliderModeName() : null, icon: actionIcon(e.action), state: e.source === "app" ? "set" : "inh", risk: rk === "keys" || rk === "shell" || rk === "destructive" ? rk : null };
    });
}
/** the same plain phrases as the List view: "Holds Shift", the action's own label, else its shortcut or action name */
function calloutText(e, a) {
    if (e.action === "generic.modifierHold") { const md = MODS.find((x) => x.id === e.fields?.modifier?.value); return md ? `Holds ${md.name || md.label}` : "Holds a key"; }
    if (e.action === "resolve.hueCurve") return `Hue vs ${e.fields?.curve?.value === "lum" ? "Lum" : "Sat"} curve`;
    const own = e.fields && e.fields.label && e.fields.label.value;
    const k = e.fields?.key?.value;
    if (own && !/chord/i.test(String(own)) && (e.fields.label.source === "app" || String(own).length <= 18)) return String(own);
    if (a && a.type === "key" && k) return glyphChord(k);
    return (a && a.label) || "";
}
function renderDeck(instant, bankChange) {
    if (!ctx.stage || !S.pr) return;
    ctx.stage.setSelected(S.control, true);
    ctx.stage.refreshLabels(); applyLens();
    $("#emptyBank").hidden = bankAssigned(S.bank) > 0;
    paintEmptyBank(); paintEmptyApp(); paintCoverage();
    if (S.view === "list") ctx.list.refresh();
    if (!S.flags.callouts) { // normal mode: the stage keeps this layer's callouts ready for the hover callout, and plays the intro when the app or the layer page changes (not for a held modifier)
        ctx.stage.setGhostItems(calloutItems());
        const ik = S.app + "|" + parseBank(S.bank).page;
        if (ik !== introKey) { introKey = ik; if (S.view !== "list" && !S.test) ctx.stage.introCallouts(); }
    }
    if (S.flags.callouts) {
        if (firstCoFade) ctx.stage.withoutGlide(() => ctx.stage.buildCallouts(!bankChange && !instant, calloutItems())); else ctx.stage.buildCallouts(!bankChange && !instant, calloutItems());
        if (firstCoFade) { firstCoFade = false; const wrap = document.querySelector(".callouts"); if (wrap && !reduced()) { wrap.style.transition = "none"; wrap.style.opacity = "0"; (document.fonts && document.fonts.ready ? document.fonts.ready : Promise.resolve()).then(() => setTimeout(() => { wrap.style.transition = "opacity 160ms ease-out"; wrap.style.opacity = "1"; setTimeout(() => { wrap.style.transition = ""; wrap.style.opacity = ""; }, 190); }, 250)); } } // first load with Callouts already on: the final state, one soft fade, no zoom
    }
}
let firstCoFade = true;
let introKey = "";
/* Resolve: how many of this layer's controls are assigned, and which are not */
let covSeq = 0;
async function paintCoverage() {
    const my = ++covSeq;
    if (!isResolve() || S.ep.coverage === false || !S.pr) { S.cov = null; paintLayerTitle(); return; }
    try {
        const r = await api.coverage(S.app); S.ep.coverage = true; if (my !== covSeq) return;
        const b = (r.banks || []).find((x) => x.bank === S.bank); if (!b) { S.cov = null; paintLayerTitle(); return; }
        const ctrls = (b.controls || []).filter((c) => ctx.stage.has(c.id)), dead = ctrls.filter((c) => c.state === "none");
        S.cov = { bank: S.bank, total: ctrls.length, set: ctrls.length - dead.length, dead: dead.map((c) => c.id) };
        paintLayerTitle();
    } catch (e) { if (e.status === 404) S.ep.coverage = false; S.cov = null; paintLayerTitle(); }
}
function openDead(btn, ids) {
    openMenu({ anchor: btn, minWidth: 220, ariaLabel: "Unassigned controls", groups: [{ label: "Unassigned", items: ids.map((id) => ({ value: id, label: ctlLabel(id) })) }], onPick: (id) => selectControl(id) });
}
function paintEmptyBank() {
    const el = $("#emptyBank"); if (el.hidden) return;
    if (!$("#emptyApp").hidden) { el.hidden = true; return; }
    el.replaceChildren(icon("layers", 16, "eb-ic"), h("h3", {}, "Nothing assigned on this layer"), h("p", {}, "Select a control on the deck, then pick an action for it."),
        h("button", { type: "button", class: "btn ghost sm", onclick: () => ctx.openDrawer("library") }, "Browse the action library"));
    if (!isGlobalApp() && !isResolve() && S.ep.copy !== false && S.apps.length > 1) el.append(h("button", { type: "button", class: "btn ghost sm", onclick: async () => {
        try { const r = await api.copyFrom(S.app, { from: GLOBAL, scope: "bank", bank: S.bank, mode: "merge" }); S.ep.copy = true; await reloadAll(); toast({ msg: `Copied ${r.copied} assignment${r.copied === 1 ? "" : "s"} from Global`, undo: () => doUndo() }); }
        catch (e) { if (e.status === 404) { S.ep.copy = false; paintEmptyBank(); } else fail(e); }
    } }, icon("copy"), "Copy from Global"));
    placeEmptyBank();
}
/** the empty-layer message sits in the free space under the deck, between it and the live strip; where that space is too small it is not drawn at all (the title row and the inspector already say "0 controls assigned" / "Select a control"). It is never laid over the photo. */
function placeEmptyBank() {
    const el = $("#emptyBank"); if (!el || el.hidden || !ctx.stage) return;
    el.classList.remove("eb-off");
    const z = $("#zone").getBoundingClientRect(), db = ctx.stage.deckBox(), live = $("#live"), limit = live && !live.hidden ? live.getBoundingClientRect().top : z.bottom;
    el.style.bottom = "auto"; el.style.top = Math.round(db.b - z.top + 16) + "px";
    const r = el.getBoundingClientRect();
    if (r.bottom > limit - 8 || r.top < db.b) el.classList.add("eb-off");
}
{ let pend = 0; const again = () => { cancelAnimationFrame(pend); pend = requestAnimationFrame(placeEmptyBank); }; new MutationObserver(again).observe($("#zone"), { attributes: true, attributeFilter: ["style"] }); addEventListener("resize", again); document.addEventListener("transitionend", again, true); }
let panelSwap = 0;
/* ---- the inspector is a drawer: open while a control is selected (or a drawer is open), closed otherwise. The deck re-fits with ONE ease only when it opens or closes; switching between controls leaves the width, and so the deck, alone. */
let inspOpen = null, inspTimer = 0;
const wantInsp = () => !!S.control || !!(ctx.drawers && ctx.drawers.current && !ctx.drawers.closing);
function refitDeck(change) {
    const wrap = document.querySelector(".callouts"), rm = reduced(), on = ctx.stage.calloutsOn;
    clearTimeout(coOffT);
    const from = ctx.stage.deckState();
    if (on && wrap && !rm) { wrap.style.transition = "none"; wrap.style.opacity = "0"; wrap.style.pointerEvents = "none"; }
    ctx.stage.withoutGlide(() => { change(); ctx.stage.layout(); if (on) ctx.stage.buildCallouts(true, calloutItems()); });
    ctx.stage.morph(from, 280, () => {
        if (!(on && wrap)) return;
        if (rm) { wrap.style.opacity = ""; wrap.style.pointerEvents = ""; return; }
        wrap.style.transition = "opacity 160ms ease-out"; wrap.style.opacity = "1";
        coOffT = setTimeout(() => { wrap.style.transition = ""; wrap.style.opacity = ""; wrap.style.pointerEvents = ""; }, 190);
    });
}
function syncInsp(first) {
    if (!ctx.stage) return;
    const want = wantInsp(); if (want === inspOpen) return;
    const insp = $("#panel"), body = $("#body"), w = (insp.offsetWidth || 360) + 24;
    const was = inspOpen; inspOpen = want;
    const cols = want ? "var(--insp-w)" : "0px";
    clearTimeout(inspTimer);
    if (was === null) { body.style.setProperty("--insp-cols", cols); springSet(insp, { tx: want ? 0 : w }); insp.hidden = !want; return; }
    if (want) { insp.hidden = false; if (!reduced()) springSet(insp, { tx: w }); }
    refitDeck(() => body.style.setProperty("--insp-cols", cols));
    springTo(insp, { tx: want ? 0 : w }, "panel", () => { if (!inspOpen) insp.hidden = true; });
    if (!want) inspTimer = setTimeout(() => { if (!inspOpen) insp.hidden = true; }, 700);
}
function renderPanel(fade) {
    syncInsp();
    const P = $("#panel");
    const build = () => {
        let kids;
        try { kids = renderInspector(ctx); }
        catch (e) { console.error(e); kids = [h("div", { class: "empty" }, icon("triangle-alert", 32, "em-ic"), h("h2", {}, "Couldn\u2019t show this control"), h("p", {}, e.message || "Unexpected error."))]; }
        P.replaceChildren(...kids); P.scrollTo?.(0, 0);
    };
    if (!fade || reduced()) { build(); if (fade) anim(P, [{ opacity: 0 }, { opacity: 1 }], { duration: 120 }); return; }
    // selection hand-off: swap at once and crossfade the new body in over 80 ms (no travel, no stagger)
    panelSwap++; build();
    anim(P, [{ opacity: 0.15 }, { opacity: 1 }], { duration: 80, easing: EASE_OUT });
}
function selectControl(id) {
    S.control = id; S.draft = {}; if (S.pending && (S.pending.control !== id || S.pending.bank !== wbank(id))) S.pending = null;
    ctx.stage.setSelected(id);
    renderPanel(true);
    ctx.drawers.rebuild(); paintEmptyApp();
    if (S.view === "list") ctx.list.refresh();
    if (S.flags.callouts) ctx.stage.buildCallouts(true, calloutItems());
    // keep the library caption and Sensitivity / Advanced drawers on the same control
}

/* ================================================================== writes */
const timers = {}, pending = {}; let inflight = 0;
function rowsFor(key) { return [...ctx.rows, ...ctx.drows].filter((r) => r.key === key); }
function queueValue(key, value) {
    if (!editable()) return; // paused: nothing is queued, the controls are disabled too
    S.draft[key] = value; ctx.curve && ctx.curve.update();
    pending[key] = value;
    clearTimeout(timers[key]); timers[key] = setTimeout(() => { delete pending[key]; saveValue(key, value); }, 150);
}
/** write every debounced edit now (page hidden / closing) or drop them (offline: they cannot be saved) */
function flushTimers(drop) {
    for (const k of Object.keys(pending)) { clearTimeout(timers[k]); const v = pending[k]; delete pending[k]; if (!drop) saveValue(k, v); }
}
function ghostValues() {
    if (isGlobalApp() || !S.gl) return null;
    const e = eff(), ge = S.gl.eff[S.bank]?.[S.control] || S.gl.eff.base?.[S.control];
    if (!e || !ge || ge.action !== e.action) return null;
    return Object.fromEntries(Object.entries(ge.fields || {}).map(([k, x]) => [k, x.value]));
}
function ghostFor(key) {
    const g = ghostValues(), e = eff(); if (!g || !e || e.source !== "app") return null;
    const cur = S.draft[key] ?? e.fields[key]?.value;
    return g[key] != null && g[key] !== cur ? g[key] : null;
}
async function ensureOwn() {
    const e = eff(); if (e && e.source === "app") return;
    await api.assign(S.app, wbank(S.control), S.control, { action: e.action, type: actionDef(e.action)?.type });
}
async function saveValue(key, value) {
    const e = eff(); if (!e) return;
    const f = actionDef(e.action)?.fields.find((x) => x.key === key); if (!f) return;
    const ctl = S.control;
    inflight++;
    try {
        const wasOwn = e.source === "app";
        await ensureOwn();
        const r = await api.value(S.app, wbank(ctl), ctl, key, value);
        rowsFor(key).forEach((x) => x.setError(null));
        await afterWrite(r, `${ctlLabel(ctl)}: ${f.label}${typeof value === "object" || value == null ? " changed" : " set to " + value}`, { full: !wasOwn, key, ctl });
        rowsFor(key).forEach((x) => x.flash());
    } catch (er) {
        if (netDown(er) || readOnly(er)) return;
        rowsFor(key).forEach((x) => x.setError(er.message));
        toast({ msg: `Couldn’t save ${f.label}. ${er.message}`, tone: "bad", key: "err:" + key });
    } finally { inflight--; }
}
async function resetValue(key) {
    const e = eff(); const f = e && actionDef(e.action)?.fields.find((x) => x.key === key);
    try { const r = await api.value(S.app, wbank(S.control), S.control, key, null); await afterWrite(r, `${f ? f.label : key} reset`, { key, reset: true }); rowsFor(key).forEach((x) => x.flash()); }
    catch (er) { fail(er); }
}
async function resetMany(keys) {
    let n = 0;
    try { for (const k of keys) { if (eff()?.fields?.[k]?.source === "app") { await api.value(S.app, wbank(S.control), S.control, k, null); n++; } } }
    catch (er) { fail(er); }
    await loadPreset(); await loadApps(); renderAppPicker(); patchFields(); renderLayerState();
    if (n) toast({ msg: `Reset ${n} value${n === 1 ? "" : "s"} to ${isGlobalApp() ? "default" : "inherited"}`, undo: async () => { for (let i = 0; i < n; i++) await api.undo(); await reloadAll(); } });
}
/** actions with required values that have no default (a key, a command, a modifier...) cannot be saved empty: the inspector asks for them first */
const needsValue = (a) => (a ? a.fields.filter((f) => f.required && f.default == null) : []);
async function assignAction(control, id) {
    const a = actionDef(id); if (!a) return;
    if (needsValue(a).length) {
        if (S.control !== control) { S.control = control; S.draft = {}; ctx.stage.setSelected(control); }
        S.pending = { bank: wbank(control), control, action: id, vals: {} };
        renderPanel(false); ctx.drawers.rebuild(); setTimeout(() => $(".draft .kc, .draft input, .draft .sel, .draft .seg-opt")?.focus({ preventScroll: true }), 30);
        return;
    }
    S.pending = null;
    if (S.control !== control) { S.control = control; S.draft = {}; ctx.stage.setSelected(control); }
    try { const r = await api.assign(S.app, wbank(control), control, { action: id, type: a.type, ...modeDefaults(control, a) }); await afterWrite(r, plainChange(`Assign ${control}`, ctlLabel), { full: true }); }
    catch (e) { fail(e); }
}
async function commitPending() {
    const p = S.pending; if (!p) return; const a = actionDef(p.action);
    try { const r = await api.assign(S.app, p.bank, p.control, { action: p.action, type: a.type, ...p.vals }); S.pending = null; await afterWrite(r, plainChange(`Assign ${p.control}`, ctlLabel), { full: true }); }
    catch (e) { fail(e); }
}
function cancelPending() { S.pending = null; renderPanel(false); }
/** a Hue curve point given to a P slider in Sat / Lum mode starts on that mode's curve and on the slider's own hue (P3 = yellow) */
function modeDefaults(control, a) {
    if (a.type !== "hueCurve" || !modeOn(control) || !(S.sMode === "sat" || S.sMode === "lum")) return {};
    const n = /^P([1-8])/.exec(control); return { curve: S.sMode, ...(n ? { hue: Number(n[1]) - 1 } : {}) };
}
const pickAction = (id) => assignAction(S.control, id);
const assignTo = (control, actionId) => assignAction(control, actionId);
async function overrideWhole() {
    const e = eff();
    try { const r = await api.assign(S.app, wbank(S.control), S.control, { action: e.action, type: actionDef(e.action)?.type }); await afterWrite(r, `${ctlLabel(S.control)} set for ${modeOn(S.control) ? sliderModeName() : appName(S.app)}`, { full: true }); }
    catch (er) { fail(er); }
}
async function clearAssign() {
    try { const r = await api.unassign(S.app, wbank(S.control), S.control); await afterWrite(r, `${ctlLabel(S.control)} unassigned`, { full: true }); }
    catch (er) { fail(er); }
}
async function afterWrite(r, label, o = {}) {
    toast({ msg: (r && r.label && plainChange(r.label, ctlLabel)) || label, undo: () => doUndo(), key: o.key ? `save:${o.ctl || S.control}:${o.key}` : undefined });
    if (r && r.warnings && r.warnings.length) toast({ msg: r.warnings.map((w) => w.message || w).join(" "), tone: "info", key: "warn", ms: 8000 });
    await loadPreset(); await loadApps();
    renderAppPicker(); renderLayerState(); renderDeck(true);
    if (S.flags.lens) loadDiff().then(applyLens);
    if (o.full) { renderPanel(true); ctx.drawers.rebuild(); } else patchFields();
    ctx.drawers.refresh();
}
function patchFields() {
    const e = eff(); const a = e && actionDef(e.action); if (!a) return;
    for (const row of [...ctx.rows, ...ctx.drows]) { const fv = e.fields[row.key]; if (fv) row.patch(fv); }
    ctx.curve && ctx.curve.update();
}
/* undo / redo availability: the same numbers the History drawer reads (current position vs the entries around it). Unknown (no history endpoint yet) = enabled. */
const hist = { known: false, undo: true, redo: true }; let histTimer = 0, histSeq = 0;
function paintHist() {
    const u = $("#undoBtn"), r = $("#redoBtn"); if (!u || !r) return;
    const du = hist.known && !hist.undo, dr = hist.known && !hist.redo;
    /* aria-disabled, never the disabled attribute: a disabled button drops keyboard focus the moment the last step is undone */
    for (const [b, d] of [[u, du], [r, dr]]) { if (d) b.setAttribute("aria-disabled", "true"); else b.removeAttribute("aria-disabled"); b.classList.toggle("is-off", d); }
    u.setAttribute("data-tip", du ? "Nothing to undo" : "Undo"); r.setAttribute("data-tip", dr ? "Nothing to redo" : "Redo");
}
async function refreshHist() {
    if (S.ep.history === false || offline) return; const my = ++histSeq;
    try {
        const r = await api.history(); if (my !== histSeq) return; S.ep.history = true;
        const cur = r.current ?? r.pos ?? 0;
        hist.known = true; hist.undo = cur > 0; hist.redo = (r.entries || []).some((x) => x.index > cur); paintHist();
    } catch (e) { if (e instanceof ApiError && e.status === 404) { S.ep.history = false; hist.known = false; paintHist(); } }
}
const queueHist = () => { clearTimeout(histTimer); histTimer = setTimeout(refreshHist, 120); };
events.addEventListener("mutated", queueHist);
window.addEventListener("focus", queueHist);
async function doUndo() { if (hist.known && !hist.undo) return; await stepHistory(api.undo, "Undone", false); }
async function doRedo() { if (hist.known && !hist.redo) return; await stepHistory(api.redo, "Redone", true); }
async function stepHistory(fn, word, isRedo) {
    try {
        const r = await fn();
        if (r && r.ok === false) { toast({ msg: (r.label && plainChange(r.label, ctlLabel)) || `Nothing to ${isRedo ? "redo" : "undo"}`, tone: "info", key: "hist" }); return; }
        toast({ msg: `${word}: ${r.label ? plainChange(r.label, ctlLabel, /^Restore defaults/.test(r.label) && restoreApp() ? appName(restoreApp()) : undefined) : "last change"}`, tone: "info", key: "hist", ...(isRedo ? { undo: () => doUndo() } : { redo: () => doRedo() }) });
        await reloadAll();
    } catch (e) { fail(e); }
}
async function reloadAll(opts = {}) {
    await loadApps();
    if (!appInfo(S.app)) { return selectApp(S.apps.find((a) => a.id.includes("DaVinciResolve"))?.id || S.apps[0]?.id); }
    await loadPreset(); loadStd();
    if (S.flags && S.flags.lens) await loadDiff();
    const bl = banks(); if (!bl.includes(S.bank)) S.bank = bl[0];
    if (S.control && !ctx.stage.has(S.control)) S.control = null;
    renderAppPicker(); renderLayerState(); renderDeck(true); renderPanel(false);
    if (opts.quiet && ctx.drawers.midFlow) return; // the live poll must not wipe a preview the person is reading
    refreshHud();
    ctx.drawers.rebuild(); ctx.drawers.refresh();
}

/* ================================================================== first-run tour */
function startTour() {
    if ($(".tour")) return;
    /* the coach card sits beside its target, never over it and never over the photo */
    const CW = 300, CH = 150;
    const belowApps = () => { const r = $(".top").getBoundingClientRect(); return [24, r.bottom + 20]; };
    const underDeck = () => { const d = $(".deck").getBoundingClientRect(), z = $("#zone").getBoundingClientRect(); const y = d.bottom + 28; return [z.left + (z.width - CW) / 2, y + CH < innerHeight - 8 ? y : z.top + 16]; };
    const inPanel = () => { const r = $("#panel").getBoundingClientRect(); return [r.left + (r.width - CW) / 2, r.top + Math.max(24, r.height * 0.5)]; };
    const steps = [
        { sel: "#appPick", t: "Pick an app", d: "Global applies everywhere. Add an app to give it its own deck layout.", pos: belowApps },
        { sel: "#zone", t: "Pick a control", d: "Click a knob, key or slider on the deck. Or turn Test on and press it on the real deck.", pos: underDeck },
        { sel: "#panel", t: "Pick an action", d: "Choose what it does here. Everything is undoable with Cmd+Z.", pos: inPanel },
    ];
    let i = 0, card = null, first = true;
    /* 60% scrim with a rounded cut-out (12px radius, 4px outset) around the step's target; the cut-out glides between targets */
    const scrim = h("div", { class: "tour-scrim", "aria-hidden": "true" }, h("div", { class: "tour-hole" }));
    const hole = scrim.firstChild;
    const place = (animate) => {
        const r = $(steps[i].sel).getBoundingClientRect(), o = 4;
        if (!animate || reduced()) hole.style.transition = "none"; else hole.style.transition = "";
        Object.assign(hole.style, { left: r.left - o + "px", top: r.top - o + "px", width: r.width + o * 2 + "px", height: r.height + o * 2 + "px" });
        if (!animate || reduced()) { void hole.offsetWidth; hole.style.transition = ""; }
    };
    const onKey = (e) => { if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); done(); } };
    const onResize = () => { place(false); };
    const done = () => { card?.remove(); scrim.remove(); removeEventListener("keydown", onKey, true); removeEventListener("resize", onResize); store.set("tourDone", true); };
    const show = () => {
        card?.remove();
        const st = steps[i];
        const [x, y] = st.pos();
        card = h("div", { class: "tour", role: "dialog", "aria-label": "Quick tour" }, h("h3", {}, st.t), h("p", {}, st.d),
            h("div", { class: "tour-foot" }, h("span", { class: "tour-step" }, `${i + 1} of ${steps.length}`), h("span", { class: "sp" }),
                h("button", { type: "button", class: "btn text", onclick: done }, "Skip"),
                i ? h("button", { type: "button", class: "btn sm", onclick: () => { i--; show(); } }, "Back") : null,
                h("button", { type: "button", class: "btn sm light", onclick: () => { if (i < steps.length - 1) { i++; show(); } else done(); } }, i < steps.length - 1 ? "Next" : "Done")));
        card.style.left = Math.min(innerWidth - CW - 16, Math.max(8, x)) + "px"; card.style.top = Math.min(innerHeight - CH - 16, Math.max(8, y)) + "px";
        if (first) { document.body.append(scrim); place(false); first = false; if (!reduced()) anim(scrim, [{ opacity: 0 }, { opacity: 1 }], { duration: 200 }); } else place(true);
        document.body.append(card); card.querySelector(".btn.light").focus({ preventScroll: true, focusVisible: false });
        if (!reduced()) anim(card, [{ opacity: 0, transform: "translateY(4px)" }, { opacity: 1, transform: "none" }], { duration: 160, easing: EASE_OUT });
    };
    addEventListener("keydown", onKey, true); addEventListener("resize", onResize);
    show();
}
const tourSeen = () => { try { return !!store.get("tourDone", false); } catch (e) { return true; } };
/* ================================================================== drawers, live, test */
function onDrawerChange(name) {
    if (lastDrawerName === "setup" && name !== "setup") setupClosed();
    lastDrawerName = name;
    const P = $("#panel");
    P.classList.toggle("covered", !!name);
    syncInsp(); syncInert();
    $("#libBtn").setAttribute("aria-pressed", String(name === "library")); $("#libBtn").classList.toggle("active", name === "library");
}
function onLiveChange(open, hgt) {
    S.test = open;
    $("#zone").style.setProperty("--live-pad", open ? hgt + "px" : "0px");
    if (!open && S.pinned) { S.pinned = false; const pb = $("#pinBtn"); if (pb) { pb.setAttribute("aria-pressed", "false"); pb.firstChild.firstChild.setAttribute("href", "#i-pin-off"); } }
    document.body.classList.toggle("test-mode", open); paintLiveChip();
    $("#testBtn").setAttribute("aria-pressed", String(open)); $("#testBtn").classList.toggle("active", open);
    ctx.stage.layout({ bottom: open ? hgt : 0 });
    if (ctx.stage.calloutsOn) ctx.stage.buildCallouts(true);
    placeEmptyBank();
    renderPanel(false);
}
/** Test mode: while this window is in front the engine swallows deck input. Say which state we are in, and flip it with window focus. */
function paintLiveChip() {
    const c = $("#liveSeg"); c.hidden = !S.test; if (!S.test) return;
    const front = document.hasFocus();
    c.className = "st-seg live-seg " + (front ? "paused" : "live");
    c.replaceChildren(front ? icon("pause", 16) : h("i", { class: "sdot ok" }), front ? "Window in front: deck paused" : "Live: deck active");
    c.setAttribute("data-tip", front ? "While this window is in front the deck does not fire, so tests never trigger real actions. Switch to another app to use it." : "The deck is controlling the frontmost app.");
}
addEventListener("focus", () => paintLiveChip()); addEventListener("blur", () => paintLiveChip());
function setPinned(v) { S.pinned = !!v; renderPanel(false); toast({ msg: v ? `Pinned to ${ctlLabel(S.control) || "this control"}. The inspector stays put.` : "Following the device again", tone: "info", key: "pin" }); }
function toggleTest() { if (ctx.drawers.liveOpen) ctx.drawers.closeLive(); else ctx.drawers.openLive($("#testBtn")); }

function openMoreMenu() {
    const btn = $("#moreBtn");
    const items = [];
    if (S.ep.history !== false) items.push({ value: "history", label: "History", icon: "history" });
    items.push({ value: "profile", label: "App profile…", icon: "app-window" });
    if (S.ep.deps !== false) items.push({ value: "setup", label: "Requirements…", icon: "plug" });
    if (S.ep.io !== false) items.push({ value: "io", label: "Import, export, backup…", icon: "archive" });
    if (S.ep.ai !== false) items.push({ value: "ai", label: "AI access…", icon: "terminal" });
    if (S.apps.some((x) => String(x.id).includes("DaVinciResolve") || (x.packs || []).includes("resolve"))) items.push({ value: "hud", label: "HUD…", icon: "panel-top" });
    items.push({ value: "help", label: "Help", icon: "circle-help" }, { value: "tour", label: "Show tour", icon: "sparkles" }, { value: "keys", label: "Keyboard shortcuts", icon: "keyboard" }, { value: "about", label: "About 2T5 DECK+", icon: "info" });
    openMenu({ anchor: btn, minWidth: 240, groups: [{ items }], ariaLabel: "More", placement: "bottom-end", menu: true, onPick: (v) => { if (v === "keys") showShortcuts(btn); else if (v === "tour") startTour(); else if (v === "setup") openSetup(btn); else ctx.drawers.open(v, btn); } });
}
function showShortcuts(opener) {
    if ($("#shortcutSheet")) return;
    const G = [
        ["Navigate", [["Command palette", "Cmd+K"], ["Previous / next bank", "[ ]"], ["Switch app", "Cmd+1..9"], ["Move between controls", "~Arrow keys"], ["First / last control in a row", "~Home, End"], ["Deselect", "Escape"]]],
        ["Edit", [["Undo", "Cmd+Z"], ["Redo", "Cmd+Shift+Z"], ["Unassign the focused control", "Delete"], ["History", "Cmd+Shift+H"], ["Action library", "Cmd+L"]]],
        ["View", [["Deck or list", "V"], ["Callouts on / off", "L"], ["Peek at callouts", "Hold L"], ["Overrides lens", "O"], ["Live test", "Cmd+T"], ["This list", "?"]]],
    ];
    const cols = h("div", { class: "sheet-cols" });
    for (const [t, rows] of G) { cols.append(h("h3", {}, t)); for (const [n, k] of rows) cols.append(h("div", { class: "sc-row" }, h("span", {}, n), k === "[ ]" ? h("span", { class: "caps" }, h("kbd", { class: "cap" }, "["), h("kbd", { class: "cap" }, "]")) : k === "Cmd+1..9" ? h("span", { class: "caps" }, h("kbd", { class: "cap gl" }, "\u2318"), h("kbd", { class: "cap word" }, "1\u20139")) : k === "~Arrow keys" ? h("span", { class: "caps", "aria-label": "Arrow keys" }, ...["\u2190", "\u2191", "\u2193", "\u2192"].map((g) => h("kbd", { class: "cap gl" }, g))) : k === "~Home, End" ? h("span", { class: "caps" }, h("kbd", { class: "cap word" }, "Home"), h("kbd", { class: "cap word" }, "End")) : k[0] === "~" ? h("span", { class: "hint" }, k.slice(1)) : k === "Hold L" ? h("span", { class: "caps" }, h("span", { class: "hint" }, "Hold"), h("kbd", { class: "cap" }, "L")) : keycaps(k))); }
    const sheet = h("div", { class: "sheet", role: "dialog", "aria-modal": "true", "aria-label": "Keyboard shortcuts" }, h("h2", {}, "Keyboard shortcuts"), cols);
    const scrim = h("div", { class: "pal-scrim", id: "shortcutSheet", onpointerdown: (e) => { if (e.target === scrim) close(); } }, sheet);
    document.body.append(scrim);
    const key = (e) => { if (e.key === "Escape" || e.key === "?") { e.preventDefault(); e.stopPropagation(); close(); } };
    function close() { document.removeEventListener("keydown", key, true); scrim.remove(); if (opener && document.contains(opener)) opener.focus({ preventScroll: true }); }
    document.addEventListener("keydown", key, true);
    sheet.setAttribute("tabindex", "-1"); sheet.focus();
    if (!reduced()) { anim(scrim, [{ opacity: 0 }, { opacity: 1 }], { duration: 140 }); anim(sheet, [{ opacity: 0, transform: "translateY(-8px) scale(.98)" }, { opacity: 1, transform: "none" }], { duration: 200, easing: EASE_OUT }); }
}

/* ================================================================== jump (Cmd+K) */
function jumpItem(app, bank, control, e) {
    const a = e && e.action ? actionDef(e.action) : null;
    const m = ctlMeta(control);
    return { key: `${app}|${bank}|${control}`, title: m?.label || control, sub: `${appName(app)} · ${bankName(bank)}${a ? " · " + a.label : e?.label ? " · " + e.label : " · Unassigned"}`, icon: a ? actionIcon(a.id) : "circle-dashed", app, bank, control };
}
/* labs: one printable page, the current app's assignments (window.print may not work in the packaged app, so this stays behind ?labs=1) */
function printSheet() {
    ctx.list.refresh(); const rows = ctx.list.rowsData();
    let el = $("#printSheet"); if (!el) { el = h("div", { id: "printSheet" }); document.body.append(el); }
    el.replaceChildren(h("h1", {}, `${appName(S.app)}: deck assignments`), h("table", {}, h("thead", {}, h("tr", {}, ...["Control", "Action", "Value", "Source", "Bank"].map((t) => h("th", {}, t)))),
        h("tbody", {}, ...rows.map((r) => h("tr", {}, h("td", {}, r.label), h("td", {}, r.action), h("td", {}, r.value), h("td", {}, r.src[1]), h("td", {}, bankName(r.bank)))))));
    setTimeout(() => window.print(), 50);
}
function cmdHost() {
    return {
        state: () => ({ test: S.test, lens: !!S.flags.lens, callouts: !!S.flags.callouts, view: S.view }),
        undo: doUndo, redo: doRedo, drawer: (n) => ctx.drawers.open(n, $("#moreBtn")), library: () => $("#libBtn").click(), test: toggleTest, lens: toggleLens,
        callouts: () => setFlag("callouts", !S.flags.callouts), toggleView: () => setView(S.view === "list" ? "deck" : "list", true), copyMarkdown: () => ctx.list.copyMarkdown(),
        print: printSheet, addApp: () => openAddApp($("#appPick")), shortcuts: () => showShortcuts($("#moreBtn")), arm: toggleArm, unassign: unassignSelected, stepBank, selectApp, switchBank, switchPage, switchHold,
        reduce: () => { const v = !reduced(); setReduceOverride(v); toast({ msg: v ? "Motion is reduced" : "Motion is back", tone: "info", key: "motion" }); },
        contrast: () => { const on = document.documentElement.getAttribute("data-contrast") === "more"; setContrastOverride(!on); toast({ msg: on ? "Normal contrast" : "High contrast on", tone: "info", key: "contrast" }); },
    };
}
function openJump(opener) {
    const here = () => {
        const out = [];
        const bank = S.pr?.eff?.[S.bank] || {};
        for (const c of S.profile?.controls || []) { if (!ctx.stage.has(c.id)) continue; out.push(jumpItem(S.app, S.bank, c.id, bank[c.id])); }
        return out;
    };
    const host = cmdHost();
    openPalette({
        idle: () => {
            const rec = store.get("recentJump", []).slice(0, 5).map((r) => ({ ...jumpItem(r.app, r.bank, r.control, { action: r.action }), sec: "Recent" })); const seen = new Set(rec.map((r) => r.key));
            const bank = here().filter((x) => !seen.has(x.key) && x.icon !== "circle-dashed").slice(0, 4).map((x) => ({ ...x, sec: "This bank" }));
            const cmds = rankCommands(commandItems(host), "").map((c) => ({ ...c, sec: "Commands" }));
            return [...rec, ...bank, ...cmds];
        },
        query: async (q) => {
            const local = here().filter((it) => fuzzy(q, it.title + " " + it.sub) > 0);
            let remote = [];
            if (S.ep.search !== false) {
                try { const r = await api.search(q, 40); S.ep.search = true; remote = (r.hits || []).filter((x) => HOT[x.control]).map((x) => ({ ...jumpItem(x.app, x.bank, x.control, { action: x.action, label: x.label }), sub: `${x.appName || appName(x.app)} \u00b7 ${bankLabel(x.bank)} \u00b7 ${x.label || actionDef(x.action)?.label || x.summary || ""}` })); }
                catch (e) { if (e.status === 404) S.ep.search = false; }
            }
            const seen = new Set(); const ctls = [...local, ...remote].filter((x) => (seen.has(x.key) ? false : seen.add(x.key)));
            const scored = ctls.map((x, i) => ({ x, i, sc: (fuzzy(q, x.title) + fuzzy(q, x.sub) * 0.3) * (x.icon === "circle-dashed" ? 0.5 : 1) * (x.app === S.app ? 1 : 0.8) }));
            const all = [...scored.map((o) => ({ it: o.x, sc: o.sc, i: o.i })), ...rankCommands(commandItems(host), q).map((c, i) => ({ it: c, sc: c._sc, i: 1000 + i }))];
            return all.filter((o) => o.sc > 0).sort((a, b) => b.sc - a.sc || a.i - b.i).map((o) => o.it);
        },
        pick: (it) => { if (it.kind === "command") { it.run(); setTimeout(() => { if (document.activeElement === document.body && opener && document.contains(opener) && !document.querySelector(".pop, .drawer:not([hidden])")) opener.focus({ preventScroll: true }); }, 0); } else jumpTo(it); },
    }, opener);
}
async function jumpTo(it) {
    const rec = [{ app: it.app, bank: it.bank, control: it.control, action: S.pr?.eff?.[it.bank]?.[it.control]?.action }, ...store.get("recentJump", []).filter((r) => !(r.app === it.app && r.bank === it.bank && r.control === it.control))].slice(0, 6);
    store.set("recentJump", rec);
    if (it.app !== S.app) await selectApp(it.app);
    if (it.bank !== S.bank && banks().includes(it.bank)) switchBank(it.bank);
    setTimeout(() => { selectControl(it.control); ctx.stage.ping(it.control); }, reduced() ? 0 : 90);
}

/* ================================================================== live events */
let lastFollow = 0, followT = 0;
function follow(ev) {
    if (!S.test || S.pinned || ev.control === S.control) return;
    lastFollow = performance.now();
    if (ev.bank && ev.bank !== S.bank && banks().includes(ev.bank) && ev.presetId === S.app) { S.bank = ev.bank; renderLayerState(); renderDeck(true); }
    selectControl(ev.control);
}
/** what one raw click does for the control that just fired (from the loaded preset), so the live panel can show real arithmetic */
function perFor(ev) {
    if (ev.presetId !== S.app || !S.pr) return null;
    const e = S.pr.eff?.[ev.bank]?.[ev.control]; if (!e || !e.fields) return null;
    const vals = Object.fromEntries(Object.entries(e.fields).map(([k, x]) => [k, x.value]));
    return perClick(actionDef(e.action), vals);
}
function onEvent(ev) {
    if (ev.type === "front") { if (ev.presetId) { S.live = Object.assign({}, S.live, { activeApp: ev.presetId }); followFront(); } refreshFront(); return; } // the frontmost app changed: repaint the status now
    if (ev.type === "deck-state") { deckState(ev); return; } // lamp state, not an input: no pulse, no dock row
    S.lastEv = ev; ledEvent(ev);
    ctx.stage.pulse(ev);
    const hs = HOT[ev.control];
    ctx.dock.push(ev, {
        kind: hs?.kind || "knob", label: ctlLabel(ev.control), kindLabel: (KIND[hs?.kind] || [])[1] || "",
        action: actionDef(ev.action)?.label || ev.action || "", appName, appFor: appInfo, per: perFor(ev),
    });
    if (ctx.curve && ev.control === S.control) ctx.curve.live(ev);
    if (ev.presetId && ev.presetId !== S.app) picker.flash();
    if (!ctx.drawers.liveOpen) { const d = $("#testDot"); d.classList.remove("blink"); void d.offsetWidth; d.classList.add("blink"); }
    // test mode: the selection and inspector follow the control being turned or pressed, unless pinned
    if (S.test && !S.pinned && (ev.kind === "press" || ev.kind === "turn") && ctx.stage.has(ev.control) && ev.control !== S.control) {
        const now = performance.now(), wait = 350 - (now - lastFollow);
        clearTimeout(followT);
        if (ev.kind === "turn" && wait > 0) { followT = setTimeout(() => follow(ev), wait); return; } // the latest touched control always wins
        follow(ev);
    }
}
/* polls pause while the page is hidden, resume with a fresh fetch, and back off when nothing happens */
let lastActivity = performance.now();
const hidden = () => document.hidden;
function waitVisible() { return new Promise((r) => { const f = () => { if (!document.hidden) { document.removeEventListener("visibilitychange", f); r(); } }; document.addEventListener("visibilitychange", f); }); }
async function pollEvents() {
    if (hidden()) { await waitVisible(); S.since = 0; }
    if (offline) { setTimeout(pollEvents, 500); return; }
    let held = false;
    const t0 = performance.now();
    try {
        // long poll: after the first read the server holds the request until something happens, so a press arrives at once (no poll interval in the path)
        const first = S.since === 0;
        const r = await api.events(S.since, first || S.noLongPoll ? 0 : 20000);
        S.since = r.next ?? S.since;
        const evs0 = r.events || [];
        held = !first && !S.noLongPoll && (evs0.length > 0 || performance.now() - t0 > 200); // an answer that came back at once with nothing (mock, old server) is not a long poll: fall back to the timed poll
        if (!first) { const evs = evs0.slice(-30); if (evs.length) lastActivity = performance.now(); for (const ev of evs) onEvent(ev); }
        S.apiDown = 0;
    } catch (e) { if (++S.apiDown >= 2) netDown(e); }
    if (held && S.apiDown === 0) { setTimeout(pollEvents, 0); return; }
    const idle = performance.now() - lastActivity;
    setTimeout(pollEvents, S.apiDown >= 2 ? 2000 : idle > 30000 ? 600 : idle > 5000 ? 250 : 80);
}
let lastSeq = -1, needReload = false;
const busyEditing = () => !!(document.querySelector(".slider.drag, .stepper.scrub, .kc.listen, .pop, .drag-chip, .pal-scrim") || document.activeElement?.closest?.(".frow, .tf, .le") || inflight > 0 || Object.keys(pending).length);
async function pollLive() {
    if (hidden()) { await waitVisible(); }
    if (offline) { setTimeout(pollLive, 500); return; }
    try {
        S.live = await api.live(); syncLive(S.live); paintStatus(); paintSafety();
        const front = S.live.frontmost?.id;
        picker.setFront(); followFront();
        if (S.live.seq != null) {
            if (lastSeq >= 0 && S.live.seq !== lastSeq) { lastSeq = S.live.seq; needReload = true; queueHist(); /* an undo / redo from another client moves the history position too */ } else if (lastSeq < 0) lastSeq = S.live.seq;
        }
        // a preset file changed on disk (external edit): reload once nothing is being edited
        if (needReload && !offline && !busyEditing()) { needReload = false; await reloadAll({ quiet: true }); loadWarnings(); loadPending(); }
    } catch (e) { /* the events poll owns the banner */ }
    setTimeout(pollLive, performance.now() - lastActivity > 30000 ? 2000 : 1000);
}
/* a file dropped anywhere must never navigate the window (WKWebView would, and drafts would be lost): only the import dropzone handles drops */
for (const t of ["dragover", "drop"]) addEventListener(t, (e) => { if (e.dataTransfer && Array.from(e.dataTransfer.types || []).includes("Files")) e.preventDefault(); });
addEventListener("pagehide", () => flushTimers(false));
addEventListener("focus", recheckPerms);
document.addEventListener("visibilitychange", () => { if (document.hidden) flushTimers(false); });

/* ================================================================== boot */
async function probe() {
    for (const [name, fn] of [["history", () => api.history()], ["io", () => api.backups()]]) {
        try { await fn(); S.ep[name] = true; } catch (e) { if (e instanceof ApiError && e.status === 404) S.ep[name] = false; }
    }
    try { await api.aiAccess(); S.ep.ai = true; } catch (e) { if (e instanceof ApiError && e.status === 404) S.ep.ai = false; }
    paintAppActs();
    try { await api.getArm(); S.ep.arm = true; } catch (e) { if (e instanceof ApiError && e.status === 404) { S.ep.arm = false; paintSafety(); } }
    loadWarnings(); loadTemplates(); loadPending();
}
/* fail-closed start: the packaged app may report that the presets could not be opened and the deck was paused */
async function checkStartup() {
    try {
        const f = window.go && window.go.main && window.go.main.App && window.go.main.App.StartupState; if (!f) return;
        const st = await f(); if (!st) return;
        const bad = st.editorAvailable === false || st.reason === "store-unavailable" || st.storeUnavailable || st.disarmedByFailure;
        const why = st.reason && st.reason !== "store-unavailable" ? st.reason : (st.message || st.error || "");
        if (bad) { notices.startup = `Presets could not be opened, so the deck is paused.${why ? " " + String(why) : ""}`; renderNotice(); paintArmSeg(); }
    } catch (e) { /* no startup state: nothing to show */ }
}
async function init() {
    document.body.classList.add("loading");
    try {
        const [d, reg] = await Promise.all([api.deck(), api.registry()]);
        S.profile = d.profile || d; S.registry = reg;
        S.packs = Array.isArray(reg.packs) ? reg.packs : Object.entries(reg.packs).map(([id, p]) => ({ id, ...p }));
        for (const pk of S.packs) for (const a of pk.actions) S.actions[a.id] = { ...a, pack: pk.id };
        await loadApps();
        probe(); queueHist();
    } catch (e) { document.body.classList.remove("loading"); return fail(e); }
    if (!ctx.stage) buildStage();
    syncInsp();
    renderAppPicker();
    const want = QS.get("app") && S.apps.find((a) => a.id === QS.get("app") || a.name === QS.get("app"));
    await selectApp((want || S.apps.find((a) => a.id.includes("DaVinciResolve")) || S.apps[0]).id);
    if (QS.get("bank") && banks().includes(QS.get("bank"))) switchBank(QS.get("bank"));
    document.body.classList.remove("loading");
    ctx.stage.layout(); ctx.stage.enter(); setView(S.view);
    if (QS.get("sel") && ctx.stage.has(QS.get("sel"))) selectControl(QS.get("sel"));
    const tour = !tourSeen() && !QS.has("notour") && !MOCK;
    if (tour) setTimeout(startTour, 600);
    const depsP = loadDeps(true);
    loadPreflight().then(async (pre) => { await depsP; if (pre && !tour && !S.depsAuto && !ctx.drawers.current && !S.preShown && (pre.checks || []).some((c) => c.status === "warn" || c.status === "fail")) { S.preShown = true; openWhy($("#status")); } });
}
function buildStage() {
    const flags = ((v) => (v && typeof v === "object" && !Array.isArray(v) ? v : {}))(store.get("flags", {}));
    /* an older build's shape (or junk) never breaks the start */
    S.coUser = typeof flags.coUser === "boolean" ? flags.coUser : null; S.flags = { callouts: S.coUser === true, allEdges: !!flags.allEdges, lens: !!flags.lens };
    S.view = QS.get("view") === "list" ? "list" : QS.get("view") === "deck" ? "deck" : store.get("view", "deck") === "list" ? "list" : "deck";
    ctx.stage = createStage({ zone: $("#zone"), controls: S.profile.controls, info: stageInfo, onFit: (ok) => onStageFit(ok), onSelect: (id) => { if (!onDeckKey(id)) selectControl(id); }, onDelete: (id) => unassignControl(id), onEscape: () => { picker.focus(); } });
    ctx.list = createList(ctx); $("#zone").append(ctx.list.el); buildViewToggle();
    ctx.dock = createDock();
    ctx.drawers = createDrawers(ctx);
    ctx.stage.setForced(S.coUser === true); ctx.stage.setCallouts(S.flags.callouts); ctx.stage.setAllEdges(S.flags.allEdges); paintLeds();
    ctx.stage.layout({ callouts: S.flags.callouts });
    paintViewBtn(); paintLegend();
    applyLens();
}

/* ---- static wiring */
initMiniTips();
$("#undoBtn").addEventListener("click", doUndo);
$("#redoBtn").addEventListener("click", doRedo);
$("#libBtn").addEventListener("click", () => (ctx.drawers.current === "library" ? ctx.drawers.close() : ctx.drawers.open("library", $("#libBtn"))));
$("#testBtn").addEventListener("click", toggleTest);
$("#moreBtn").addEventListener("click", openMoreMenu);
$("#brandBtn").addEventListener("click", (e) => (ctx.drawers.current === "about" ? ctx.drawers.close() : ctx.drawers.open("about", e.currentTarget)));
$("#jumpBtn").addEventListener("click", (e) => openJump(e.currentTarget));
$("#lensBtn").addEventListener("click", toggleLens);
$("#armBtn").addEventListener("click", toggleArm);
$("#status").addEventListener("click", (e) => openWhy(e.currentTarget));
$("#status").addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openWhy(e.currentTarget); } });

$("#viewBtn").addEventListener("click", () => setFlag("callouts", !S.flags.callouts));
$("#viewMenuBtn").addEventListener("click", openViewMenu);
/* notices.external is the held-change list; S.pending is also the unsaved-edit slot and selection clears it */
picker = createAppPicker({ button: $("#appPick"), getApps: () => S.apps, getCurrent: () => S.app, getFront: () => S.live?.frontmost?.id, getHeld: (id) => ((notices.external) || []).some((x) => x.preset === id), onCalibrate: (app, el) => ctx.drawers.open("calibration", el), onSelect: (id) => selectApp(id), onAdd: (btn) => openAddApp(btn) });
$("#appMenuBtn").addEventListener("click", (e) => openAppMenu(e.currentTarget));
/* the app menu's options as visible buttons in the top bar, between the arm box and the search icon */
$("#calBtn").addEventListener("click", (e) => ctx.drawers.open("calibration", e.currentTarget));
$("#profileBtn").addEventListener("click", (e) => ctx.drawers.open("profile", e.currentTarget));
$("#ioBtn").addEventListener("click", (e) => ctx.drawers.open("io", e.currentTarget));
/* AI Agent setup: the existing AI access drawer (enable switch, connection line, config block) opened from the top bar, left of the search icon */
/* HUD on/off in the top bar, left of AI Agent. The same persisted setting as More > HUD > "Show the HUD" (the Resolve preset's hudEnabled, written with api.setting) and Fn+C6 (the store); it follows every change through the reload that the live seq / mutated event already trigger. */
const hudRid = () => (S.apps.find((a) => String(a.id).includes("DaVinciResolve") || (a.packs || []).includes("resolve")) || {}).id;
function paintHud(on) { const b = $("#hudBtn"); b.hidden = on == null; if (on == null) return; S.hudOn = on; b.setAttribute("aria-pressed", String(on)); }
let hudReq = 0;
function refreshHud() {
    const rid = hudRid(); if (!rid) { paintHud(null); return; }
    const n = ++hudReq;
    api.preset(rid).then((r) => { if (n !== hudReq) return; const raw = r.preset || r.raw || r, v = raw && raw.settings ? raw.settings.hudEnabled : undefined; paintHud(v == null ? true : !!v); ctx.hudInvalidate && ctx.hudInvalidate(); }).catch(() => { if (n === hudReq) paintHud(null); });
}
$("#hudBtn").addEventListener("click", async () => {
    const rid = hudRid(); if (!rid || S.hudOn == null) return;
    const on = !S.hudOn; hudReq++; paintHud(on);
    try { await api.setting(rid, "hudEnabled", on); ctx.hudInvalidate && ctx.hudInvalidate(); await ctx.reloadAll(); toast({ msg: on ? "The HUD is on" : "The HUD is off", tone: "info", key: "hud-en", undo: () => ctx.doUndo() }); }
    catch (er) { paintHud(!on); toast({ msg: er.message || "Could not change the HUD", tone: "bad", key: "hud-en" }); }
});
events.addEventListener("mutated", refreshHud);
$("#aiBtn").addEventListener("click", (e) => ctx.drawers.open("ai", e.currentTarget));
const paintAppActs = () => { $("#aiBtn").hidden = S.ep.ai === false; $("#calBtn").hidden = !(ctx.calApps && ctx.calApps[S.app]); $("#ioBtn").hidden = S.ep.io === false; };
$("#stdCompare").addEventListener("click", (e) => compareStd(e.currentTarget));
$("#followBtn").addEventListener("click", () => { S.follow = true; followFront(); });
$("#retryBtn").addEventListener("click", retry);
/* L: tap toggles callouts, hold peeks (callouts show only while held) */
let lHeld = 0, lTimer = 0, lPeek = false;
function peek(on) {
    if (on === peekOn) return; peekOn = on;
    if (on) { ctx.stage.setCallouts(true); ctx.stage.layout({ callouts: true }); ctx.stage.buildCallouts(false, calloutItems()); }
    else { ctx.stage.setCallouts(S.flags.callouts); ctx.stage.layout({ callouts: S.flags.callouts }); if (S.flags.callouts) ctx.stage.buildCallouts(true, calloutItems()); }
    paintLegend();
}
document.addEventListener("keydown", (e) => {
    const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName) || document.activeElement?.isContentEditable || document.querySelector(".kc.listen");
    const mod = e.metaKey || e.ctrlKey;
    if (mod && e.key.toLowerCase() === "z" && !typing) {
        e.preventDefault();
        // a settings / import / outside-changes drawer is a sheet over the deck: Undo must not rewrite what is behind it (it would also rebuild the drawer and wipe a preview). The History drawer has its own Undo button.
        if (ctx.drawers.current === "profile" || ctx.drawers.current === "external" || ctx.drawers.midFlow) return;
        e.shiftKey ? doRedo() : doUndo();
    }
    else if (mod && e.key.toLowerCase() === "k") { e.preventDefault(); openJump(document.activeElement); }
    else if (mod && e.key.toLowerCase() === "l") { e.preventDefault(); $("#libBtn").click(); }
    else if (mod && e.key.toLowerCase() === "t") { e.preventDefault(); toggleTest(); }
    else if (mod && e.shiftKey && e.key.toLowerCase() === "h") { e.preventDefault(); if (S.ep.history !== false) ctx.drawers.open("history", document.activeElement); }
    else if (!typing && !mod && !e.altKey && e.key.toLowerCase() === "l") {
        e.preventDefault();
        if (e.repeat || lHeld) return; lHeld = 1; lPeek = false;
        /* no hold-to-peek: callouts show only when the toggle is on */
    }
    else if (mod && !e.shiftKey && !e.altKey && /^[1-9]$/.test(e.key) && !typing) { const a = orderedApps(S.apps)[+e.key - 1]; if (a) { e.preventDefault(); selectApp(a.id).then(() => picker.focus()); } }
    else if (mod && !e.shiftKey && !e.altKey && (e.key === "[" || e.key === "]")) { e.preventDefault(); stepBank(e.key === "]" ? 1 : -1); }
    else if (!typing && !mod && !e.altKey && e.key.toLowerCase() === "v") { e.preventDefault(); setView(S.view === "list" ? "deck" : "list", true); }
    else if (!typing && !mod && !e.altKey && e.key.toLowerCase() === "o" && S.view !== "list") { e.preventDefault(); toggleLens(); }
    else if (!typing && !mod && e.key === "?") { e.preventDefault(); showShortcuts(document.activeElement); }
    else if (!typing && !mod && e.key === "[") stepBank(-1);
    else if (!typing && !mod && e.key === "]") stepBank(1);
    else if (e.key === "Escape" && !e.defaultPrevented && !typing && heldMod() && !ctx.drawers.current && !ctx.drawers.liveOpen && !document.querySelector(".pop, .pal-scrim")) { e.preventDefault(); switchHold(null); }
    else if (e.key === "Escape" && !e.defaultPrevented && !typing && S.control && !ctx.drawers.current && !ctx.drawers.liveOpen && !document.querySelector(".pop, .pal-scrim")) {
        const prev = S.control, fromPanel = !!document.activeElement?.closest?.("#panel") || document.activeElement === document.body;
        S.control = null; ctx.stage.setSelected(null); renderPanel(true); if (S.flags.callouts) ctx.stage.buildCallouts(true, calloutItems());
        if (prev && fromPanel) { if (S.view === "list") ctx.list.focus(); else ctx.stage.focus(prev); } // the inspector closes: focus goes back to the control it was about, never to the page
    }
});
/* a click on empty space (the stage background, the gaps in the header, the area around the deck) closes the open drawer and clears the selection. Controls, boxes, buttons, menus, the inspector and the drawers themselves are not empty space. Latched layers, slider modes and Custom Mode are untouched: only the selection clears. */
const NOT_EMPTY = 'button, a, input, select, textarea, label, summary, [role="button"], [role="menuitem"], [role="tab"], [role="switch"], [contenteditable], .drawer, .pop, #panel, .insp, #live, .dock, .toast, .pal-scrim, .tour-scrim, .tour-card, .ctl, .hit, .co-chip, .co-more, .apick, .list';
const isEmptySpace = (t) => t instanceof Element && !t.closest(NOT_EMPTY) && !(S.view === "list" && t.closest("#zone"));
function deselectControl() { if (!S.control) return; S.control = null; ctx.stage.setSelected(null); renderPanel(true); }
ctx.deselect = deselectControl;
let emptyDown = false, popDown = false;
document.addEventListener("pointerdown", (e) => { popDown = !!document.querySelector(".pop"); emptyDown = e.button === 0 && isEmptySpace(e.target); }, true);
document.addEventListener("click", (e) => {
    if (!emptyDown) return; emptyDown = false;
    if (popDown || !isEmptySpace(e.target)) return; // an open menu only closes itself (its own outside-click handler)
    if (ctx.drawers && ctx.drawers.current) ctx.drawers.close({ noFocus: true });
    deselectControl();
});
document.addEventListener("keyup", (e) => {
    if (e.key.toLowerCase() !== "l" || !lHeld) return;
    lHeld = 0; clearTimeout(lTimer);
    if (lPeek) { lPeek = false; peek(false); } else if (!/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName)) setFlag("callouts", !S.flags.callouts);
});
addEventListener("blur", () => { if (lHeld) { lHeld = 0; clearTimeout(lTimer); if (lPeek) { lPeek = false; peek(false); } } });
window.__editor = { S, ctx, selectApp, selectControl, switchBank, reloadAll, setFlag, toggleTest, openJump, toggleLens };
init().then(() => { refreshHud(); pollEvents(); pollLive(); checkStartup(); loadCal(); setInterval(loadCal, 6000); });

/* dev assertion (mock mode or ?assert=1): no visible text may ever read "null", "undefined", "[object Object]" or "NaN" */
if (MOCK || QS.has("assert")) {
    const LEAK = /(^|[^\w])(null|undefined|NaN)([^\w]|$)|\[object Object\]/;
    let t = 0;
    const scan = () => {
        const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
        for (let n = w.nextNode(); n; n = w.nextNode()) { const el = n.parentElement; if (!el || el.closest("script,style,svg,textarea,input,[hidden]")) continue; if (LEAK.test(n.nodeValue)) console.error("DOM leak: “" + n.nodeValue.trim().slice(0, 60) + "” in ." + String(el.className).split(" ")[0]); }
    };
    new MutationObserver(() => { clearTimeout(t); t = setTimeout(scan, 250); }).observe(document.body, { childList: true, subtree: true, characterData: true });
}

/** a switch in words: configured app names first, then the name macOS reported, then the id; the editor window coming to the front is said as such */
function switchWords(x) {
    const nm = (id, n) => { const a = id ? appName(id) : ""; return a && a !== id && a !== "?" ? a : n || id || ""; };
    if (x.editor) return `2T5 DECK+ window in front \u00b7 ${nm(x.to, x.toName)} stays active`;
    return `${nm(x.from, x.fromName) || "start"} \u2192 ${nm(x.to, x.toName)}`;
}

initStageBg(document.getElementById("stage"));

/** one immediate /api/live read when the engine says the frontmost app changed (pushed on the events stream), so the status line follows the real app without waiting for the next poll */
let frontBusy = false;
async function refreshFront() {
    if (frontBusy || offline || S.ep.live === false) return; frontBusy = true;
    try { S.live = await api.live(); syncLive(S.live); paintStatus(); picker.setFront(); followFront(); } catch (e) { /* the polls own the banner */ }
    frontBusy = false;
}
