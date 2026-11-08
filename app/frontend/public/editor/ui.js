// ui.js: the custom control library. Nothing here is a browser-default widget.
// Popover, list menu, select, stepper, slider, segmented, switch, key capture, text field, list editor,
// two-step confirm, mini tooltip, toasts, app icon tile, state chips.
import { h, s, $, icon, clamp, fmt, decimals, loop, reduced, anim, EASE_OUT, EASE_IN, willChange, store, debounce, Spring } from "./util.js";

const portalEl = () => document.getElementById("portal");
let uid = 0;
export const nextId = (p = "u") => p + ++uid;

/* ============================================================ popover */
const pops = [];
export function popover({ anchor, el, placement = "bottom-start", gap = 8, matchWidth = false, minWidth = 0, maxHeight = 360, onClose, onEsc, restoreFocus = true, viewportRight = false, caret = false }) {
    const root = portalEl();
    el.classList.add("pop");
    el.style.visibility = "hidden";
    root.append(el);
    const a = anchor.getBoundingClientRect();
    const vw = innerWidth, vh = innerHeight;
    if (matchWidth) el.style.width = Math.max(a.width, minWidth) + "px";
    else if (minWidth) el.style.minWidth = minWidth + "px";
    const below = vh - a.bottom - gap - 8, above = a.top - gap - 8;
    let dir = 1; // 1 = opens downward
    const want = Math.min(maxHeight, el.scrollHeight || maxHeight);
    if (placement.startsWith("top") || (below < Math.min(want, 240) && above > below)) dir = -1;
    const room = dir === 1 ? below : above;
    el.style.maxHeight = Math.max(160, Math.min(maxHeight, room)) + "px";
    const w = el.offsetWidth, hgt = el.offsetHeight;
    let left = placement.endsWith("end") ? a.right - w : a.left;
    if (viewportRight) left = vw - w - 8; // sit over the inspector, never over the photo
    left = clamp(left, 8, Math.max(8, vw - w - 8));
    const top = dir === 1 ? a.bottom + gap : a.top - gap - hgt;
    el.style.left = left + "px";
    if (dir === 1) el.style.top = clamp(top, 8, Math.max(8, vh - hgt - 8)) + "px";
    else { el.style.top = "auto"; el.style.bottom = Math.max(8, vh - (a.top - gap)) + "px"; } // opening upward: pinned by its bottom edge, so content that arrives later grows upward (within maxHeight) instead of running off the window
    if (caret && dir === 1) { el.classList.add("has-caret"); el.style.setProperty("--caret-x", clamp(a.left + a.width / 2 - left, 20, Math.max(20, w - 20)) + "px"); }
    el.style.transformOrigin = (dir === 1 ? "top " : "bottom ") + (placement.endsWith("end") ? "right" : "left");
    el.style.visibility = "";
    willChange(el, "transform, opacity", 400);
    anim(el, [{ opacity: 0, transform: `translateY(${-4 * dir}px) scale(.98)` }, { opacity: 1, transform: "none" }], { duration: 160, easing: EASE_OUT });
    anchor.setAttribute("aria-expanded", "true");
    hideTip();
    let closed = false;
    const rec = { el, anchor };
    const close = (opts = {}) => {
        if (closed) return;
        closed = true;
        const i = pops.indexOf(rec); if (i >= 0) pops.splice(i, 1);
        document.removeEventListener("pointerdown", down, true);
        document.removeEventListener("keydown", key, true);
        removeEventListener("resize", onResize);
        anchor.setAttribute("aria-expanded", "false");
        const done = () => { el.remove(); };
        const an = opts.instant ? null : anim(el, [{ opacity: 1, transform: "none" }, { opacity: 0, transform: `translateY(${-4 * dir}px) scale(.98)` }], { duration: 110, easing: EASE_IN, fill: "forwards" });
        if (an) { an.onfinish = done; } else done();
        if (onClose) onClose();
        if (restoreFocus && !opts.noFocus && document.activeElement && (el.contains(document.activeElement) || document.activeElement === document.body)) anchor.focus({ preventScroll: true });
    };
    const down = (e) => { if (el.contains(e.target) || anchor.contains(e.target)) return; close({ noFocus: true }); };
    const key = (e) => {
        if (e.key !== "Escape" || pops[pops.length - 1] !== rec) return;
        if (onEsc && onEsc(e)) { e.stopPropagation(); e.preventDefault(); return; }
        e.stopPropagation(); e.preventDefault(); close();
    };
    const onResize = () => close({ noFocus: true });
    document.addEventListener("pointerdown", down, true);
    document.addEventListener("keydown", key, true);
    addEventListener("resize", onResize);
    pops.push(rec);
    rec.close = close;
    return { el, close, get open() { return !closed; } };
}
export const closeAllPopovers = () => { for (const p of pops.slice()) p.close({ noFocus: true, instant: true }); };

