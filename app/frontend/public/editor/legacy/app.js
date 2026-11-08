// app.js — 2T5 DECK+ global editor (draft). Vanilla ES modules, no framework.
import { api, ApiError, MOCK } from "../api.js";
import { createDeckMap, decorateDeck } from "./deck.js"; // borrowed from DECKMAP, see deck.js
import { photoProfile, applyPhoto } from "./hotspots.js"; // real photo + hotspot table
const QS = new URLSearchParams(location.search);

const $ = (s, r = document) => r.querySelector(s);
const SVGNS = "http://www.w3.org/2000/svg";
/** SVG node from DOM calls (no HTML strings): sv("line", {x1:..}, "text child") */
function sv(tag, attrs, text) { const n = document.createElementNS(SVGNS, tag); for (const k in attrs || {}) n.setAttribute(k, attrs[k]); if (text != null) n.textContent = String(text); return n; }
function h(tag, attrs, ...kids) {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
        if (v == null || v === false) continue;
        if (k === "class") n.className = v;
        else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
        else n.setAttribute(k, v === true ? "" : v);
    }
    for (const c of kids.flat()) if (c != null && c !== false) n.append(c.nodeType ? c : document.createTextNode(c));
    return n;
}
const GLOBAL = "_global";
const RESOLVE_BANKS = ["col", "edit", "edit-shift", "col-fn", "col-2"];
const APP_BANKS = ["base", "shift", "cmd", "alt", "ctrl", "fn", "layer-2", "layer-3"];

const S = {
    profile: null, registry: null, actions: {}, apps: [], live: null,
    app: null, bank: null, control: null, pr: null, // pr = {raw, eff}
    hideInh: false, test: false, lastEv: null, since: 0, apiDown: 0,
    hist: {}, lastLabel: "", draft: {},
};
let deck = null, fx = null;

/* ------------------------------------------------------------ helpers */
const appInfo = (id) => S.apps.find((a) => a.id === id);
const appName = (id) => (appInfo(id) ? (appInfo(id).isGlobal ? "Global" : appInfo(id).name) : id === GLOBAL ? "Global" : id || "?");
const isGlobalApp = () => S.app === GLOBAL;
const isResolve = () => S.app && (S.app.includes("DaVinciResolve") || (appInfo(S.app)?.packs || []).includes("resolve"));
const eff = (bank = S.bank, ctl = S.control) => S.pr?.eff?.[bank]?.[ctl] || null;
const ctlMeta = (id) => S.profile?.controls.find((c) => c.id === id);
function banks() {
    const have = Object.keys(S.pr?.raw?.banks || {});
    const base = isResolve() ? RESOLVE_BANKS : APP_BANKS;
    return [...new Set([...base, ...have])];
}
function bankCount(b) {
    return Object.keys(S.pr?.eff?.[b] || {}).filter((c) => S.pr.eff[b][c].source === "app").length;
}
function allActions() {
    const packs = S.registry.packs;
    return Array.isArray(packs) ? packs : Object.entries(packs).map(([id, p]) => ({ id, ...p }));
}

/* ------------------------------------------------------------ toast */
let toastTimer = 0;
function toast(msg, opts = {}) {
    const box = $("#toasts");
    box.replaceChildren();
    clearTimeout(toastTimer);
    const t = h("div", { class: "toast" + (opts.bad ? " bad" : ""), role: "status" }, h("span", {}, msg));
    if (opts.undo) t.append(h("button", { type: "button", class: "btn btn-sm", onclick: () => { box.replaceChildren(); doUndo(); } }, "Undo"));
    if (opts.redo) t.append(h("button", { type: "button", class: "btn btn-sm", onclick: () => { box.replaceChildren(); doRedo(); } }, "Redo"));
    t.append(h("button", { type: "button", class: "btn btn-sm btn-ghost", "aria-label": "Dismiss", onclick: () => box.replaceChildren() }, "×"));
    box.append(t);
    toastTimer = setTimeout(() => box.replaceChildren(), opts.bad ? 6000 : 8000);
}
function setLast(label) {
    if (label) S.lastLabel = label;
    $("#lastChange").textContent = S.lastLabel ? "Last change: " + S.lastLabel : "";
}

/* ------------------------------------------------------------ data */
function netDown(e) {
    if (e instanceof ApiError && e.network) {
        $("#apiBanner").hidden = false;
        $("#apiBannerText").textContent = "Can't reach the 2T5 DECK+ engine API. Is the app or dev server running?";
        return true;
    }
    return false;
}
async function loadPreset() {
    const r = await api.preset(S.app);
    S.pr = { raw: r.preset || r.raw || r, eff: r.effective || {} };
}
async function loadApps() {
    S.apps = await api.apps();
}
async function refresh() {
    await Promise.all([loadPreset(), loadApps()]);
    renderApps(); renderBanks(); renderDeck();
}

