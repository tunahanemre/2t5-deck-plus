// inspector.js: field rows (shared by the essentials inspector and the Sensitivity / Advanced drawers),
// the essentials inspector, the action picker, the sensitivity curve card.
import { h, s, icon, clamp, fmt, compact, loop, Spring, reduced, store, debounce, bankLabel } from "./util.js";
import { SMODES, isPControl, modeOn, modeBank, smodeLabel, S, GLOBAL, eff, ctlMeta, actionDef, appName, isGlobalApp, visiblePacks, PACK_ICON, actionIcon, compat, groupFields, curveFn, appInfo } from "./state.js";
import { HOT, SLIDER_DOTS } from "./hotspots.js";
import { plain, plainUnit, plainControl, titleFor } from "./plain.js";
import { api } from "./api.js";
import { openMenu, selectField, stepper, slider, segmented, switchCtl, keyCapture, textField, listEditor, stateChip, KIND, keycaps, popover } from "./ui.js";

/* ------------------------------------------------------------ one field -> its control */
const normOpts = (f) => (f.options || []).map((o) => (typeof o === "object" ? { value: o.value, label: o.label ?? String(o.value) } : { value: o, label: String(o) }));
const isNum = (f) => f.kind === "number" || f.kind === "int" || f.kind === "float";

function pressOverride(f, value, commit) {
    // "On knob press": an action picker, never raw JSON. null = default, {type:"none"} = disabled, {type:"key",key}.
    const wrap = h("div", { class: "ov" });
    let cur = value && typeof value === "object" ? value : null;
    const mode = () => (!cur ? "default" : cur.type === "none" ? "none" : cur.type === "key" ? "key" : "custom");
    const custom = cur && !["none", "key"].includes(cur.type) ? cur : null;
    const opts = [{ value: "default", label: "Default press action", icon: "circle-dashed" }, { value: "key", label: "Press a shortcut", icon: "keyboard" }, { value: "none", label: "Disabled", icon: "ban" }];
    // the registry's own name for the press type (never the raw identifier such as selectWheel)
    const typeName = (t) => normOpts(f).find((o) => o.value === t)?.label || Object.values(S.actions).find((a) => a.type === t)?.label || String(t).replace(/([a-z])([A-Z])/g, "$1 $2").replace(/^./, (c) => c.toUpperCase()).replace(/ ([A-Z])(?=[a-z])/g, (m, c) => " " + c.toLowerCase());
    if (custom) opts.push({ value: "custom", label: typeName(custom.type), icon: "puzzle" });
    const sel = selectField({ options: opts, value: mode(), ariaLabel: f.label, onChange: (m) => {
        if (m === "default") { cur = null; commit(null); } else if (m === "none") { cur = { type: "none" }; commit(cur); } else if (m === "key") { cur = { type: "key", key: cur?.key || "" }; paint(); if (cur.key) commit(cur); } else if (m === "custom") { cur = custom; commit(cur); }
        paint();
    } });
    const slot = h("div", { class: "ov-slot" });
    wrap.append(sel.el, slot);
    function paint() {
        slot.replaceChildren();
        if (cur && cur.type === "key") { const kc = keyCapture({ value: cur.key, label: f.label, onCommit: (c) => { cur = { type: "key", key: c || "" }; if (c) commit(cur); } }); slot.append(kc.el); }
    }
    paint();
    return { el: wrap, set(v) { cur = v && typeof v === "object" ? v : null; sel.set(mode()); paint(); } };
}