/* ============================================================ list menu (select, action picker, bank menu, unit menu) */
function highlight(text, q) {
    if (!q) return text;
    const i = text.toLowerCase().indexOf(q.toLowerCase());
    if (i < 0) return text;
    return [text.slice(0, i), h("mark", {}, text.slice(i, i + q.length)), text.slice(i + q.length)];
}
/** groups: [{label, icon, items:[{value,label,sub,icon,count,disabled}]}]. footer: [{label, icon, tone, onClick}] */
export function openMenu({ anchor, groups, value, onPick, search = false, minWidth = 200, matchWidth = false, footer = [], ariaLabel = "Menu", onClose, maxHeight = 360, placement = "bottom-start", emptyText = "No results", searchPlaceholder = "Search", menu = false }) {
    const id = nextId("m");
    let q = "", active = -1, flat = [];
    // menu = an action menu (role menu / menuitem, roving focus: the focused row IS the active one); otherwise a listbox with aria-activedescendant
    const list = h("div", { class: "m-list", role: menu ? "menu" : "listbox", "aria-label": ariaLabel, id: id + "-l", tabindex: search || menu ? "-1" : "0" });
    const input = search ? h("input", { type: "text", class: "m-search-in", placeholder: searchPlaceholder, "aria-label": searchPlaceholder, role: "combobox", "aria-controls": id + "-l", "aria-expanded": "true", autocomplete: "off", spellcheck: "false" }) : null;
    const clearBtn = search ? h("button", { type: "button", class: "icon-btn xs", "aria-label": "Clear search", tabindex: "-1", hidden: true, onclick: () => { input.value = ""; q = ""; build(); input.focus(); } }, icon("x")) : null;
    const searchRow = search ? h("div", { class: "m-search" }, icon("search"), input, clearBtn) : null;
    const foot = footer.length ? h("div", { class: "m-foot" }, footer.map((f) => {
        const nm = h("span", { class: "m-name" }, f.label);
        let armed = false, tm = 0;
        const b = h("button", { type: "button", class: "m-row foot" + (f.tone ? " " + f.tone : ""), onclick: () => {
            if (f.confirm && !armed) { armed = true; b.classList.add("armed"); nm.textContent = f.confirm; tm = setTimeout(() => { armed = false; b.classList.remove("armed"); nm.textContent = f.label; }, 4000); return; }
            clearTimeout(tm); pop.close({ noFocus: true }); f.onClick();
        } }, icon(f.icon || "chevron-right"), nm);
        return b;
    })) : null;
    const el = h("div", { class: "menu" }, searchRow, list, foot);
    let pop = null;

    function build() {
        list.replaceChildren(); flat = [];
        const ql = q.trim();
        if (clearBtn) clearBtn.hidden = !q;
        for (const g of groups) {
            const items = g.items.filter((it) => !ql || (it.label + " " + (it.sub || "")).toLowerCase().includes(ql.toLowerCase()));
            if (!items.length) continue;
            if (g.label) list.append(h("div", { class: "m-grp", role: "presentation" }, g.icon ? icon(g.icon) : null, h("span", {}, g.label)));
            for (const it of items) {
                const i = flat.length;
                const row = h("div", { class: "m-row" + (it.value === value ? " cur" : "") + (it.disabled ? " dis" : "") + (it.sub ? " two" : ""), role: menu ? "menuitem" : "option", tabindex: menu ? "-1" : null, id: id + "-o" + i, "aria-selected": menu ? null : it.value === value ? "true" : "false", "aria-disabled": it.disabled ? "true" : null },
                    it.icon ? icon(it.icon, 16, "m-ic") : null,
                    h("span", { class: "m-txt" }, h("span", { class: "m-name" }, highlight(it.label, ql)), it.sub ? h("span", { class: "m-sub" }, it.sub) : null),
                    it.caps ? keycaps(it.caps, "m-caps") : null,
                    it.count != null ? h("span", { class: "m-count" }, String(it.count)) : null,
                    it.value === value ? icon("check", 16, "m-check") : null);
                row.addEventListener("pointermove", () => setActive(i, false));
                row.addEventListener("click", () => pick(i));
                flat.push({ it, row }); list.append(row);
            }
        }
        if (!flat.length) list.append(h("div", { class: "m-empty" }, icon("search-x", 24), h("div", {}, emptyText + (ql ? " " : ""), ql ? h("b", {}, `“${ql}”`) : ""), ql ? h("button", { type: "button", class: "btn ghost sm", onclick: () => { input.value = ""; q = ""; build(); input.focus(); } }, "Clear search") : null));
        const cur = flat.findIndex((f) => f.it.value === value);
        setActive(ql ? (flat.length ? 0 : -1) : cur >= 0 ? cur : flat.length ? 0 : -1, true);
    }
    function setActive(i, scroll) {
        if (active >= 0 && flat[active]) { flat[active].row.classList.remove("act"); if (menu) flat[active].row.tabIndex = -1; }
        active = i;
        if (i >= 0 && flat[i]) {
            flat[i].row.classList.add("act");
            if (menu) { flat[i].row.tabIndex = 0; if (pop && pop.open !== false && flat[i].row.isConnected && document.contains(flat[i].row)) flat[i].row.focus({ preventScroll: true }); }
            else { const tgt = input || list; tgt.setAttribute("aria-activedescendant", flat[i].row.id); }
            if (scroll) flat[i].row.scrollIntoView({ block: "nearest" });
        }
    }
    function pick(i) {
        const f = flat[i]; if (!f || f.it.disabled) return;
        pop.close();
        onPick(f.it.value, f.it);
    }
    let typed = "", typedT = 0;
    function onKey(e) {
        const n = flat.length;
        if (e.key === "ArrowDown") { e.preventDefault(); if (n) { setActive((active + 1) % n, true); } }
        else if (e.key === "ArrowUp") { e.preventDefault(); if (n) { setActive((active - 1 + n) % n, true); } }
        else if (e.key === "Home") { if (!search || e.ctrlKey) { e.preventDefault(); setActive(0, true); } }
        else if (e.key === "End") { if (!search || e.ctrlKey) { e.preventDefault(); setActive(n - 1, true); } }
        else if (e.key === "Enter") { e.preventDefault(); if (active >= 0) pick(active); }
        else if (e.key === "Tab") { pop.close({ noFocus: true }); }
        else if (!search && e.key.length === 1 && !e.metaKey && !e.ctrlKey) {
            clearTimeout(typedT); typed += e.key.toLowerCase(); typedT = setTimeout(() => { typed = ""; }, 600);
            const j = flat.findIndex((f) => f.it.label.toLowerCase().startsWith(typed));
            if (j >= 0) setActive(j, true);
        }
    }
    (input || list).addEventListener("keydown", onKey);
    if (input) input.addEventListener("input", () => { q = input.value; build(); });
    build(); // fill the list BEFORE the popover measures it, or a menu opened near the bottom is placed for an empty box and runs off screen
    pop = popover({ anchor, el, placement, minWidth, matchWidth, maxHeight, onClose, onEsc: () => { if (search && q) { input.value = ""; q = ""; build(); return true; } return false; } });
    if (active >= 0 && flat[active]) flat[active].row.scrollIntoView({ block: "nearest" });
    if (menu && flat[active]) flat[active].row.focus({ preventScroll: true }); else (input || list).focus({ preventScroll: true });
    return pop;
}