/* ------------------------------------------------------------ left: apps */
function renderApps() {
    const ul = $("#appList");
    ul.replaceChildren();
    const mk = (a) => h("li", {}, h("button", {
        type: "button", class: "app-row", "data-app": a.id, "aria-current": a.id === S.app ? "true" : "false",
        onclick: () => selectApp(a.id),
    },
    h("span", { class: "dot" + (a.id === (S.live?.frontmost?.id) || a.frontmost ? " on" : ""), title: "Frontmost app" }),
    h("span", { class: "nm" }, h("b", {}, a.isGlobal ? "Global" : a.name), a.isGlobal ? h("small", {}, "Fallback for every app") : null),
    h("span", { class: "cnt", title: "Assigned controls" }, String(a.assigned ?? 0))));
    const g = S.apps.filter((a) => a.isGlobal), rest = S.apps.filter((a) => !a.isGlobal);
    g.forEach((a) => ul.append(mk(a)));
    if (g.length && rest.length) ul.append(h("li", { class: "sep", "aria-hidden": "true" }));
    rest.forEach((a) => ul.append(mk(a)));
    if (!rest.length) ul.append(h("li", { class: "empty" }, "No apps yet. Add one below."));
}
function flashApp(id) {
    const r = $(`.app-row[data-app="${CSS.escape(id)}"]`);
    if (!r) return;
    r.classList.remove("flash"); void r.offsetWidth; r.classList.add("flash");
}

async function selectApp(id) {
    S.app = id; S.control = null;
    try { await loadPreset(); } catch (e) { return fail(e); }
    const bl = banks();
    S.bank = bl.find((b) => bankCount(b) > 0) || bl[0];
    if (isResolve() && bl.includes("col")) S.bank = "col";
    renderApps(); renderBanks(); renderDeck(); renderPanel();
}

/* add-app popover */
function wireAdd() {
    const pop = $("#addPop"), btn = $("#addAppBtn");
    const close = () => { pop.hidden = true; btn.setAttribute("aria-expanded", "false"); };
    btn.addEventListener("click", async () => {
        if (!pop.hidden) return close();
        pop.hidden = false; btn.setAttribute("aria-expanded", "true"); $("#addErr").hidden = true;
        $("#runList").replaceChildren(h("li", { class: "muted" }, "Loading…"));
        try { S.running = await api.running(); } catch (e) { S.running = []; }
        renderRunning();
    });
    document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !pop.hidden) { close(); btn.focus(); } });
    document.addEventListener("pointerdown", (e) => { if (!pop.hidden && !pop.contains(e.target) && e.target !== btn) close(); });
    $("#runFilter").addEventListener("input", renderRunning);
    const doAdd = async (fn) => {
        try { const r = await fn(); await loadApps(); const id = r.id || r.app?.id; close(); toast("App added.", { undo: true }); setLast("Add app"); if (id) await selectApp(id); else renderApps(); }
        catch (e) { if (!netDown(e)) { const er = $("#addErr"); er.textContent = e.message; er.hidden = false; } }
    };
    $("#captureBtn").addEventListener("click", () => doAdd(() => api.capture()));
    $("#manAdd").addEventListener("click", () => doAdd(() => api.addApp($("#manId").value.trim(), $("#manName").value.trim() || undefined)));
    function renderRunning() {
        const f = $("#runFilter").value.toLowerCase();
        const ul = $("#runList"); ul.replaceChildren();
        const have = new Set(S.apps.map((a) => a.id));
        const items = (S.running || []).filter((r) => !f || (r.name + r.id).toLowerCase().includes(f));
        if (!items.length) ul.append(h("li", { class: "muted" }, "No matching running apps."));
        for (const r of items) ul.append(h("li", {}, h("button", { type: "button", disabled: have.has(r.id), onclick: () => doAdd(() => api.addApp(r.id, r.name)) }, h("span", {}, r.name), h("small", {}, have.has(r.id) ? "added" : r.id))));
    }
}