/** returns {el, set(v), setErr(bool)} */
function buildControl(f, fv, ctx, setMsg) {
    const label = f.label || f.key;
    const send = (v) => ctx.queueValue(f.key, v);
    if (f.kind === "bool") {
        const sw = switchCtl({ checked: !!fv.value, label, text: undefined, onChange: (v) => send(v) });
        return { el: sw.el, set: (v) => sw.set(v) };
    }
    if (f.kind === "enum" || f.kind === "combo") {
        let opts = normOpts(f);
        if (f.kind === "combo" && fv.value != null && !opts.some((o) => o.value === fv.value)) opts = [...opts, { value: fv.value, label: String(fv.value) }];
        const short = f.kind === "enum" && opts.length > 1 && opts.length <= 4 && opts.every((o) => o.label.length <= 9);
        if (short) { const sg = segmented({ options: opts, value: fv.value, label, onChange: send }); return { el: sg.el, set: (v) => sg.set(v) }; }
        const sel = selectField({ options: opts, value: fv.value, placeholder: "Not set", ariaLabel: label, onChange: send });
        return { el: sel.el, set: (v) => sel.set(v) };
    }
    if (f.kind === "key") {
        const kc = keyCapture({ value: fv.value, label, onCommit: (c) => { if (c) send(c); else ctx.resetValue(f.key); } });
        return { el: kc.el, set: (v) => kc.set(v), setErr: (m) => kc.setError(m) };
    }
    if (f.kind === "stringList") { const le = listEditor({ values: fv.value, label, onChange: send }); return { el: le.el, set: (v) => le.set(v) }; }
    if (f.kind === "object") { const po = pressOverride(f, fv.value, send); return { el: po.el, set: (v) => po.set(v) }; }
    if (f.key === "hue" && f.kind === "int" && f.min === 0 && f.max === 7) {
        // the eight hues printed under the faders, not a bare index
        let cur = Number(fv.value);
        const wrap = h("div", { class: "hue-sw", role: "radiogroup", "aria-label": label });
        const btns = SLIDER_DOTS.map(([name, col], i) => { const b = h("button", { type: "button", class: "hue-dot", role: "radio", "aria-checked": "false", "aria-label": name[0].toUpperCase() + name.slice(1), "data-tip": name[0].toUpperCase() + name.slice(1), tabindex: "-1", onclick: () => pick(i, true) }); b.style.setProperty("--c", col); b.addEventListener("keydown", (e) => { const d = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0; if (d) { e.preventDefault(); pick((cur + d + 8) % 8, true); btns[(cur + d + 8) % 8].focus(); } }); wrap.append(b); return b; });
        function paint() { btns.forEach((b, i) => { const on = i === cur; b.setAttribute("aria-checked", String(on)); b.tabIndex = on || (!(cur >= 0 && cur < 8) && i === 0) ? 0 : -1; b.classList.toggle("on", on); }); }
        function pick(i, user) { cur = i; paint(); if (user) send(i); }
        paint();
        return { el: wrap, set: (v) => { cur = Number(v); paint(); } };
    }
    if (isNum(f)) {
        const unit = f.unit === "seconds" ? (S.unit[f.key] || store.get("unit." + f.key, "seconds")) : null;
        const factor = unit === "minutes" ? 60 : 1;
        const step = f.kind === "int" ? Math.max(1, f.step || 1) : unit === "minutes" ? 0.5 : f.step || 1;
        const lo = f.min != null ? f.min / factor : undefined, hi = f.max != null ? f.max / factor : undefined;
        const toDisp = (v) => (v == null ? v : Number((v / factor).toFixed(4)));
        const toCanon = (v) => Number((v * factor).toFixed(4));
        let sl = null;
        const st = stepper({ value: toDisp(fv.value), min: lo, max: hi, step, label, onChange: (v) => { if (sl) sl.set(v); send(toCanon(v)); }, onInvalid: (m) => setMsg(m) });
        const row = h("div", { class: "numrow" });
        if (lo != null && hi != null) {
            const gvv = ctx.ghostFor ? ctx.ghostFor(f.key) : null;
            sl = slider({ value: toDisp(fv.value) ?? lo, min: lo, max: hi, step, def: f.default != null ? toDisp(f.default) : undefined, ghost: gvv != null ? toDisp(gvv) : undefined, label, onInput: (v) => { st.set(v); send(toCanon(v)); setMsg(null); } });
            row.append(sl.el);
        }
        if (f.unit && f.unit !== "seconds") st.el.insertBefore(h("span", { class: "st-unit", "aria-hidden": "true" }, plainUnit(f.unit).replace(/^(\w+) per notch$/, "$1/notch")), st.el.lastChild);
        row.append(st.el);
        return { el: row, set: (v) => { st.set(toDisp(v)); if (sl) sl.set(toDisp(v) ?? lo); } };
    }
    const tf = textField({ value: fv.value, label, mono: f.key === "cmd" || f.key === "wheel" || f.key === "property", placeholder: f.key === "property" ? ((g) => (Array.isArray(g) && g.length ? `Using the gang: ${g.join(" + ")}` : "Choose a property"))(eff()?.fields?.gang?.value) : f.required ? "Required" : "", onInput: (v) => send(v) });
    if (f.key === "property") tf.el.append(icon("chevron-down", 16, "tf-chev"));
    return { el: tf.el, set: (v) => tf.set(v) };
}

/* ------------------------------------------------------------ unit chip (static, or a converting menu for seconds) */
function unitChip(f, rebuild) {
    if (!f.unit) return null;
    if (f.unit !== "seconds") return isNum(f) ? null : h("span", { class: "unit" }, plainUnit(f.unit));
    const cur = S.unit[f.key] || store.get("unit." + f.key, "seconds");
    const b = h("button", { type: "button", class: "unit btn-unit", "aria-haspopup": "listbox", "aria-expanded": "false", "aria-label": "Unit: " + cur }, h("span", {}, cur), icon("chevron-down"));
    b.addEventListener("click", () => openMenu({ anchor: b, minWidth: 140, groups: [{ items: [{ value: "seconds", label: "seconds" }, { value: "minutes", label: "minutes" }] }], value: cur, ariaLabel: "Unit", placement: "bottom-end",
        onPick: (v) => { S.unit[f.key] = v; store.set("unit." + f.key, v); rebuild(); } }));
    return b;
}