/* ============================================================ select (custom listbox) */
export function selectField({ options, value, onChange, placeholder = "Choose…", ariaLabel, id, search }) {
    const label = h("span", { class: "sel-label" });
    const btn = h("button", { type: "button", class: "sel", id, role: "combobox", "aria-haspopup": "listbox", "aria-expanded": "false", "aria-label": ariaLabel || null }, label, icon("chevron-down", 16, "sel-chev"));
    let cur = value, pop = null;
    const find = (v) => options.find((o) => o.value === v);
    const paint = () => { const o = find(cur); label.textContent = o ? o.label : placeholder; btn.classList.toggle("ph", !o); };
    const openIt = () => {
        if (pop && pop.open) { pop.close(); return; }
        pop = openMenu({ anchor: btn, matchWidth: true, minWidth: 160, groups: [{ items: options }], value: cur, search: search ?? options.length > 8, ariaLabel: ariaLabel || "Options",
            onPick: (v) => { cur = v; paint(); onChange(v); } });
    };
    btn.addEventListener("click", openIt);
    btn.addEventListener("keydown", (e) => { if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); if (!(pop && pop.open)) openIt(); } });
    paint();
    return { el: btn, set(v) { cur = v; paint(); }, get value() { return cur; } };
}

/* ============================================================ number stepper */
export function stepper({ value, min, max, step = 1, onChange, onInvalid, label = "Value", format }) {
    let v = value, dec = decimals(step);
    const fm = format || ((x) => fmt(x, Math.max(dec, 3)));
    const input = h("input", { type: "text", class: "st-in", inputmode: "decimal", role: "spinbutton", autocomplete: "off", spellcheck: "false", "aria-label": label });
    const minus = h("button", { type: "button", class: "st-btn", tabindex: "-1", "aria-label": "Decrease", "data-dir": "-1" }, icon("minus"));
    const plus = h("button", { type: "button", class: "st-btn", tabindex: "-1", "aria-label": "Increase", "data-dir": "1" }, icon("plus"));
    const el = h("div", { class: "stepper", role: "group", "aria-label": label }, minus, input, plus);
    const lo = min ?? -Infinity, hi = max ?? Infinity;
    const paint = () => {
        input.value = v == null ? "" : fm(v);
        input.setAttribute("aria-valuenow", v ?? "");
        if (Number.isFinite(lo)) input.setAttribute("aria-valuemin", lo);
        if (Number.isFinite(hi)) input.setAttribute("aria-valuemax", hi);
        minus.disabled = v != null && v <= lo; plus.disabled = v != null && v >= hi;
        el.classList.remove("err");
    };
    const round = (x, d) => Number(x.toFixed(Math.min(6, d)));
    function setV(x, emit) {
        const nv = clamp(x, lo, hi);
        const changed = nv !== v; v = nv; paint(); onInvalid && onInvalid(null);
        if (changed && emit) onChange(v);
    }
    function bump(dir, mult) {
        const base = v == null ? (Number.isFinite(lo) ? lo : 0) : v;
        const st = step * mult;
        setV(round(base + dir * st, decimals(st) + (st < step ? 1 : 0) + dec), true);
    }
    const mult = (e) => (e.shiftKey ? 10 : e.altKey ? 0.1 : 1);
    /* hold-to-repeat: 400ms, 80ms, then 30ms after 12 repeats */
    let rep = 0, n = 0;
    function startRepeat(btnEl, e) {
        const dir = +btnEl.dataset.dir, m = mult(e);
        bump(dir, m); n = 0;
        const tick = () => { bump(dir, m); n++; rep = setTimeout(tick, n > 12 ? 30 : 80); };
        rep = setTimeout(tick, 400);
        const stop = () => { clearTimeout(rep); removeEventListener("pointerup", stop); removeEventListener("pointercancel", stop); };
        addEventListener("pointerup", stop); addEventListener("pointercancel", stop);
    }
    for (const b of [minus, plus]) {
        b.addEventListener("pointerdown", (e) => { if (e.button !== 0 || b.disabled) return; e.preventDefault(); startRepeat(b, e); });
        b.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); bump(+b.dataset.dir, mult(e)); } });
    }
    /* scrub on the value: horizontal drag, 1 step / 4px */
    let sx = 0, sv = 0, moved = false, down = false;
    input.addEventListener("pointerdown", (e) => {
        if (document.activeElement === input) return; // already editing: normal caret behaviour
        if (e.button !== 0) return;
        e.preventDefault(); down = true; moved = false; sx = e.clientX; sv = v ?? 0; input.setPointerCapture(e.pointerId);
    });
    input.addEventListener("pointermove", (e) => {
        if (!down) return;
        const dx = e.clientX - sx;
        if (!moved && Math.abs(dx) < 3) return;
        moved = true; el.classList.add("scrub");
        const st = step * mult(e);
        setV(round(sv + Math.round(dx / 4) * st, decimals(st) + dec), true);
    });
    const end = () => { if (!down) return; down = false; el.classList.remove("scrub"); if (!moved) { input.focus(); input.select(); } };
    input.addEventListener("pointerup", end); input.addEventListener("pointercancel", end);
    input.addEventListener("focus", () => input.select());
    function commitText() {
        const t = input.value.trim().replace(",", ".");
        if (t === "" || Number.isNaN(Number(t))) { el.classList.add("err"); onInvalid && onInvalid("Enter a number."); return false; }
        const x = Number(t);
        if (x < lo || x > hi) { el.classList.add("err"); onInvalid && onInvalid(`Use ${Number.isFinite(lo) ? fm(lo) : "any"} to ${Number.isFinite(hi) ? fm(hi) : "any"}.`); return false; }
        setV(x, true); return true;
    }
    input.addEventListener("keydown", (e) => {
        if (e.key === "Enter") { e.preventDefault(); if (commitText()) input.select(); }
        else if (e.key === "Escape") { if (document.activeElement === input) { e.preventDefault(); e.stopPropagation(); paint(); onInvalid && onInvalid(null); input.blur(); } }
        else if (e.key === "ArrowUp") { e.preventDefault(); bump(1, mult(e)); }
        else if (e.key === "ArrowDown") { e.preventDefault(); bump(-1, mult(e)); }
        else if (e.key === "PageUp") { e.preventDefault(); bump(1, 10); }
        else if (e.key === "PageDown") { e.preventDefault(); bump(-1, 10); }
    });
    input.addEventListener("blur", () => { if (input.value.trim() !== fm(v) && input.value.trim() !== "") { commitText(); } else if (!el.classList.contains("err")) paint(); });
    input.addEventListener("input", () => { el.classList.remove("err"); onInvalid && onInvalid(null); });
    paint();
    return { el, input, set(x) { if (document.activeElement === input) { v = x; return; } v = x; paint(); }, setRange(a, b, st) { /* unit conversion */ step = st ?? step; dec = decimals(step); }, setError(on) { el.classList.toggle("err", on); } };
}