/* ------------------------------------------------------------ middle: banks + deck */
function renderBanks() {
    const box = $("#banks"); box.replaceChildren();
    for (const b of banks()) {
        box.append(h("button", {
            type: "button", class: "bank", role: "tab", "aria-selected": b === S.bank ? "true" : "false",
            onclick: () => { S.bank = b; S.control = null; renderBanks(); renderDeck(); renderPanel(); },
        }, b, h("span", { class: "n" }, String(bankCount(b)))));
    }
}
function renderDeck() {
    if (!deck || !S.pr) return;
    for (const id of deck.controlIds()) {
        const e = eff(S.bank, id);
        const status = !e ? "unbound" : e.action === "generic.none" ? "silenced" : e.source === "app" ? "own" : "inherited";
        let k = e ? String(e.fields?.label?.value || e.fields?.key?.value || e.summary || "") : "";
        if (k.toLowerCase() === String(ctlMeta(id)?.label || id).toLowerCase()) k = ""; // no echo of the engraved label
        deck.setState(id, { status, keyText: k.length > 9 ? k.slice(0, 8) + "…" : k });
    }
    deck.setSelected(S.control);
}
function arrowNav(e) {
    const dirs = { ArrowRight: [1, 0], ArrowLeft: [-1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] };
    const d = dirs[e.key]; const cur = e.target.closest?.("[data-control-id]");
    if (!d || !cur) return;
    const R = S.profile.layout.rects, me = R[cur.dataset.controlId];
    const c = (r) => [r.x + r.w / 2, r.y + r.h / 2];
    const [mx, my] = c(me); let best = null, bs = 1e9;
    for (const id of deck.controlIds()) {
        if (id === cur.dataset.controlId) continue;
        const [x, y] = c(R[id]); const dx = x - mx, dy = y - my;
        const along = dx * d[0] + dy * d[1]; if (along <= 1) continue;
        const across = Math.abs(dx * d[1] + dy * d[0]);
        const score = along + across * 2.2;
        if (score < bs) { bs = score; best = id; }
    }
    if (best) { e.preventDefault(); $(`[data-control-id="${CSS.escape(best)}"]`).focus(); }
}
function selectControl(id) {
    S.control = id; S.draft = {};
    deck.setSelected(id); renderPanel();
}

/* ------------------------------------------------------------ right: panel */
function badge(e) {
    if (!e) return h("span", { class: "badge none" }, "Unassigned");
    if (e.source === "app") return h("span", { class: "badge app" }, isGlobalApp() ? "Set in Global" : "Set in " + appName(S.app));
    return h("span", { class: "badge global" }, "Inherited from Global");
}
function visibleActions() {
    const packs = new Set(appInfo(S.app)?.packs || S.pr?.raw?.packs || ["generic"]);
    S.actionList = [];
    return allActions().filter((p) => packs.has(p.id));
}
function actionDef(id) { return S.actions[id] || null; }