/* ------------------------------------------------------------ a full field row */
/** mode: "compact" (essentials) | "full" (drawers: help line + explicit reset text button) */
export function fieldRow(f, fv, ctx, mode = "compact", readOnly = false) {
    const el = h("div", { class: "frow" + (readOnly ? " ro" : ""), "data-key": f.key });
    const msg = h("div", { class: "fr-err", role: "status", "aria-live": "polite", hidden: true });
    let ctl = null;
    const setMsg = (m) => { msg.hidden = !m; msg.replaceChildren(m ? icon("triangle-alert") : "", m || ""); ctl && ctl.el && ctl.el.classList && el.classList.toggle("bad", !!m); };
    const lab = h("label", { class: "fr-label" }, f.label || f.key);
    if (f.help && mode === "compact") { lab.setAttribute("data-tip", f.help); }
    const head = h("div", { class: "fr-head" }, lab);
    const ctlBox = h("div", { class: "fr-ctl" });
    const foot = h("div", { class: "fr-foot" });
    const help = f.help && mode === "full" ? h("div", { class: "fr-help" }, f.help) : null;
    el.append(...[head, ctlBox, msg, help, foot].filter(Boolean));
    let last = fv;
    function build() {
        ctl = buildControl(f, last, ctx, setMsg);
        ctlBox.replaceChildren(ctl.el);
        const old = head.querySelector(".unit"); if (old) old.remove();
        const u = unitChip(f, build); if (u) head.append(u);
        { const rb = head.querySelector(".fr-reset"); if (rb) head.append(rb); } // the reset glyph is always the last item in the row head
        if (ctl.el.querySelector) { const first = ctl.el.matches("input,button,[tabindex]") ? ctl.el : ctl.el.querySelector("input,button,[tabindex]"); if (first && !first.id) { first.id = "fc-" + f.key + "-" + mode; lab.setAttribute("for", first.id); } }
        if (readOnly) ctlBox.setAttribute("inert", "");
    }
    function paintProv() {
        foot.replaceChildren();
        const g = isGlobalApp();
        const src = last.source;
        const same = (a, b) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
        const differs = src === "app" && !same(last.value, f.default);
        const tip = g ? "Reset to default" : "Reset to inherited";
        /* one "Set here" chip lives in the inspector header; a field only offers a 24px reset glyph (always when it differs from the default, otherwise on row hover or focus) */
        if (src === "app" && !readOnly) { if (!head.querySelector(".fr-reset")) head.append(h("button", { type: "button", class: "icon-btn xs fr-reset", "data-tip": tip, "aria-label": `${f.label}: reset. ${tip}`, onclick: () => ctx.resetValue(f.key) }, icon("rotate-ccw"))); }
        else if (src === "global") foot.append(h("span", { class: "chip st-inh" }, icon("link", 16, "c-ic"), h("span", {}, "Inherited \u00b7 Global")));
        if (!(src === "app" && !readOnly)) head.querySelector(".fr-reset")?.remove();
        foot.hidden = !foot.childNodes.length;
        el.classList.toggle("is-set", src === "app"); el.classList.toggle("differs", differs);
    }
    build(); paintProv();
    const row = {
        el, key: f.key,
        patch(nfv, force) {
            if (!nfv) return;
            const changedSrc = nfv.source !== last.source;
            last = nfv;
            if (force || !el.contains(document.activeElement) || changedSrc) ctl.set(nfv.value);
            if (changedSrc) paintProv();
        },
        flash() { el.classList.remove("flash"); void el.offsetWidth; el.classList.add("flash"); },
        setError(m) { setMsg(m); if (ctl && ctl.setErr) ctl.setErr(m); if (m) el.scrollIntoView({ block: "nearest" }); },
        focus() { (ctlBox.querySelector("input,button,[tabindex]") || el).focus(); },
    };
    ctx.rows.push(row);
    return el;
}

/* ------------------------------------------------------------ action picker */
export function actionPicker(ctx, e, ctl) {
    const cur = e ? actionDef(e.action) : null;
    const kind = HOT[S.control]?.kind || "key";
    const sub = cur ? String(cur.help || "").split(/(?<=[.!?])\s/)[0] : "Pick what this control does";
    const txt = h("span", { class: "ap-txt" }, h("span", { class: "ap-t" }, cur ? cur.label : "Choose an action\u2026"), sub ? h("span", { class: "ap-d" }, sub) : null);
    const btn = h("button", { type: "button", class: "picker" + (cur ? "" : " unset"), "aria-haspopup": "listbox", "aria-expanded": "false", "aria-label": "Action: " + (cur ? cur.label : "none") }, h("span", { class: "ap-tile", "aria-hidden": "true" }, icon(cur ? actionIcon(cur.id) : "library", 20, "ap-ic")), txt, icon("chevron-down", 16, "ap-chev"));
    btn.addEventListener("click", () => {
        const ck = ctlMeta(S.control)?.kind;
        const groups = visiblePacks().map((pk) => ({ label: pk.label, icon: PACK_ICON[pk.id] || "shapes", items: pk.actions.filter((a) => compat(a, ck) || (e && e.action === a.id)).map((a) => ({ value: a.id, label: a.label, sub: String(a.help || "").split(/(?<=[.!?])\s/)[0], icon: actionIcon(a.id), caps: (a.fields || []).find((x) => x.kind === "key" && typeof x.default === "string" && x.default)?.default })) })).filter((g) => g.items.length);
        const foot = [{ label: "Browse library…", icon: "library", onClick: () => ctx.openDrawer("library") }];
        if (e && e.source === "app") foot.push({ label: modeOn(S.control) ? "Unassign, use base" : isGlobalApp() ? "Unassign" : "Unassign, use Global", confirm: "Confirm unassign", icon: "trash-2", tone: "danger", onClick: () => ctx.clearAssign() });
        openMenu({ anchor: btn, matchWidth: true, minWidth: 320, groups, value: e ? e.action : null, search: true, searchPlaceholder: "Search actions", ariaLabel: "Actions", footer: foot, emptyText: "No actions match", onPick: (v) => ctx.pickAction(v) });
    });
    return btn;
}