/* ============================================================ slider */
export function slider({ value, min, max, step = 1, def, ghost, onInput, label = "Value", format }) {
    const log = min > 0 && max / min > 200;
    const dec = decimals(step);
    const fm = format || ((x) => fmt(x, Math.max(dec, 2)));
    const thumb = h("div", { class: "sl-thumb" });
    const fill = h("div", { class: "sl-fill" });
    const chip = h("div", { class: "sl-chip", "aria-hidden": "true" });
    const rail = h("div", { class: "sl-rail" }, fill);
    const el = h("div", { class: "slider", role: "slider", tabindex: "0", "aria-label": label, "aria-valuemin": min, "aria-valuemax": max }, rail, thumb, chip);
    const toP = (x) => clamp(log ? Math.log(x / min) / Math.log(max / min) : (x - min) / (max - min), 0, 1);
    const fromP = (p) => (log ? min * Math.pow(max / min, p) : min + p * (max - min));
    let v = value, W = 0, dragging = false;
    const PAD = 8;
    const snap = (x) => {
        if (log) { const sig = Number(x.toPrecision(3)); return clamp(Math.max(step, Math.round(sig / step) * step) || sig, min, max); }
        return clamp(Number((Math.round((x - min) / step) * step + min).toFixed(Math.max(dec, 4))), min, max);
    };
    const notch = def != null && def >= min && def <= max ? h("div", { class: "sl-notch" }) : null;
    if (notch) rail.append(notch);
    let gv = ghost;
    const gh = ghost != null && ghost >= min && ghost <= max ? h("div", { class: "sl-ghost", title: "Global: " + fmt(ghost, 3), "aria-hidden": "true" }) : null;
    if (gh) el.append(gh);
    function paint() {
        const p = toP(v == null ? min : v);
        fill.style.transform = `scaleX(${p})`;
        const x = p * W;
        thumb.style.translate = `${x}px 0`;
        chip.style.translate = `${x}px 0`;
        chip.textContent = fm(v ?? min);
        el.setAttribute("aria-valuenow", v ?? min);
        if (notch) notch.style.translate = `${toP(def) * W}px 0`;
        if (gh) gh.style.translate = `${PAD + toP(gv) * W}px 0`;
    }
    const ro = new ResizeObserver(() => { W = Math.max(0, el.clientWidth - PAD * 2); paint(); });
    ro.observe(el);
    function fromEvent(e) {
        const r = el.getBoundingClientRect();
        const p = clamp((e.clientX - r.left - PAD) / Math.max(1, r.width - PAD * 2), 0, 1);
        return snap(fromP(p));
    }
    function emit(x) { if (x !== v) { v = x; paint(); onInput(v); } }
    el.addEventListener("pointerdown", (e) => {
        if (e.button !== 0) return;
        dragging = true; el.classList.add("drag"); el.setPointerCapture(e.pointerId);
        emit(fromEvent(e));
    });
    el.addEventListener("pointermove", (e) => { if (dragging) emit(fromEvent(e)); });
    const end = () => { dragging = false; el.classList.remove("drag"); };
    el.addEventListener("pointerup", end); el.addEventListener("pointercancel", end);
    el.addEventListener("keydown", (e) => {
        const big = e.shiftKey ? 10 : 1;
        let nv = null, cur = v ?? min;
        const st = log ? Math.max(step, cur * 0.05) : step;
        if (e.key === "ArrowRight" || e.key === "ArrowUp") nv = cur + st * big;
        else if (e.key === "ArrowLeft" || e.key === "ArrowDown") nv = cur - st * big;
        else if (e.key === "PageUp") nv = cur + st * 10;
        else if (e.key === "PageDown") nv = cur - st * 10;
        else if (e.key === "Home") nv = min; else if (e.key === "End") nv = max;
        if (nv != null) { e.preventDefault(); emit(snap(clamp(nv, min, max))); }
    });
    paint();
    return { el, set(x) { v = x; paint(); } };
}