function packsSection() {
    if (isGlobalApp()) return h("div");
    const have = new Set(appInfo(S.app)?.packs || []);
    const sec = h("div", { class: "sec" }, h("h2", {}, "Action packs for this app"), h("div", { class: "muted", style: "margin-bottom:6px" }, "Tick a pack to offer its actions here."));
    for (const pk of allActions()) {
        const cb = h("input", { type: "checkbox", id: "pk-" + pk.id, style: "width:20px;height:20px" });
        cb.checked = have.has(pk.id);
        cb.addEventListener("change", async () => {
            const next = allActions().map((p) => p.id).filter((id) => (id === pk.id ? cb.checked : have.has(id)));
            try { await api.packs(S.app, next); setLast("Packs: " + next.join(", ")); toast("Packs: " + next.join(", "), { undo: true }); await loadApps(); renderApps(); renderPanel(); }
            catch (e) { cb.checked = !cb.checked; if (!netDown(e)) toast(e.message, { bad: true }); }
        });
        sec.append(h("label", { class: "chk", for: "pk-" + pk.id }, cb, h("span", {}, pk.label || pk.id)));
    }
    return sec;
}
function renderPanel() {
    const P = $("#panel");
    if (!S.pr) { P.replaceChildren(h("div", { class: "loading" }, "Loading…")); return; }
    if (!S.control) {
        P.replaceChildren(h("div", { class: "empty" }, h("h2", {}, "Assignment"), h("p", {}, "Select a control on the deck to see or change what it does in "), h("b", {}, isGlobalApp() ? "Global" : appName(S.app)), h("p", { class: "muted" }, "Use Tab and the arrow keys to move between controls, Enter to select.")), packsSection());
        return;
    }
    const e = eff(); const meta = ctlMeta(S.control);
    const head = h("div", { class: "p-head" },
        h("h3", {}, meta?.label || S.control),
        badge(e),
        h("div", { class: "where" }, `${isGlobalApp() ? "Global" : appName(S.app)} · bank ${S.bank} · ${meta?.kind || ""}`));
    const picker = h("select", { id: "actPick", "aria-label": "Action", onchange: (ev) => pickAction(ev.target.value) });
    if (!e) picker.append(h("option", { value: "" }, "Pick an action…"));
    for (const pk of visibleActions()) {
        const g = h("optgroup", { label: pk.label || pk.id });
        const ck = ctlMeta(S.control)?.kind, want = ck === "knob" ? "turn" : ck === "button" ? "press" : null;
        for (const a of pk.actions) { S.actions[a.id] = { ...a, pack: pk.id };
            if (want && a.kind && a.kind !== "both" && a.kind !== want && !(e && e.action === a.id)) continue;
            g.append(h("option", { value: a.id, selected: e && e.action === a.id }, a.label)); }
        if (g.children.length) picker.append(g);
    }
    const sec = h("div", { class: "sec" }, h("h2", {}, "Action"));
    if (!e) sec.append(h("div", { class: "pick-prompt" }, h("b", {}, "Nothing assigned here."), h("div", { class: "muted" }, "Pick an action to give this control a job in " + (isGlobalApp() ? "Global" : appName(S.app)) + ".")));
    sec.append(picker);
    if (e && e.source === "global" && !isGlobalApp()) sec.append(h("div", { class: "f-act" }, h("button", { type: "button", class: "btn btn-sm", onclick: overrideWhole }, "Set for this app")));
    if (e && e.source === "app") sec.append(h("div", { class: "f-act" }, h("button", { type: "button", class: "btn btn-sm btn-ghost", onclick: clearAssign }, isGlobalApp() ? "Unassign" : "Unassign (use Global)")));
    const parts = [head, sec];

    const a = e && actionDef(e.action);
    if (a) {
        const fieldsSec = h("div", { class: "sec", id: "fields" }, h("h2", {}, "Settings"));
        const sens = [], plain = [];
        for (const f of a.fields) (isSens(f) ? sens : plain).push(f);
        if (!plain.length && !sens.length) fieldsSec.append(h("div", { class: "muted" }, "This action has no settings."));
        for (const f of plain) fieldsSec.append(fieldRow(f, e));
        parts.push(fieldsSec);
        if (sens.length) {
            const ss = h("div", { class: "sec", id: "sens" }, h("h2", {}, "Sensitivity & acceleration"));
            ss.append((() => { const s = document.createElementNS(SVGNS, "svg"); s.id = "curve"; s.setAttribute("class", "curve"); s.setAttribute("viewBox", "0 0 320 130"); return s; })(), h("div", { class: "cap" }, "Indicative response: raw ticks per second (across) to output per second (up). The dot is your last turn."));
            for (const f of sens) ss.append(fieldRow(f, e));
            parts.push(ss);
        }
    }
    parts.push(packsSection());
    P.replaceChildren(...parts);
    if (a) { S.draft = {}; for (const [k, v] of Object.entries(e.fields || {})) S.draft[k] = v.value; drawCurve(); }
}
const SENS_KEYS = new Set(["detentThreshold", "sens", "gamma", "base", "linesPerDetent", "secondsPerStep", "maxDelta", "maxDrag", "jogSustainMs", "jogSustainSeconds", "accel"]);
const isSens = (f) => SENS_KEYS.has(f.key) || /^jogTier/.test(f.key) || f.sensitivity === true || f.group === "sensitivity" || f.tag === "sensitivity" || (f.tags || []).includes("sensitivity");