/** a 40px tile carrying the control's kind as a Lucide glyph (the photo stays the only place the hardware is drawn) */
function kindTile(kind) {
    const k = KIND[kind || "key"] || KIND.key;
    return h("span", { class: "kind-tile", "aria-hidden": "true" }, icon(k[0], 24));
}
/* ------------------------------------------------------------ ownership: Inherit Global | Custom | Off here, plus the resolution trace */
const labelOf = (id) => (actionDef(id) ? actionDef(id).label : String(id || ""));
const ctx0modeBank = () => modeBank(S.sMode);
function resolutionTrace(e, g) {
    const bank = S.bank, bl = bankLabel(bank), me = appName(S.app);
    if (e && e.baseFallback) { const s0 = `Not set for ${smodeLabel(S.sMode)} mode, so the base assignment applies: ${labelOf(e.action)}.`; const el0 = h("div", { class: "trace", role: "note" }, s0); el0.setAttribute("aria-label", s0); return el0; }
    if (modeOn(S.control) && S.pr?.eff?.[ctx0modeBank()]?.[S.control]) { const s1 = `Set for ${smodeLabel(S.sMode)} mode: ${labelOf(e.action)}.`; const el1 = h("div", { class: "trace", role: "note" }, s1); el1.setAttribute("aria-label", s1); return el1; }
    const raw = S.pr?.raw?.banks?.[bank]?.[S.control];
    const kind = (KIND[HOT[S.control]?.kind || "key"] || KIND.key)[1].toLowerCase();
    // the chain stays available as hover detail; the sentence is what people read
    const chain = [];
    let sentence;
    if (raw && raw.action === "generic.none") { sentence = g ? `Turned off in Global, so this ${kind} does nothing.` : `Turned off in ${me}, so this ${kind} does nothing.`; chain.push(`${me} \u00b7 ${bl}: off`); }
    else if (raw) { sentence = `Set in ${g ? "Global" : me}: ${labelOf(raw.action || (e && e.action))}.`; chain.push(`${g ? "Global" : me} \u00b7 ${bl}: ${labelOf(raw.action || (e && e.action))}`); }
    else if (e && e.source === "global" && (!g || e.baseFallback)) { sentence = `Not set in ${me}, so Global\u2019s \u201c${labelOf(e.action)}\u201d applies.`; chain.push(`${me} \u00b7 ${bl}: not set`, `Global \u00b7 ${bl}: ${labelOf(e.action)}`); }
    else if (e) { sentence = `Not set in ${g ? "Global" : `${me} or Global`}; the built-in default \u201c${labelOf(e.action)}\u201d applies.`; chain.push(`${me} \u00b7 ${bl}: not set`, "Global: not set", `Default: ${labelOf(e.action)}`); }
    else { sentence = g ? `Nothing is set in Global, so this ${kind} does nothing.` : `Nothing is set in ${me} or Global, so this ${kind} does nothing.`; chain.push(`${me} \u00b7 ${bl}: not set`, "Global: not set", "Nothing fires"); }
    const el = h("div", { class: "trace", role: "note", "data-tip": chain.join("  \u2192  ") }, sentence);
    el.setAttribute("aria-label", sentence + " " + chain.join(", then "));
    return el;
}
/** a control that clicks or drags at a calibrated screen spot shows how trustworthy that spot is; the chip opens the calibration */
function calChip(ctx) {
    const c = ctx.calChip && ctx.calChip(S.bank, S.control); if (!c) return null;
    const [tone, txt] = c.status === "calibrated" ? ["ok", "Calibrated"] : c.status === "needs_calibration" ? ["bad", "Not calibrated"] : c.status === "stale" ? ["warn", "Needs calibration for this layout"] : c.status === "legacy" ? ["old", "Old-style calibration"] : c.status === "imported" ? ["old", "Imported from ld-resolve, not re-checked"] : ["idle", "Calibration not ready"];
    return h("button", { type: "button", class: "chip cal-chip " + tone, "data-tip": "Open the mouse calibration", onclick: (e) => { ctx.openDrawer("calibration", e.currentTarget); setTimeout(() => ctx.calibration.scrollTo(c.key), 400); } }, h("i", { class: "cal-dot " + tone, "aria-hidden": "true" }), h("span", {}, txt));
}
/** what the action touches inside Resolve, in words */
const WRITES = { "resolve.property": "Writes to the clip", "resolve.wheel": "Writes to the clip", "resolve.cdl": "Writes to the clip", "resolve.balance": "Writes to the clip", "resolve.addMarker": "Writes to the timeline", "resolve.markerColor": "Writes to the timeline", "resolve.markerAtPlayhead": "Writes to the timeline", "resolve.quickExport": "Starts a render" };
function writesChip(e) { const w = e && WRITES[e.action]; return w ? h("span", { class: "chip", "data-tip": `${w}. This action changes your DaVinci Resolve project.` }, icon("pencil-line", 16, "c-ic"), h("span", {}, w)) : null; }
/** the control's printed legend vs what it really does */
function legendNote(e, meta, kind) {
    if (!e || !meta) return null;
    const v = e.fields && (e.fields.property?.value || e.fields.wheel?.value); if (!v) return null;
    const written = plain(String(v)).toLowerCase(), printed = String(meta.label || "").toLowerCase();
    if (!printed || written.includes(printed) || printed.includes(written)) return null;
    return h("p", { class: "legend-note" }, `Printed legend: ${meta.label}. This ${kind[1].toLowerCase()} writes ${plain(String(v))}.`);
}
function ownership(ctx, e, g) {
    const own = !!e && e.source === "app", off = own && e.action === "generic.none";
    const mb = modeOn(S.control), opts = g && !mb ? [{ value: "inherit", label: "Not set" }, { value: "custom", label: "Custom" }] : [{ value: "inherit", label: mb ? "Inherit base" : "Inherit Global" }, { value: "custom", label: "Custom" }, ...(actionDef("generic.none") ? [{ value: "off", label: "Off here" }] : [])];
    const seg = segmented({ options: opts, value: off ? "off" : own ? "custom" : "inherit", label: "Who decides this control", onChange: (m) => ctx.setOwnership(m) });
    return h("section", { class: "in-sec own" }, h("div", { class: "sec-h" }, "Source"), seg.el, resolutionTrace(e, g));
}

/* ------------------------------------------------------------ a new assignment that still needs a value (a key, a command, a modifier) */
function draftSection(ctx, pend) {
    const a = actionDef(pend.action), need = a.fields.filter((f) => f.required && f.default == null), vals = pend.vals;
    const complete = () => need.every((f) => vals[f.key] != null && vals[f.key] !== "");
    const save = h("button", { type: "button", class: "btn primary", disabled: true, onclick: () => ctx.commitPending() }, icon("check"), "Assign");
    const auto = need.length === 1 && ["key", "enum"].includes(need[0].kind);
    const dctx = { rows: [], resetValue: () => {}, ghostFor: () => null, ghostValues: () => null, openDrawer: ctx.openDrawer, queueValue: (k, v) => { if (v === "" || v == null) delete vals[k]; else vals[k] = v; save.disabled = !complete(); if (auto && complete()) ctx.commitPending(); } };
    const sec = h("section", { class: "in-sec draft" }, h("div", { class: "sec-h" }, "Finish setting this up"), h("p", { class: "draft-note" }, need.length > 1 ? "This action needs a few values before it can be saved." : "This action needs a value before it can be saved."));
    for (const f of need) sec.append(fieldRow(f, { value: vals[f.key] ?? null, source: "default" }, dctx, "compact", false));
    sec.append(h("div", { class: "draft-act" }, h("button", { type: "button", class: "btn text", onclick: () => ctx.cancelPending() }, "Cancel"), auto ? null : save));
    return sec;
}