/* ============================================================ segmented */
export function segmented({ options, value, onChange, label }) {
    const n = options.length;
    const thumb = h("div", { class: "seg-thumb", style: { width: `calc((100% - 4px) / ${n})` } });
    const el = h("div", { class: "seg", role: "radiogroup", "aria-label": label || null, style: { gridTemplateColumns: `repeat(${n}, 1fr)` } }, thumb);
    let cur = value;
    const btns = options.map((o, i) => {
        const b = h("button", { type: "button", class: "seg-opt", role: "radio", "aria-checked": "false", tabindex: "-1", onclick: () => choose(i, true) }, o.label);
        b.addEventListener("keydown", (e) => {
            const d = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
            if (d) { e.preventDefault(); const j = (idx() + d + n) % n; choose(j, true); btns[j].focus(); }
        });
        el.append(b); return b;
    });
    const idx = () => Math.max(0, options.findIndex((o) => o.value === cur));
    function paint(first) {
        const i = idx(), has = options.some((o) => o.value === cur);
        thumb.style.translate = `${i * 100}% 0`;
        thumb.style.opacity = has ? "1" : "0";
        btns.forEach((b, k) => { b.setAttribute("aria-checked", has && k === i ? "true" : "false"); b.tabIndex = (has ? k === i : k === 0) ? 0 : -1; });
    }
    function choose(i, emit) { const nv = options[i].value; if (nv === cur) return; cur = nv; paint(); if (emit) onChange(nv); }
    paint();
    return { el, set(v) { cur = v; paint(); } };
}

/* ============================================================ switch */
export function switchCtl({ checked, onChange, label, text, live }) {
    const knob = h("span", { class: "sw-knob" });
    const track = h("button", { type: "button", class: "sw" + (live ? " live" : ""), role: "switch", "aria-checked": String(!!checked), "aria-label": label || null }, knob);
    const txt = text ? h("span", { class: "sw-text", onclick: () => track.click() }, text) : null; // its words are a label: clicking them flips the switch
    const el = h("span", { class: "sw-wrap" }, track, txt);
    let on = !!checked;
    const paint = () => track.setAttribute("aria-checked", String(on));
    track.addEventListener("click", () => { on = !on; paint(); onChange(on); });
    return { el, btn: track, set(v) { on = !!v; paint(); }, get on() { return on; } };
}