function fieldRow(f, e) {
    const fv = (e.fields && e.fields[f.key]) || { value: f.default, source: "default" };
    const row = h("div", { class: "field", "data-key": f.key });
    const id = "f-" + f.key;
    const head = h("div", { class: "f-head" }, h("label", { for: id }, f.label || f.key), h("span", { class: "tag", "data-role": "tag" }), h("span", { class: "saved", "data-role": "saved", "aria-hidden": "true" }, "✓ Saved"));
    const inRow = h("div", { class: "f-in" });
    const commit = (v) => { S.draft[f.key] = v; drawCurve(); queueValue(f.key, v, row); };
    let input;
    if (f.kind === "bool") {
        input = h("input", { type: "checkbox", id, role: "switch", style: "width:22px;height:22px", onchange: (ev) => commit(ev.target.checked) });
        input.checked = !!fv.value;
    } else if (f.kind === "enum" || f.kind === "combo") {
        const opts = (f.options || []).map((o) => (typeof o === "object" ? o : { value: o, label: o }));
        input = h("select", { id, onchange: (ev) => commit(ev.target.value) }, opts.map((o) => h("option", { value: o.value, selected: o.value === fv.value }, o.label ?? o.value)));
        if (fv.value == null || !opts.some((o) => o.value === fv.value)) input.prepend(h("option", { value: "", selected: true, disabled: true }, "Not set"));
    } else if (f.kind === "key") {
        input = keyCapture(id, fv.value, commit);
    } else if (f.kind === "stringList") {
        input = h("input", { type: "text", id, placeholder: "comma separated", oninput: (ev) => commit(ev.target.value.split(",").map((x) => x.trim()).filter(Boolean)) });
        input.value = (fv.value || []).join(", ");
    } else if (f.kind === "object") {
        input = h("input", { type: "text", id, class: "keycap", spellcheck: "false", placeholder: "JSON object", oninput: (ev) => {
            const er = $("[data-role=err]", row);
            try { const v = ev.target.value.trim() ? JSON.parse(ev.target.value) : null; er.hidden = true; commit(v); }
            catch (x) { er.textContent = "Not valid JSON."; er.hidden = false; }
        } });
        input.value = fv.value ? JSON.stringify(fv.value) : "";
    } else if (f.kind === "number" || f.kind === "int" || f.kind === "float") {
        input = h("input", { type: "number", id, min: f.min, max: f.max, step: f.step || "any", oninput: (ev) => {
            const n = parseFloat(ev.target.value); if (Number.isNaN(n)) return;
            const rg = $("input[type=range]", row); if (rg) rg.value = n; commit(n);
        } });
        input.value = fv.value ?? "";
    } else {
        input = h("input", { type: "text", id, spellcheck: "false", oninput: (ev) => commit(ev.target.value) });
        input.value = fv.value ?? "";
    }
    inRow.append(input);
    if ((f.kind === "number" || f.kind === "int" || f.kind === "float") && f.min != null && f.max != null) {
        const rg = h("input", { type: "range", min: f.min, max: f.max, step: f.step || "any", "aria-label": (f.label || f.key) + " slider", oninput: (ev) => { input.value = ev.target.value; commit(parseFloat(ev.target.value)); } });
        rg.value = fv.value ?? f.min; inRow.append(rg);
    }
    if (f.unit) inRow.append(h("span", { class: "unit" }, f.unit));
    row.append(head, inRow);
    if (f.help) row.append(h("div", { class: "f-help" }, f.help));
    row.append(h("div", { class: "err", "data-role": "err", role: "alert", hidden: true }));
    row.append(h("div", { class: "f-act", "data-role": "act" }));
    paintRow(row, f, fv);
    return row;
}
function paintRow(row, f, fv) {
    const inherited = fv.source !== "app";
    row.classList.toggle("inherited", inherited);
    const tag = $("[data-role=tag]", row);
    tag.className = "tag" + (fv.source === "global" ? " global" : "");
    tag.textContent = fv.source === "app" ? "set here" : fv.source === "global" ? "from Global" : "default";
    if (isGlobalApp() && fv.source !== "app") tag.textContent = "default";
    const act = $("[data-role=act]", row); act.replaceChildren();
    if (inherited && !isGlobalApp()) act.append(h("button", { type: "button", class: "btn btn-sm", onclick: () => queueValue(f.key, S.draft[f.key] ?? fv.value, row, true) }, "Set for this app"));
    if (!inherited) act.append(h("button", { type: "button", class: "btn btn-sm btn-ghost", onclick: () => resetValue(f, row) }, isGlobalApp() ? "Reset to default" : "Reset to inherited"));
}
function patchFields() {
    const e = eff(); if (!e) return;
    const a = actionDef(e.action); if (!a) return;
    for (const row of document.querySelectorAll("#panel .field")) {
        const f = a.fields.find((x) => x.key === row.dataset.key);
        const fv = e.fields[f.key]; if (!fv) continue;
        paintRow(row, f, fv);
        if (document.activeElement && row.contains(document.activeElement)) continue; // never fight the user's caret
        const inp = $("input:not([type=range]),select", row);
        if (inp && inp.type === "checkbox") inp.checked = !!fv.value;
        else if (inp && f.kind === "stringList") inp.value = (fv.value || []).join(", ");
        else if (inp && f.kind === "object") inp.value = fv.value ? JSON.stringify(fv.value) : "";
        else if (inp && !inp.classList.contains("keycap")) inp.value = fv.value ?? "";
        else if (inp) inp.value = fv.value || "Not set";
        const rg = $("input[type=range]", row); if (rg) rg.value = fv.value ?? rg.min;
        S.draft[f.key] = fv.value;
    }
    drawCurve();
}