/** which layer of this P slider / button is being edited: Base (every mode), or one slider mode (Hue | Sat | Lum). A mode with nothing of its own inherits the base assignment and says so. */
function modeSection(ctx, e) {
    const opts = [{ value: "base", label: "Base" }, ...SMODES];
    const seg = segmented({ options: opts, value: S.editBase ? "base" : S.sMode, label: "Slider mode layer", onChange: (m) => ctx.setSliderMode(m) });
    const hold = ctx.heldLayer && ctx.heldLayer();
    const note = S.editBase ? "Base: what this control does in every slider mode that has nothing of its own."
        : e && e.baseFallback ? `${smodeLabel(S.sMode)} mode has nothing of its own for this control, so the base assignment applies. Choose an action to give ${smodeLabel(S.sMode)} mode its own.`
        : `${smodeLabel(S.sMode)} mode has its own assignment here. Clear it to go back to the base.`;
    return h("section", { class: "in-sec mode-sec" }, h("div", { class: "sec-h" }, "Slider mode"), seg.el, h("p", { class: "mode-note" }, note));
}

/* ------------------------------------------------------------ the essentials inspector */
export function renderInspector(ctx) {
    ctx.rows.length = 0;
    const frag = [];
    if (!S.pr) return [h("div", { class: "empty" }, h("div", { class: "sk sk-l" }), h("div", { class: "sk sk-c" }), h("div", { class: "sk sk-c" }))];
    if (!S.control) {
        const rn = S.reqNote && S.reqNote(S.app);
        return [...(rn ? [rn] : []), h("div", { class: "empty" }, icon("mouse-pointer-click", 32, "em-ic"), h("h2", {}, "Select a control"), h("p", {}, "Click a knob, key or slider on the deck, or press it on the device while Test mode is on."),
            h("button", { type: "button", class: "btn ghost sm", onclick: () => ctx.openDrawer("library") }, icon("library"), "Browse the action library"))];
    }
    const e = eff(), meta = ctlMeta(S.control), hs = HOT[S.control];
    const kind = KIND[hs ? hs.kind : "key"] || KIND.key;
    const g = isGlobalApp();
    const state = !e ? "none" : e.source === "app" ? "set" : "inh";
    const isOff = !!e && e.source === "app" && e.action === "generic.none";
    const stateText = !e ? "Unassigned" : e.baseFallback ? "Inherits base" : isOff ? `Off in ${appName(S.app)}` : e.source === "app" ? (g ? "Set in Global" : "Set here") : "Inherited from Global";
    frag.push(h("header", { class: "in-head" }, kindTile(hs?.kind),
        h("div", { class: "in-headtxt" }, h("h2", { class: "in-title" }, plainControl(meta?.label || S.control), ctx.sliderMode && isPControl(S.control) && ctx.sliderMode() ? h("span", { class: "in-mode" }, " \u00B7 " + ctx.sliderMode()) : null), h("div", { class: "in-sub" }, kind[1]),
        h("div", { class: "in-chips" }, stateChip(state, stateText),
            e && (e.risk === "shell" || e.risk === "destructive") ? h("span", { class: "chip risk-chip " + e.risk, "data-tip": e.risk === "shell" ? "Runs a command" : "Can be destructive" }, icon("triangle-alert", 16, "c-ic"), h("span", {}, e.risk === "shell" ? "Runs a command" : "Can be destructive")) : null,
            writesChip(e), calChip(ctx),
            S.test ? h("span", { class: "pin-chip", "data-tip": S.pinned ? "Pinned to this control" : "Following the deck" }, icon(S.pinned ? "pin" : "activity", 16, "c-ic"), h("span", {}, S.pinned ? "Pinned" : "Following the deck")) : null)),
        h("button", { type: "button", class: "icon-btn in-close", "data-tip": "Close", "data-kbd": "Esc", "aria-label": "Close the inspector", onclick: () => ctx.deselect && ctx.deselect() }, icon("x", 20))));
    frag.push(h("div", { class: "paused-note", role: "status" }, icon("lock", 16), h("span", { class: "pn-off" }, "Offline: edits paused"), h("span", { class: "pn-ro" }, "Read-only: edits disabled")));
    const body = h("div", { class: "in-body" });
    { const rn = S.reqNote && S.reqNote(S.app); if (rn) body.append(rn); }
    { const hn = ctx.heldNote && ctx.heldNote(S.control); if (hn) body.append(hn); }
    if (ctx.modeAvail && isPControl(S.control) && ctx.modeAvail()) body.append(modeSection(ctx, e));
    body.append(ownership(ctx, e, g));
    const lg = legendNote(e, meta, kind); if (lg) body.append(lg);
    const pend = S.pending && S.pending.control === S.control && S.pending.bank === S.bank ? S.pending : null;
    const picker = actionPicker(ctx, pend ? { action: pend.action, source: "draft" } : e, S.control);
    body.append(h("section", { class: "in-sec" }, h("div", { class: "sec-h" }, "Action"), picker));
    if (pend) { body.append(draftSection(ctx, pend)); frag.push(body); return frag; }
    const a = e && actionDef(e.action);
    if (e && e.source === "global" && !g) {
        body.append(h("div", { class: "banner-row" }, icon("link", 16, "c-ic"), e.baseFallback ? h("span", {}, "The base assignment, ", h("b", {}, e.summary || (a && a.label) || "an action"), `, applies in ${smodeLabel(S.sMode)} mode. Choose an action here to give ${smodeLabel(S.sMode)} mode its own.`) : h("span", {}, "Global sets this to ", h("b", {}, e.summary || (a && a.label) || "an action"), ". Choose an action here to override.")));
    }
    if (a && a.id === "generic.disarm" && a.help) body.append(h("div", { class: "banner-row" }, icon("shield-alert", 16, "c-ic"), h("span", {}, a.help)));
    const shadow = S.warns && S.warns.find((w) => (/disarm|panic/i.test(w.kind || "") && w.kind !== "disarm-marker") && (w.app === S.app || !w.app) && (!w.control || w.control === S.control) && (!w.bank || w.bank === S.bank));
    if (shadow && shadow.message) body.append(h("div", { class: "banner-row warn-row" }, icon("triangle-alert", 16, "c-ic"), h("span", {}, String(shadow.message))));
    const hold = e && S.warns && S.warns.find((w) => w.suggest === "holdMs" && w.app === S.app && w.bank === S.bank && w.control === S.control);
    if (hold && a && a.fields.some((f) => f.key === "holdMs") && !(e.fields && e.fields.holdMs && Number(e.fields.holdMs.value) >= 1)) body.append(h("div", { class: "banner-row hold-hint" }, icon("timer", 16, "c-ic"), h("span", {}, "This can close, quit or delete things. Holding the control for a moment before it fires is recommended."), h("button", { type: "button", class: "btn sm", onclick: () => ctx.saveNow("holdMs", 600) }, "Require a 600 ms hold")));
    if (a) {
        const grp = groupFields(a);
        const ro = !!e && e.source === "global" && (!g || !!e.baseFallback);
        const prim = h("section", { class: "in-sec" + (ro ? " dim" : "") });
        if (!grp.primary.length) prim.append(h("div", { class: "muted pad" }, "This action has no settings."));
        for (const f of grp.primary) prim.append(fieldRow(f, e.fields[f.key] || { value: f.default, source: "default" }, ctx, "compact", ro));
        body.append(prim);
        if (ro) body.append(h("button", { type: "button", class: "btn block", onclick: () => ctx.overrideWhole() }, icon("link", 16), e.baseFallback ? `Override for ${smodeLabel(S.sMode)} mode` : "Override for this app"));
        const rowsEl = h("div", { class: "in-rows" });
        const showSens = grp.sens.length && ["knob", "dial", "slider"].includes(hs?.kind);
        if (showSens) rowsEl.append(drawerRow("spline", "Response curve", null, () => ctx.openDrawer("sens"), miniCurve(e)));
        if (grp.adv.length) rowsEl.append(drawerRow("settings-2", "Advanced", String(grp.adv.length), () => ctx.openDrawer("adv")));
        if (rowsEl.children.length) body.append(rowsEl);
    }
    frag.push(body);
    return frag;
}
function drawerRow(ic, label, count, onClick, extra) {
    return h("button", { type: "button", class: "drow", onclick: onClick }, icon(ic, 16, "dr-ic"), h("span", { class: "dr-l" }, label), extra || null, count ? h("span", { class: "count" }, count) : null, icon("chevron-right", 16, "dr-chev"));
}
function miniCurve(e) {
    const v = Object.fromEntries(Object.entries(e.fields || {}).map(([k, x]) => [k, x.value]));
    const f = curveFn(v); const ys = []; for (let t = 0; t <= 80; t += 4) ys.push(f(t));
    const m = Math.max(1e-6, ...ys);
    const pts = ys.map((y, i) => `${(2 + (i / 20) * 92).toFixed(1)},${(22 - (y / m) * 20).toFixed(1)}`).join(" ");
    const sv = s("svg", { class: "mini-curve", viewBox: "0 0 96 24", width: 96, height: 24, "aria-hidden": "true" }, s("polyline", { points: pts }));
    return sv;
}