/* ============================================================ key capture */
const GLYPH = { Cmd: "⌘", Shift: "⇧", Alt: "⌥", Ctrl: "⌃", Return: "⏎", Escape: "Esc", Tab: "⇥", Delete: "⌫", Space: "Space", Fn: "fn", Left: "Left", Right: "Right", Up: "Up", Down: "Down" };
export const isChord = (t) => /^(?:(?:Cmd|Ctrl|Alt|Shift|Fn)\+)*(?:[A-Za-z0-9]|F\d{1,2}|Left|Right|Up|Down|Space|Tab|Return|Escape|Delete|[=\-\[\];',.\/`])$/.test(t);
export const MODS = ["Cmd", "Ctrl", "Alt", "Shift", "Fn"];
export function keycaps(spec, cls = "") {
    const box = h("span", { class: "caps " + cls });
    if (!spec) return box;
    for (const t of String(spec).split("+")) box.append(h("kbd", { class: (/^[A-Za-z]$/.test(t) ? "let " : "") + "cap" + (t.length > 1 && !GLYPH[t] ? " word" : "") + (GLYPH[t] && GLYPH[t].length > 1 ? " word" : "") + (GLYPH[t] && GLYPH[t].length === 1 ? " gl" : "") }, GLYPH[t] || t));
    return box;
}
function chordOf(ev) {
    let k = ev.key;
    if (/^Key[A-Z]$/.test(ev.code)) k = ev.code.slice(3);
    else if (/^Digit\d$/.test(ev.code)) k = ev.code.slice(5);
    else k = ({ ArrowLeft: "Left", ArrowRight: "Right", ArrowUp: "Up", ArrowDown: "Down", " ": "Space", Enter: "Return", Backspace: "Delete", Escape: "Escape" })[k] || (k.length === 1 ? k.toUpperCase() : k);
    const m = [ev.metaKey && "Cmd", ev.ctrlKey && "Ctrl", ev.altKey && "Alt", ev.shiftKey && "Shift"].filter(Boolean);
    return [...m, k].join("+");
}
const RESERVED = new Set(["Cmd+Q", "Cmd+Tab", "Cmd+Space", "Cmd+H", "Cmd+Alt+Escape"]);
export function keyCapture({ value, onCommit, label = "Shortcut", live }) {
    let cur = value || "", listening = false, pendingChord = "";
    const body = h("span", { class: "kc-body" });
    const btn = h("button", { type: "button", class: "kc", "aria-label": "" }, body);
    const note = h("div", { class: "kc-note", "aria-live": "polite" });
    const recents = h("div", { class: "kc-recent" });
    const el = h("div", { class: "kc-wrap" }, btn, note, recents);
    const paint = () => {
        btn.classList.toggle("listen", listening);
        btn.setAttribute("aria-label", `${label}: ${cur || "none"}. Press Enter to record a new one`);
        body.replaceChildren();
        if (listening) {
            const held = pendingChord || "";
            body.append(held ? keycaps(held, "holding") : h("span", { class: "kc-hint" }, "Press keys", h("i", { class: "caret" })));
        } else body.append(cur ? keycaps(cur) : h("span", { class: "kc-hint" }, "Click to record"));
    };
    function paintRecents() {
        recents.replaceChildren();
        const rs = store.get("recentKeys", []).filter((r) => r !== cur).slice(0, 4);
        for (const r of rs) recents.append(h("button", { type: "button", class: "chip ghostchip", "aria-label": "Use " + r, onclick: () => commit(r) }, keycaps(r)));
    }
    function commit(chord) {
        const rs = [chord, ...store.get("recentKeys", []).filter((x) => x !== chord)].slice(0, 6); store.set("recentKeys", rs);
        cur = chord; listening = false; pendingChord = ""; paint(); paintRecents();
        note.className = "kc-note" + (RESERVED.has(chord) ? " warn" : ""); note.textContent = RESERVED.has(chord) ? "macOS keeps this combination." : "";
        body.querySelectorAll(".cap").forEach((c, i) => anim(c, [{ transform: "scale(.92)" }, { transform: "scale(1)" }], { duration: 220, delay: i * 30, easing: "cubic-bezier(.2,1.2,.4,1)" }));
        onCommit(chord);
    }
    function stop() { listening = false; pendingChord = ""; paint(); document.removeEventListener("keydown", onKey, true); document.removeEventListener("keyup", onUp, true); }
    function start() { if (listening) return; listening = true; pendingChord = ""; note.textContent = ""; note.className = "kc-note"; paint(); document.addEventListener("keydown", onKey, true); document.addEventListener("keyup", onUp, true); }
    let lastChord = "";
    function onKey(e) {
        if (e.key === "Tab" && !e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey) { stop(); return; } // Tab alone leaves the capture
        e.preventDefault(); e.stopPropagation();
        if (e.key === "Escape" && !e.metaKey && !e.ctrlKey && !e.altKey) { stop(); btn.focus(); return; }
        if ((e.key === "Backspace" || e.key === "Delete") && !e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey) { stop(); cur = ""; paint(); onCommit(null); return; }
        if (["Meta", "Control", "Alt", "Shift"].includes(e.key)) { pendingChord = [e.metaKey && "Cmd", e.ctrlKey && "Ctrl", e.altKey && "Alt", e.shiftKey && "Shift"].filter(Boolean).join("+"); paint(); return; }
        lastChord = chordOf(e); pendingChord = lastChord; paint();
    }
    function onUp(e) {
        if (["Meta", "Control", "Alt", "Shift"].includes(e.key)) { if (!lastChord) { pendingChord = [e.metaKey && "Cmd", e.ctrlKey && "Ctrl", e.altKey && "Alt", e.shiftKey && "Shift"].filter(Boolean).join("+"); paint(); } return; }
        if (lastChord) { const c = lastChord; lastChord = ""; stop(); commit(c); btn.focus(); }
    }
    btn.addEventListener("click", start);
    btn.addEventListener("blur", () => { if (listening) stop(); });
    paint(); paintRecents();
    return {
        el, btn,
        set(v) { if (listening) return; cur = v || ""; paint(); paintRecents(); },
        setError(msg) {
            btn.classList.toggle("err", !!msg);
            if (msg && !reduced()) anim(btn, [{ transform: "translateX(0)" }, { transform: "translateX(-2px)" }, { transform: "translateX(2px)" }, { transform: "translateX(-2px)" }, { transform: "translateX(2px)" }, { transform: "translateX(0)" }], { duration: 180 });
        },
    };
}

/* ============================================================ text field, list editor */
export function textField({ value, onInput, placeholder, label, id, mono }) {
    const input = h("input", { type: "text", class: "tf-in" + (mono ? " mono" : ""), id, placeholder: placeholder || "", "aria-label": label || null, autocomplete: "off", spellcheck: "false" });
    const clear = h("button", { type: "button", class: "icon-btn xs", "aria-label": "Clear", tabindex: "-1", hidden: true, onclick: () => { input.value = ""; sync(); onInput(""); input.focus(); } }, icon("x"));
    const el = h("div", { class: "tf" }, input, clear);
    const sync = () => { clear.hidden = !input.value; };
    input.value = value ?? ""; sync();
    input.addEventListener("input", () => { sync(); onInput(input.value); });
    return { el, input, set(v) { if (document.activeElement !== input) { input.value = v ?? ""; sync(); } } };
}
export function listEditor({ values, onChange, label, placeholder = "Argument" }) {
    let list = Array.isArray(values) ? values.slice() : [];
    const rows = h("div", { class: "le-rows" });
    const add = h("button", { type: "button", class: "btn ghost sm", onclick: () => { list.push(""); paint(); rows.querySelector(".le-row:last-child input")?.focus(); } }, icon("plus"), "Add argument");
    const el = h("div", { class: "le" }, rows, add);
    const emit = () => onChange(list.filter((x) => x !== ""));
    function paint() {
        rows.replaceChildren();
        list.forEach((val, i) => {
            const input = h("input", { type: "text", class: "tf-in mono", value: val, placeholder, "aria-label": `${label || "Argument"} ${i + 1}`, spellcheck: "false", autocomplete: "off" });
            input.addEventListener("input", () => { list[i] = input.value; emit(); });
            rows.append(h("div", { class: "le-row" }, h("div", { class: "tf" }, input), h("button", { type: "button", class: "icon-btn xs", "aria-label": `Remove ${label || "argument"} ${i + 1}`, onclick: () => { list.splice(i, 1); paint(); emit(); } }, icon("x"))));
        });
        if (!list.length) rows.append(h("div", { class: "le-empty" }, "No arguments."));
    }
    paint();
    return { el, set(v) { if (!el.contains(document.activeElement)) { list = Array.isArray(v) ? v.slice() : []; paint(); } } };
}

/* ============================================================ two-step inline confirm (never a browser dialog) */
export function confirmButton({ label, confirmLabel, ico = "trash-2", onConfirm, cls = "btn ghost sm danger-text" }) {
    const txt = h("span", {}, label);
    const b = h("button", { type: "button", class: cls }, icon(ico), txt);
    let t = 0, armed = false;
    const reset = () => { armed = false; clearTimeout(t); b.classList.remove("armed"); txt.textContent = label; };
    b.addEventListener("click", () => {
        if (b.getAttribute("aria-disabled") === "true") return;
        if (!armed) { armed = true; b.classList.add("armed"); txt.textContent = confirmLabel || "Confirm"; t = setTimeout(reset, 4000); return; }
        reset(); onConfirm();
    });
    b.addEventListener("blur", reset);
    return b;
}

/* ============================================================ mini tooltip (icon buttons) */
let tipEl = null, tipT = 0, lastClose = 0;
function ensureTip() { if (!tipEl) { tipEl = h("div", { class: "minitip", role: "tooltip" }); portalEl().append(tipEl); } return tipEl; }
/* no hover tooltip while a popover or menu is open: it would float behind the sheet (or fight it for the same spot) */
const popOpen = () => !!document.querySelector('[aria-expanded="true"], .pop');
function showTip(target, immediate) {
    const text = target.dataset.tip; if (!text || popOpen()) return;
    const go = () => {
        if (popOpen()) return;
        const t = ensureTip(); t.replaceChildren(...[h("span", {}, text), target.dataset.kbd ? h("kbd", { class: "cap sm" }, target.dataset.kbd) : null].filter(Boolean));
        t.style.visibility = "hidden"; t.classList.add("on");
        const r = target.getBoundingClientRect(), w = t.offsetWidth, hh = t.offsetHeight;
        let top = r.bottom + 8; if (top + hh > innerHeight - 8) top = r.top - hh - 8;
        t.style.left = clamp(r.left + r.width / 2 - w / 2, 8, innerWidth - w - 8) + "px"; t.style.top = top + "px"; t.style.visibility = "";
        anim(t, [{ opacity: 0, transform: "translateY(4px)" }, { opacity: 1, transform: "none" }], { duration: 140, easing: EASE_OUT });
    };
    clearTimeout(tipT);
    if (immediate || performance.now() - lastClose < 400) go(); else tipT = setTimeout(go, 220);
}
function hideTip() { clearTimeout(tipT); if (tipEl && tipEl.classList.contains("on")) { tipEl.classList.remove("on"); lastClose = performance.now(); } }
/* text that is cut with an ellipsis gets its full text as a tip, set the moment the pointer arrives (only when it really is cut) */
const TRUNC = ".lt td, .dr-sub, .dk-act, .dr-title";
function truncTip(el) {
    const cut = el.scrollWidth > el.clientWidth + 1 || [...el.querySelectorAll("span")].some((c) => c.scrollWidth > c.clientWidth + 1);
    const txt = el.textContent.replace(/\s+/g, " ").trim();
    if (cut && txt) { el.setAttribute("data-tip", txt); el.setAttribute("data-trunc-tip", ""); }
    else if (el.hasAttribute("data-trunc-tip")) { el.removeAttribute("data-tip"); el.removeAttribute("data-trunc-tip"); }
}
export function initMiniTips() {
    document.addEventListener("pointerover", (e) => { const t = e.target.closest && e.target.closest(TRUNC); if (t && (!t.hasAttribute("data-tip") || t.hasAttribute("data-trunc-tip"))) truncTip(t); }, true);
    document.addEventListener("pointerover", (e) => { const t = e.target.closest && e.target.closest("[data-tip]"); if (t) showTip(t); });
    document.addEventListener("pointerout", (e) => { const t = e.target.closest && e.target.closest("[data-tip]"); if (t && !t.contains(e.relatedTarget)) hideTip(); });
    document.addEventListener("pointerdown", hideTip, true);
    document.addEventListener("focusin", (e) => { const t = e.target.closest && e.target.closest("[data-tip]"); if (t && t.matches(":focus-visible")) showTip(t, true); });
    document.addEventListener("focusout", hideTip);
}

/* ============================================================ toasts (one at a time; later ones merge into it with a +N count) */
let cur = null;
/** drop whatever toast is showing (a change in another app must not keep offering its Undo) */
export function clearToasts() { if (cur && !cur.gone) cur.dismiss(true); }
export function toast({ msg, tone = "ok", undo, redo, key, ms = 6000 }) {
    const box = $("#toasts");
    const ic = () => (tone === "bad" ? icon("triangle-alert", 16, "t-bad") : tone === "info" ? icon("info", 16, "t-info") : icon("check", 16, "t-ok"));
    if (cur && !cur.gone && !(key && cur.key === key && cur.msg === msg)) cur.dismiss(true); // only the same message under the same key merges ("and 1 more"); anything else replaces it
    if (cur && !cur.gone) {
        cur.n += 1; cur.key = key || cur.key;
        cur.set({ msg, tone, undo, redo, ms, ic: ic() });
        return cur;
    }
    const msgEl = h("span", { class: "toast-msg" }), count = h("span", { class: "toast-n", "aria-hidden": "true", hidden: true });
    const actBox = h("span", { class: "toast-acts" });
    const el = h("div", { class: "toast", role: "status" }, h("span", { class: "toast-ic" }), msgEl, count, actBox);
    const rec = { key, el, n: 1, gone: false, timer: 0, paused: false, tone };
    cur = rec;
    function dismiss(instant) {
        if (rec.gone) return; rec.gone = true; clearTimeout(rec.timer); if (cur === rec) cur = null;
        const a = instant ? null : anim(el, [{ opacity: 1, transform: "none" }, { opacity: 0, transform: "translateY(4px)" }], { duration: 165, easing: EASE_IN, fill: "forwards" });
        if (a) a.onfinish = () => el.remove(); else el.remove();
    }
    rec.dismiss = dismiss;
    const arm = () => { clearTimeout(rec.timer); if (rec.tone !== "bad" && !rec.paused) rec.timer = setTimeout(() => dismiss(), rec.ms); };
    rec.set = (o) => {
        rec.msg = o.msg; rec.tone = o.tone; rec.ms = o.ms;
        el.querySelector(".toast-ic").replaceChildren(o.ic);
        msgEl.textContent = o.msg; el.setAttribute("role", o.tone === "bad" ? "alert" : "status");
        count.hidden = rec.n < 2; count.textContent = "and " + (rec.n - 1) + " more";
        actBox.replaceChildren(...[
            o.undo ? h("button", { type: "button", class: "toast-act", onclick: () => { dismiss(); o.undo(); } }, "Undo") : null,
            o.redo ? h("button", { type: "button", class: "toast-act", onclick: () => { dismiss(); o.redo(); } }, "Redo") : null,
            h("button", { type: "button", class: "toast-act x", "aria-label": "Dismiss", onclick: () => dismiss() }, icon("x"))].filter(Boolean));
        if (rec.n > 1 && !reduced()) anim(count, [{ transform: "scale(1.25)" }, { transform: "none" }], { duration: 220, easing: EASE_OUT });
        arm();
    };
    el.addEventListener("pointerenter", () => { rec.paused = true; clearTimeout(rec.timer); });
    el.addEventListener("pointerleave", () => { rec.paused = false; arm(); });
    el.addEventListener("focusin", () => { rec.paused = true; clearTimeout(rec.timer); });
    el.addEventListener("focusout", () => { rec.paused = false; arm(); });
    rec.set({ msg, tone, undo, redo, ms, ic: ic() });
    box.append(el);
    anim(el, [{ opacity: 0, transform: "translateY(6px) scale(.98)" }, { opacity: 1, transform: "none" }], { duration: 180, easing: EASE_OUT });
    return rec;
}

/* ============================================================ app icon tile (real macOS icon, graphite monogram fallback) */
const TINTS = 6;
function hash(str) { let x = 0; for (let i = 0; i < str.length; i++) x = (x * 31 + str.charCodeAt(i)) >>> 0; return x; }
export function appTile(app, { size = 28, url } = {}) {
    const el = h("span", { class: "tile t" + size });
    if (app.isGlobal) { el.classList.add("t-global"); el.append(icon("link", 16)); return el; }
    el.classList.add("tint" + (hash(app.id) % TINTS));
    el.append(h("span", { class: "tile-mono", "aria-hidden": "true" }, (app.name || app.id).trim().charAt(0).toUpperCase()));
    if (url) {
        const img = h("img", { class: "tile-img", width: size, height: size, alt: "", decoding: "async", draggable: "false" });
        img.addEventListener("load", () => { el.classList.add("has-img"); anim(img, [{ opacity: 0 }, { opacity: 1 }], { duration: 140 }); });
        img.addEventListener("error", () => img.remove());
        img.src = url;
        el.append(img);
    }
    return el;
}

/* ============================================================ chips */
export function stateChip(state, text) {
    // state: set | inh | none | default
    const glyph = state === "set" ? h("i", { class: "dot-fill" }) : state === "inh" ? icon("link", 16, "c-ic") : state === "none" ? icon("square-dashed", 16, "c-ic") : icon("circle-dashed", 16, "c-ic");
    return h("span", { class: "chip st-" + state }, glyph, h("span", {}, text));
}
export const KIND = { knob: ["circle-dot", "Knob"], dial: ["disc-3", "Dial"], slider: ["sliders-vertical", "Slider"], key: ["square", "Key"], button: ["circle", "Button"] };