/* key capture: focus, press chord, writes "Cmd+Alt+C"; Escape cancels */
const MODK = new Set(["Meta", "Control", "Alt", "Shift", "Fn"]);
function chordOf(ev) {
    let k = ev.key;
    if (/^Key[A-Z]$/.test(ev.code)) k = ev.code.slice(3); else if (/^Digit\d$/.test(ev.code)) k = ev.code.slice(5);
    else k = ({ ArrowLeft: "Left", ArrowRight: "Right", ArrowUp: "Up", ArrowDown: "Down", " ": "Space", Enter: "Return", Backspace: "Delete" })[k] || (k.length === 1 ? k.toUpperCase() : k);
    const m = [ev.metaKey && "Cmd", ev.ctrlKey && "Ctrl", ev.altKey && "Alt", ev.shiftKey && "Shift"].filter(Boolean);
    return [...m, k].join("+");
}
function keyCapture(id, value, commit) {
    const inp = h("input", { type: "text", id, class: "keycap", readonly: true, placeholder: "Click, then press a chord", "aria-describedby": id + "-h" });
    inp.value = value || "";
    let prev = inp.value;
    inp.addEventListener("focus", () => { prev = inp.value; inp.classList.add("capturing"); inp.value = ""; inp.placeholder = "Press a chord… (Esc cancels)"; });
    inp.addEventListener("blur", () => { inp.classList.remove("capturing"); if (!inp.value) inp.value = prev; inp.placeholder = "Click, then press a chord"; });
    inp.addEventListener("keydown", (ev) => {
        if (ev.key === "Tab") return;
        ev.preventDefault();
        if (ev.key === "Escape") { inp.value = prev; inp.blur(); return; }
        if (MODK.has(ev.key)) { inp.value = chordOf({ ...ev, key: "", code: "" }).replace(/\+$/, "") + "+"; return; }
        const c = chordOf(ev); inp.value = c; prev = c; commit(c); inp.blur();
    });
    return inp;
}

/* writes */
const timers = {};
function queueValue(key, value, row, now) {
    clearTimeout(timers[key]);
    timers[key] = setTimeout(() => saveValue(key, value, row), now ? 0 : 150);
}
async function ensureOwn() {
    const e = eff();
    if (e && e.source === "app") return;
    const a = actionDef(e.action);
    await api.assign(S.app, S.bank, S.control, { action: e.action, type: a?.type });
}
async function saveValue(key, value, row) {
    const f = actionDef(eff().action).fields.find((x) => x.key === key);
    const err = $("[data-role=err]", row); err.hidden = true;
    try {
        await ensureOwn();
        const r = await api.value(S.app, S.bank, S.control, key, value);
        await afterWrite(r, `${ctlMeta(S.control)?.label || S.control}: ${f.label} ${value}`);
        const sv = $("[data-role=saved]", row); sv.classList.add("show"); setTimeout(() => sv.classList.remove("show"), 1400);
    } catch (e) { if (!netDown(e)) { err.textContent = e.message; err.hidden = false; } }
}
async function resetValue(f, row) {
    const err = $("[data-role=err]", row); err.hidden = true;
    try { const r = await api.value(S.app, S.bank, S.control, f.key, null); await afterWrite(r, `${f.label} reset`); }
    catch (e) { if (!netDown(e)) { err.textContent = e.message; err.hidden = false; } }
}
async function pickAction(id) {
    if (!id) return;
    const a = actionDef(id);
    try { const r = await api.assign(S.app, S.bank, S.control, { action: id, type: a.type }); await afterWrite(r, `${ctlMeta(S.control)?.label || S.control} = ${a.label}`, true); }
    catch (e) { if (!netDown(e)) toast(e.message, { bad: true }); }
}
async function overrideWhole() {
    const e = eff();
    try { const r = await api.assign(S.app, S.bank, S.control, { action: e.action, type: actionDef(e.action)?.type }); await afterWrite(r, `${S.control} set for ${appName(S.app)}`, true); }
    catch (er) { if (!netDown(er)) toast(er.message, { bad: true }); }
}
async function clearAssign() {
    try { const r = await api.unassign(S.app, S.bank, S.control); await afterWrite(r, `${S.control} unassigned`, true); }
    catch (er) { if (!netDown(er)) toast(er.message, { bad: true }); }
}
async function afterWrite(r, label, full) {
    const l = (r && r.label) || label;
    setLast(l); toast("Saved: " + l, { undo: true });
    await loadPreset(); await loadApps();
    renderApps(); renderBanks(); renderDeck();
    if (full) renderPanel(); else { renderHead(); patchFields(); }
}
function renderHead() {
    const b = $("#panel .p-head .badge"); if (b) b.replaceWith(badge(eff()));
}
async function doUndo() { await stepHistory(api.undo, "Undone", "redo"); }
async function doRedo() { await stepHistory(api.redo, "Redone", "undo"); }
async function stepHistory(fn, word, other) {
    try {
        const r = await fn();
        if (r && r.ok === false) { toast(r.label || "Nothing to " + (word === "Undone" ? "undo" : "redo"), {}); return; }
        setLast(`${word}: ${r.label || ""}`);
        toast(`${word}: ${r.label || "last change"}`, other === "redo" ? { redo: true } : { undo: true });
        await loadPreset(); await loadApps(); renderApps(); renderBanks(); renderDeck(); renderPanel();
    } catch (e) { if (!netDown(e)) toast(e.message, { bad: true }); }
}