/* ------------------------------------------------------------ sensitivity readouts */
const unitWord = (u) => String(u || "").split(/ per |\//)[0].trim();
const MAG = ["linesPerDetent", "secondsPerStep", "base", "sens"];
/** "per click = X <unit>": what one raw encoder click does with the current values. */
export function perClick(a, v) {
    if (!a) return null;
    const thr = Math.max(1, Number(v.detentThreshold) || 1);
    const mk = MAG.find((k) => a.fields.some((f) => f.key === k) && v[k] != null);
    if (mk) { const f = a.fields.find((x) => x.key === mk); const per = (Number(v[mk]) || 0) / (["linesPerDetent", "secondsPerStep"].includes(mk) ? thr : 1); return { val: per, unit: unitWord(f.unit) || "units", note: per < 0 ? "reversed" : "" }; }
    if (a.type === "keyDelta" && a.fields.some((f) => f.key === "detentThreshold")) return { val: 1 / thr, unit: "key presses", note: "" };
    return null;
}
/** Coarse / Normal / Fine: one field, multiples of the shipped default, so a preset is ONE history step. */
export function sensPresets(a, v) {
    if (!a) return null;
    const mk = MAG.find((k) => a.fields.some((f) => f.key === k && f.default != null));
    const q = (f, x) => { let r = Number(x.toFixed(2)); if (f.min != null) r = Math.max(f.min, r); if (f.max != null) r = Math.min(f.max, r); return r; };
    if (mk) { const f = a.fields.find((x) => x.key === mk), d = Number(f.default) || 1; return { key: mk, coarse: q(f, d * 2), normal: d, fine: q(f, d * 0.5) }; }
    const tf = a.fields.find((f) => f.key === "detentThreshold" && f.default != null);
    if (tf) { const d = Number(tf.default); return { key: "detentThreshold", coarse: Math.max(tf.min ?? 1, Math.round(d / 2)), normal: d, fine: Math.min(tf.max ?? 64, d * 2) }; }
    return null;
}

/* ------------------------------------------------------------ the real curve (POST /api/curve) behind a thin adapter */
const CURVE_TYPES = new Set(["scrollDelta", "keyDelta", "jog", "jogSeconds"]);
const SENS_SEND = new Set(["linesPerDetent", "secondsPerStep", "base", "sens", "gamma", "detentThreshold", "maxDelta", "velocityProfile", "momentumMs", "keyDelta"]);
/** -> { fn(t)->y, unit, staged, approx } or null (unsupported action, endpoint missing, or failed: callers use the local model) */
export async function realCurve(actionId, values) {
    if (S.ep.curve === false || (S.curveNo && S.curveNo[actionId])) return null;
    const a = actionDef(actionId); if (!a || !CURVE_TYPES.has(a.type)) return null; // the real curve exists for these types only; others use the local model
    const fields = {}; for (const f of a.fields) if (values[f.key] != null && (SENS_SEND.has(f.key) || isSens(f))) fields[f.key] = values[f.key];
    try {
        const r = await api.curve(actionId, fields, 80); S.ep.curve = true;
        const pts = r.points || []; if (pts.length < 2) return null;
        const fn = (t) => { if (t <= pts[0].ticksPerSec) return pts[0].outputPerSec; for (let i = 1; i < pts.length; i++) if (t <= pts[i].ticksPerSec) { const p = pts[i - 1], q = pts[i], k = (t - p.ticksPerSec) / Math.max(1e-9, q.ticksPerSec - p.ticksPerSec); return p.outputPerSec + (q.outputPerSec - p.outputPerSec) * k; } const p = pts[pts.length - 1]; return p.outputPerSec; };
        return { fn, unit: r.unit || "", staged: !!r.staged, approx: !!r.approx };
    } catch (e) { if (e.status === 404) S.ep.curve = false; else if (e.status === 400) (S.curveNo = S.curveNo || {})[actionId] = true; return null; }
}
const isSens = (f) => f.sensitivity === true || f.group === "sensitivity";

/* ------------------------------------------------------------ sensitivity curve card (Sensitivity drawer) */
export function curveCard(ctx) {
    const el = h("div", { class: "curve-card" });
    const svg = s("svg", { class: "curve", role: "img", "aria-label": "Sensitivity curve: raw clicks per second against output per second" });
    const meterFill = h("div", { class: "meter-fill" });
    const ticks = [25, 50, 75].map((p) => h("i", { class: "meter-tick", style: { left: p + "%" } }));
    const meterVal = h("span", { class: "meter-val" }, "0");
    const cap = h("div", { class: "meter-cap" }, "Turn this control on the deck to see its response");
    const meter = h("div", { class: "meter" }, h("div", { class: "meter-bar" }, meterFill, ...ticks), meterVal);
    const fine = h("span", { class: "chip", hidden: true }, icon("zoom-in", 16, "c-ic"), "Fine mode");
    const note = h("div", { class: "curve-note", hidden: true }, icon("info", 16, "c-ic"), h("span", {}));
    const key = h("div", { class: "ghost-key", hidden: true }, h("i"), "Global");
    el.append(svg, meter, cap, fine, note, key);
    const M = { W: 296, H: 160, L: 44, B: 22, T: 26, R: 10, MAXT: 80, ys: [], ysT: [], gh: [], mx: new Spring("smooth", 0), my: new Spring("smooth", 0), lastEv: 0, tps: 0, unit: "", fn: null, ymax: 1, bar: new Spring("soft", 0), seq: 0, timer: 0 };
    const valuesOf = (src) => Object.fromEntries(Object.entries(src || {}).map(([k, x]) => [k, S.draft[k] ?? x.value]));
    const curE = () => eff();
    const X = (t) => M.L + (t / M.MAXT) * (M.W - M.L - M.R), Y = (y) => M.T + (1 - y / M.ymax) * (M.H - M.T - M.B);
    function unitName(a) { const f = a && MAG.map((k) => a.fields.find((x) => x.key === k)).find(Boolean); return unitWord(f && f.unit) || ""; }
    function sample(fn) { const out = []; for (let t = 0; t <= M.MAXT; t += 2) out.push(fn(t)); return out; }
    function globalVals() { const e = curE(), g = ctx.ghostValues && ctx.ghostValues(); return g && e && e.source === "app" ? g : null; }
    function target() {
        const e = curE(); if (!e) return;
        const a = actionDef(e.action), v = valuesOf(e.fields);
        M.fn = M.fn || curveFn(v);
        const local = curveFn(v); const first = !M.ys.length;
        M.ysT = sample(local); M.ymax = Math.max(1e-6, M.ysT[M.ysT.length - 1]) * 1.1; M.unit = unitName(a); if (first || reduced()) M.ys = M.ysT.slice();
        clearTimeout(M.timer);
        const my = ++M.seq;
        M.timer = setTimeout(async () => {
            const r = await realCurve(e.action, v); if (my !== M.seq) return;
            let gv = globalVals(), gr = null;
            if (gv) gr = await realCurve(e.action, gv);
            if (my !== M.seq) return;
            if (r) { M.fn = r.fn; M.ysT = sample(r.fn); M.unit = r.unit || M.unit; note.hidden = !(r.staged); note.lastChild.textContent = "Staged: the Resolve engine doesn’t apply this response yet, so this shows what the control does today."; }
            else { M.fn = local; note.hidden = true; }
            M.gh = gv ? sample(gr ? gr.fn : curveFn(gv)) : [];
            key.hidden = !M.gh.length;
            M.ymax = Math.max(1e-6, M.ysT[M.ysT.length - 1], ...(M.gh.length ? [M.gh[M.gh.length - 1]] : [0])) * 1.1;
            wake();
        }, 120);
        const fm = fineFactor(); fine.lastChild.textContent = `Fine mode × ${fm}`;
    }
    function fineFactor() { const f = S.pr?.raw?.settings?.fineFactor; return f != null ? f : 0.5; }
    /* the chart is built once from DOM nodes (no HTML strings); draw() only moves attributes */
    const N = (tag, cls, at) => s(tag, { class: cls, ...at });
    const gridV = [1, 2, 3].map(() => N("line", "gl")), gridH = [1, 2, 3].map(() => N("line", "gl"));
    const axis = N("line", "ax"), xLabs = [0, 20, 40, 60, 80].map((t) => N("text", "lb", { "text-anchor": t === 0 ? "start" : t === 80 ? "end" : "middle" })), yLabs = [0, 1, 2, 3].map(() => N("text", "lb", { "text-anchor": "end" }));
    const uY = N("text", "lb ut"), uX = N("text", "lb ut", { "text-anchor": "end" });
    const ghLine = N("polyline", "gh"), area = N("path", "area", { fill: "url(#cg)" }), ln = N("polyline", "ln");
    const xh1 = N("line", "xh"), xh2 = N("line", "xh"), mk = N("circle", "mk", { r: 4 });
    const grad = s("linearGradient", { id: "cg", x1: 0, y1: 0, x2: 0, y2: 1 }, s("stop", { offset: 0, class: "cg0" }), s("stop", { offset: 1, class: "cg1" }));
    svg.append(s("defs", {}, grad), ...gridV, ...gridH, axis, ...xLabs, ...yLabs, uY, uX, ghLine, area, ln, xh1, xh2, mk);
    const setA = (n, o) => { for (const k in o) n.setAttribute(k, typeof o[k] === "number" ? +o[k].toFixed(2) : o[k]); };
    const polyPts = (arr) => arr.map((y, i) => `${X(i * 2).toFixed(1)},${Y(y).toFixed(1)}`).join(" ");
    function draw() {
        const ys = M.ys.length ? M.ys : M.ysT;
        gridV.forEach((n, i) => { const x = X((i + 1) * 20); setA(n, { x1: x, y1: M.T, x2: x, y2: M.H - M.B }); });
        gridH.forEach((n, i) => { const y = M.T + ((M.H - M.T - M.B) * (i + 1)) / 4; setA(n, { x1: M.L, y1: y, x2: M.W - M.R, y2: y }); });
        setA(axis, { x1: M.L, y1: M.H - M.B, x2: M.W - M.R, y2: M.H - M.B });
        xLabs.forEach((n, i) => { setA(n, { x: X(i * 20), y: M.H - 6 }); n.textContent = String(i * 20); });
        yLabs.forEach((n, i) => { const yv = (M.ymax / 1.1) * (i / 4) * 1.1; setA(n, { x: M.L - 6, y: Y(yv) + 4 }); n.textContent = compact(yv); });
        /* above the plot: the dotted cap / Global line runs along its top edge and must not cross the words */ setA(uY, { x: 4, y: 11 }); uY.textContent = M.unit ? M.unit + " per second" : "output per second";
        setA(uX, { x: M.W - M.R, y: M.H - 24 }); uX.textContent = "clicks per second";
        ghLine.style.display = M.gh.length ? "" : "none"; if (M.gh.length) ghLine.setAttribute("points", polyPts(M.gh));
        const pts = polyPts(ys);
        ln.setAttribute("points", pts);
        area.setAttribute("d", `M${X(0)},${Y(0)} L${pts.replace(/ /g, " L")} L${X(80)},${Y(0)} Z`);
        const on = !!M.markOn;
        for (const n of [xh1, xh2, mk]) n.style.display = on ? "" : "none";
        if (on) { const px = X(clamp(M.mx.x, 0, 80)), py = Y(clamp(M.my.x, 0, M.ymax)); setA(xh1, { x1: px, y1: py, x2: px, y2: M.H - M.B }); setA(xh2, { x1: M.L, y1: py, x2: px, y2: py }); setA(mk, { cx: px, cy: py }); }
    }
    function frame(dt) {
        const now = performance.now();
        let busy = false;
        if (M.ys.length === M.ysT.length) { let d = 0; for (let i = 0; i < M.ys.length; i++) { const nv = M.ys[i] + (M.ysT[i] - M.ys[i]) * (1 - Math.exp(-dt / 0.06)); d += Math.abs(nv - M.ysT[i]); M.ys[i] = Math.abs(nv - M.ysT[i]) < 1e-4 * M.ymax ? M.ysT[i] : nv; } if (d > 1e-4 * M.ymax) busy = true; }
        if (now - M.lastEv > 600) { M.mx.t = 0; M.my.t = 0; M.bar.t = 0; }
        M.mx.step(dt); M.my.step(dt); M.bar.step(dt);
        meterFill.style.transform = `scaleX(${clamp(M.bar.x, 0, 1)})`;
        if (M.markOn && now - M.lastEv > 600 && M.mx.rest) M.markOn = false;
        draw();
        return busy || !M.mx.rest || !M.my.rest || !M.bar.rest || now - M.lastEv < 700;
    }
    const wake = () => loop.add(frame);
    const ro = new ResizeObserver(() => { const w = el.clientWidth - 32; if (w > 120) { M.W = w; draw(); } });
    ro.observe(el);
    target(); draw();
    const api_ = {
        el,
        update() { if (!curE()) return; target(); wake(); if (api_.onUpdate) api_.onUpdate(); },
        live(ev) {
            if (!M.fn || ev.kind !== "turn") return;
            const tps = Math.min(M.MAXT, Math.abs(ev.ticksPerSec || 0));
            M.lastEv = performance.now(); M.markOn = true; M.mx.t = tps; M.my.t = M.fn(tps);
            const out = M.fn(tps); M.bar.t = clamp(out / (M.ymax / 1.1), 0, 1);
            meterVal.textContent = `${compact(out)} ${M.unit ? M.unit + "/s" : "/s"}`; cap.hidden = true; fine.hidden = !ev.fine;
            wake();
        },
        destroy() { ro.disconnect(); clearTimeout(M.timer); M.seq++; },
    };
    return api_;
}