/* ------------------------------------------------------------ curve */
function curveFn(v) {
    const step = v.secondsPerStep ?? v.linesPerDetent ?? v.sens ?? v.base ?? 1;
    const g = v.gamma ?? 1, ref = 20, thr = v.detentThreshold ?? 1;
    return (t) => (t <= 0 ? 0 : (step / thr) * t * (t > ref ? Math.pow(t / ref, Math.max(0, g - 1)) : 1));
}
function drawCurve() {
    const svg = $("#curve"); if (!svg) return;
    const f = curveFn(S.draft), W = 320, H = 130, L = 30, B = 18, T = 8, R = 8, MAXT = 80;
    const ys = []; for (let t = 0; t <= MAXT; t += 2) ys.push(f(t));
    const ymax = Math.max(1e-6, ...ys, S.lastEv?.out?.value ? 0 : 0) * 1.1;
    const X = (t) => L + (t / MAXT) * (W - L - R), Y = (y) => T + (1 - y / ymax) * (H - T - B);
    const pts = ys.map((y, i) => `${X(i * 2).toFixed(1)},${Y(y).toFixed(1)}`).join(" ");
    const unit = (actionDef(eff()?.action)?.fields.find((x) => x.unit && isSens(x))?.unit || "").split(" /")[0];
    const kids = [
        sv("line", { class: "ax", x1: L, y1: T, x2: L, y2: H - B }), sv("line", { class: "ax", x1: L, y1: H - B, x2: W - R, y2: H - B }),
        sv("text", { x: L, y: H - 4 }, "0"), sv("text", { x: X(40), y: H - 4, "text-anchor": "middle" }, "40 ticks/s"), sv("text", { x: W - R, y: H - 4, "text-anchor": "end" }, "80"),
        sv("text", { x: L - 4, y: T + 7, "text-anchor": "end" }, ymax.toFixed(ymax < 10 ? 1 : 0)), sv("text", { x: L + 4, y: T + 8 }, unit ? unit + "/s" : "out/s"),
        sv("polyline", { class: "ln", points: pts })];
    const ev = S.lastEv;
    if (ev && ev.control === S.control && ev.kind === "turn") kids.push(sv("circle", { class: "pt", cx: X(Math.min(MAXT, ev.ticksPerSec)), cy: Y(Math.min(ymax, f(ev.ticksPerSec))), r: 4 }));
    svg.replaceChildren(...kids);
}

/* ------------------------------------------------------------ live test */
const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;
function onEvent(ev) {
    S.lastEv = ev;
    const hist = (S.hist[ev.control] = S.hist[ev.control] || []);
    if (ev.kind === "turn") { hist.push({ t: performance.now(), v: ev.ticksPerSec }); if (hist.length > 120) hist.shift(); }
    if (ev.kind === "turn") fx.turn(ev.control, ev.rawTicks, ev.ticksPerSec);
    else if (ev.kind === "press") fx.press(ev.control, true);
    else if (ev.kind === "release") fx.press(ev.control, false);
    if (ev.presetId && ev.presetId !== S.app) flashApp(ev.presetId);
    if (ev.kind === "turn" && ev.control === S.control) drawCurve();
}
function renderReadout() {
    const box = $("#readout"), ev = S.lastEv;
    if (!ev) return;
    const m = ctlMeta(ev.control);
    const other = ev.presetId && ev.presetId !== S.app;
    const handler = ev.presetId === GLOBAL || ev.source === "global" ? "global" : "app";
    const hb = !ev.presetId ? h("span", { class: "badge none" }, "No assignment (nothing handled it)") : h("span", { class: "badge " + (handler === "global" ? "global" : "app") }, "Handled by " + appName(ev.presetId));
    const sparkId = (ctlMeta(S.control)?.kind === "knob" ? S.control : ev.control);
    const grid = h("div", { class: "ro-grid" },
        h("div", { class: "ro-ctl" }, m?.label || ev.control, h("small", {}, `${ev.kind} · bank ${ev.bank || "?"} · ${ev.action || "no action"}`), h("div", { style: "margin-top:6px" }, hb, other ? h("span", { class: "muted", style: "margin-left:6px;font-size:11px" }, `not ${appName(S.app)}`) : null)),
        stat("Raw ticks", ev.kind === "turn" ? (ev.rawTicks > 0 ? "+" : "") + ev.rawTicks : "—"),
        stat("Ticks/s", ev.kind === "turn" ? String(ev.ticksPerSec) : "—"),
        stat("Accel", ev.kind === "turn" ? "×" + (ev.accel ?? 1) : "—"),
        stat("Fine mode", ev.fine ? "ON" : "off"),
        h("div", { class: "ro-out" },
            h("div", { class: "stat" }, h("small", {}, "Output"), h("span", { class: "big" }, ev.out ? ev.out.text : "—")),
            h("div", { class: "stat" }, h("small", {}, "Unit"), h("b", {}, ev.out?.unit || "—")),
            h("div", { class: "spark-box", id: "spark" })));
    box.replaceChildren(h("div", { class: "ro-head" }, h("b", {}, "Live test"), h("span", { class: "muted" }, ev.kind === "turn" ? `ticks/s history for ${sparkId}` : "last input")), grid);
    S.sparkId = sparkId; drawSpark();
}
const stat = (l, v) => h("div", { class: "stat" }, h("small", {}, l), h("b", {}, v));
function drawSpark() {
    const host = $("#spark"); if (!host) return;
    const hist = (S.hist[S.sparkId] || []).filter((p) => performance.now() - p.t < 6000);
    const W = 260, H = 44, now = performance.now();
    const pts = hist.map((p) => `${(W - ((now - p.t) / 6000) * W).toFixed(1)},${(H - 4 - (Math.min(80, p.v) / 80) * (H - 10)).toFixed(1)}`).join(" ");
    const poly = sv("polyline", { points: pts, "vector-effect": "non-scaling-stroke" });
    const g = sv("svg", { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none", class: "spark", style: "width:100%;height:100%" }); g.append(poly);
    host.replaceChildren(g, h("span", { class: "spark-l" }, "ticks/s, last 6 s"));
}
async function pollEvents() {
    try {
        const r = await api.events(S.since);
        const first = S.since === 0;
        S.since = r.next ?? S.since;
        if (!first) for (const ev of r.events || []) onEvent(ev);
        if (!first && (r.events || []).length) renderReadout();
        S.apiDown = 0; $("#apiBanner").hidden = true;
    } catch (e) { if (++S.apiDown >= 3) netDown(e); }
    setTimeout(pollEvents, 80);
}
async function pollLive() {
    try {
        S.live = await api.live();
        document.querySelectorAll(".app-row").forEach((r) => r.querySelector(".dot").classList.toggle("on", r.dataset.app === S.live.frontmost?.id));
        $("#liveApp").textContent = S.live.frontmost ? "front: " + S.live.frontmost.name : "";
    } catch (e) { /* events poll owns the banner */ }
    setTimeout(pollLive, 1000);
}
setInterval(() => { if (S.lastEv && S.hist[S.sparkId]?.length) drawSpark(); }, 250);

/* ------------------------------------------------------------ boot */
function fail(e) { if (!netDown(e)) toast(e.message, { bad: true }); }
async function init() {
    $("#apiBanner").hidden = true;
    try {
        const [d, reg] = await Promise.all([api.deck(), api.registry()]);
        S.profile = d.profile || d; S.registry = reg;
        for (const pk of allActions()) for (const a of pk.actions) S.actions[a.id] = { ...a, pack: pk.id };
        await loadApps();
    } catch (e) { $("#deckHost").replaceChildren(h("div", { class: "empty" }, "Deck unavailable.")); return fail(e); }
    const usePhoto = !QS.has("schematic");
    if (usePhoto) S.profile = photoProfile(S.profile);
    deck = createDeckMap($("#deckHost"), S.profile, { onSelect: selectControl });
    if (usePhoto) applyPhoto(deck.svg, QS.has("calib"));
    fx = decorateDeck(deck, S.profile);
    deck.svg.addEventListener("keydown", arrowNav);
    renderApps();
    if (!S.apps.length) { $("#panel").replaceChildren(h("div", { class: "empty" }, "No apps yet. Use + Add app to create the first preset.")); return; }
    await selectApp(S.apps.find((a) => a.id.includes("DaVinciResolve"))?.id || S.apps[0].id);
}
$("#retryBtn").addEventListener("click", init);
$("#undoBtn").addEventListener("click", doUndo);
$("#redoBtn").addEventListener("click", doRedo);
$("#testSwitch").addEventListener("change", (e) => { S.test = e.target.checked; document.body.classList.toggle("test-mode", S.test); });
$("#inhSwitch").addEventListener("change", (e) => { S.hideInh = !e.target.checked; fx && fx.setInheritedHidden(S.hideInh); });
document.addEventListener("keydown", (e) => { if ((e.metaKey || e.ctrlKey) && e.key === "z" && !/INPUT|SELECT/.test(document.activeElement.tagName)) { e.preventDefault(); e.shiftKey ? doRedo() : doUndo(); } });
wireAdd();
init().then(() => { pollEvents(); pollLive(); });
window.__editor = { S, selectApp, selectControl };
