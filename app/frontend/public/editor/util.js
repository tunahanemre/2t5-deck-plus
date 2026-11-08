// util.js: DOM helpers, icons, springs, the single rAF loop, storage. No dependencies.
export const $ = (s, r = document) => r.querySelector(s);
export const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));
export const clamp = (v, a, b) => Math.min(b, Math.max(a, v));
export const lerp = (a, b, t) => a + (b - a) * t;
const HTML_NS = "http://www.w3.org/1999/xhtml";
const SVG_NS = "http://www.w3.org/2000/svg";

/* browsers drop tabs, newlines and control characters inside a URL scheme ("java\tscript:" runs), so strip every whitespace / control / invisible character before looking at the scheme; data: is refused whole */
function unsafeUrl(v) {
    const u = String(v).replace(/[\u0000-\u0020\u007f-\u00a0\u1680\u180e\u2000-\u200f\u2028-\u202f\u205f-\u2064\u3000\ufeff]/g, "").toLowerCase();
    return /^(javascript|vbscript|data):/.test(u);
}
function apply(n, attrs, kids) {
    for (const [k, v] of Object.entries(attrs || {})) {
        if (v == null || v === false) continue;
        if (k === "class") n.setAttribute("class", v);
        else if (k.startsWith("on") && typeof v === "function") n.addEventListener(k.slice(2), v);
        else if (k === "style" && typeof v === "object") Object.assign(n.style, v);
        else if (/^on/i.test(k) || ((/^(xlink:)?href$|^src$|^action$|^formaction$/i.test(k)) && unsafeUrl(v))) continue; // never an event-handler attribute or a script URL
        else n.setAttribute(k, v === true ? "" : v);
    }
    for (const c of kids.flat(2)) if (c != null && c !== false) n.append(c.nodeType ? c : document.createTextNode(String(c)));
    return n;
}
/** HTML element. h("div", {class:"x", onclick}, child, ...) */
export const h = (tag, attrs, ...kids) => apply(document.createElementNS(HTML_NS, tag), attrs, kids);
/** SVG element. */
export const s = (tag, attrs, ...kids) => apply(document.createElementNS(SVG_NS, tag), attrs, kids);

/* ---------------------------------------------------------------- icons
   One family: Lucide (ISC), vendored sprite inlined in index.html (assets/icons). 1.5px on screen at 16/20/24 (see .ic-* in CSS). */
export function icon(name, size = 16, cls) {
    const n = s("svg", { class: "ic ic-" + size + (cls ? " " + cls : ""), "aria-hidden": "true", focusable: "false" });
    n.append(s("use", { href: "#i-" + name }));
    return n;
}

/* ---------------------------------------------------------------- motion */
const mq = window.matchMedia ? matchMedia("(prefers-reduced-motion: reduce)") : null;
const QS = new URLSearchParams(location.search);
/* the OS flag wins only when the app switch is on "system"; WKWebView honours the system flag only, so the app has its own override. */
let reduceOverride = null; try { const v = localStorage.getItem("ldeck.reduce"); reduceOverride = v == null ? null : JSON.parse(v); } catch (e) { /* private mode */ }
export const reduced = () => reduceOverride === true || (reduceOverride == null && ((mq && mq.matches) || QS.has("reduce"))) || QS.has("reduce");
function syncReduce() { document.documentElement.toggleAttribute("data-reduce", !!reduced()); }
export function setReduceOverride(v) { reduceOverride = v; try { if (v == null) localStorage.removeItem("ldeck.reduce"); else localStorage.setItem("ldeck.reduce", JSON.stringify(v)); } catch (e) { /* private mode */ } syncReduce(); }
export const reduceOverrideValue = () => reduceOverride;
syncReduce();
if (mq && mq.addEventListener) mq.addEventListener("change", syncReduce);
/* high contrast: OS preference (prefers-contrast: more) or the app switch */
let contrastOverride = null; try { const v = localStorage.getItem("ldeck.contrast"); contrastOverride = v == null ? null : JSON.parse(v); } catch (e) { /* private mode */ }
export function setContrastOverride(v) { contrastOverride = v; try { if (v == null) localStorage.removeItem("ldeck.contrast"); else localStorage.setItem("ldeck.contrast", JSON.stringify(v)); } catch (e) { /* private mode */ } if (v == null) document.documentElement.removeAttribute("data-contrast"); else document.documentElement.setAttribute("data-contrast", v ? "more" : "normal"); }
export const contrastOverrideValue = () => contrastOverride;
if (contrastOverride != null) document.documentElement.setAttribute("data-contrast", contrastOverride ? "more" : "normal");
if (QS.has("contrast")) document.documentElement.setAttribute("data-contrast", "more");

/** Semi-implicit Euler spring, 1/120 s sub-steps. Params per the direction: snap 520/36, smooth 300/30, soft 170/24. */
export const SPRING = { snap: [520, 36], smooth: [300, 30], soft: [170, 24] };
export class Spring {
    /** kind: a SPRING name or a [stiffness, damping] pair. eps / veps = rest tolerances (position / velocity). */
    constructor(kind, x = 0, eps = 0.002, veps = 0.01) { [this.k, this.c] = Array.isArray(kind) ? kind : SPRING[kind]; this.x = x; this.v = 0; this.t = x; this.eps = eps; this.veps = veps; }
    step(dt) {
        if (reduced()) { this.x = this.t; this.v = 0; return; }
        let n = Math.max(1, Math.round(dt * 120));
        const sub = dt / n;
        while (n--) { this.v += (-this.k * (this.x - this.t) - this.c * this.v) * sub; this.x += this.v * sub; }
    }
    get rest() { return Math.abs(this.x - this.t) < this.eps && Math.abs(this.v) < this.veps; }
    snapTo(x) { this.x = this.t = x; this.v = 0; }
}

/** One rAF loop for every per-frame animation. Each task returns true while it still needs frames. */
const tasks = new Set();
let raf = 0, lastT = 0;
function frame(t) {
    const dt = Math.min(0.05, (t - (lastT || t)) / 1000);
    lastT = t;
    for (const fn of Array.from(tasks)) { if (!fn(dt, t)) tasks.delete(fn); }
    raf = tasks.size ? requestAnimationFrame(frame) : 0;
    if (!raf) lastT = 0;
}
export const loop = {
    add(fn) { tasks.add(fn); if (!raf) raf = requestAnimationFrame(frame); },
    get busy() { return tasks.size > 0; },
};

/** Web Animations helper that respects reduced motion (duration 0 collapses to the final state). */
export function anim(el, frames, opts) {
    if (!el || !el.animate) return null;
    const o = { fill: "backwards", ...opts };
    if (reduced()) o.duration = Math.min(o.duration || 0, 120);
    return el.animate(frames, o);
}
export const EASE_OUT = "cubic-bezier(.22,1,.36,1)";
export const EASE_IN = "cubic-bezier(.5,0,.75,0)";
export function willChange(el, props = "transform, opacity", ms = 600) {
    if (!el) return;
    el.style.willChange = props;
    clearTimeout(el._wc);
    el._wc = setTimeout(() => { el.style.willChange = ""; }, ms);
}


/* ---------------------------------------------------------------- the ONE interruptible spring helper
   springTo(el, {tx, ty, s, o}, kind) moves an element's translate / scale / opacity with springs that keep their
   velocity when retargeted mid-flight (a drawer closed while it is still opening just turns around). Rest rule:
   |x - target| < 0.5 px and |v| < 5 px/s (Apple: don't wait for the theoretical settle). Reduced motion jumps.
   Parameter table (duration, bounce 0 -> critically damped; k = (2pi/d)^2, c = 2*zeta*sqrt(k)): panel .40s = 247/31.4,
   deck fit .46s = 187/27.3, snap .30s bounce .15 = 439/35.6. */
export const SPRINGS = { panel: [247, 31.4], deck: [187, 27.3], snap: [439, 35.6] };
const PX = { tx: [0.5, 5], ty: [0.5, 5], s: [0.0005, 0.005], o: [0.004, 0.02] };
export function springTo(el, to, kind = "panel", onRest) {
    if (!el) return;
    const st = el._sp || (el._sp = { p: {}, run: false, kind });
    for (const k of Object.keys(to)) {
        const sp = st.p[k] || (st.p[k] = new Spring(SPRINGS[kind] || SPRINGS.panel, to[k], PX[k][0], PX[k][1]));
        if (st.kind !== kind) { sp.k = (SPRINGS[kind] || SPRINGS.panel)[0]; sp.c = (SPRINGS[kind] || SPRINGS.panel)[1]; }
        sp.t = to[k];
    }
    st.kind = kind; st.onRest = onRest;
    const paint = () => {
        const p = st.p, tx = p.tx ? p.tx.x : 0, ty = p.ty ? p.ty.x : 0, sc = p.s ? p.s.x : 1;
        el.style.transform = `translate3d(${tx.toFixed(2)}px, ${ty.toFixed(2)}px, 0) scale(${sc.toFixed(4)})`;
        if (p.o) el.style.opacity = String(Math.min(1, Math.max(0, p.o.x)));
    };
    if (reduced()) { for (const k of Object.keys(st.p)) { st.p[k].snapTo(st.p[k].t); } paint(); if (onRest) onRest(); return; }
    if (st.run) return;
    st.run = true;
    loop.add((dt) => {
        let busy = false;
        for (const k of Object.keys(st.p)) { const sp = st.p[k]; sp.step(dt); if (!sp.rest) busy = true; else sp.snapTo(sp.t); }
        paint();
        if (!busy) { st.run = false; const f = st.onRest; st.onRest = null; if (f) f(); }
        return busy;
    });
}
/** place an element at a spring state with no motion (first paint, resize) */
export function springSet(el, to) { if (!el) return; const st = el._sp || (el._sp = { p: {}, run: false, kind: "panel" }); for (const k of Object.keys(to)) { if (st.p[k]) st.p[k].snapTo(to[k]); else st.p[k] = new Spring(SPRINGS.panel, to[k], PX[k][0], PX[k][1]); } const p = st.p; el.style.transform = `translate3d(${(p.tx ? p.tx.x : 0).toFixed(2)}px, ${(p.ty ? p.ty.x : 0).toFixed(2)}px, 0) scale(${(p.s ? p.s.x : 1).toFixed(4)})`; if (p.o) el.style.opacity = String(p.o.x); }

/* ---------------------------------------------------------------- storage (every access guarded) */
export const store = {
    get(k, d) { try { const v = localStorage.getItem("ldeck." + k); return v == null ? d : JSON.parse(v); } catch (e) { return d; } },
    set(k, v) { try { localStorage.setItem("ldeck." + k, JSON.stringify(v)); } catch (e) { /* private mode */ } },
};

/* ---------------------------------------------------------------- formatting */
export function fmt(v, max = 4) {
    if (v == null || v === "") return "";
    const n = Number(v);
    if (!Number.isFinite(n)) return String(v);
    return String(Number(n.toFixed(max)));
}
export const decimals = (step) => Math.min(4, (String(step ?? 1).split(".")[1] || "").length);
export function compact(n) {
    const a = Math.abs(n);
    if (a >= 1e6) return fmt(n / 1e6, 1) + "M";
    if (a >= 1e3) return fmt(n / 1e3, 1) + "k";
    return a < 10 ? fmt(n, 2) : fmt(n, 0);
}
/** "layer-2" -> "Layer 2", "edit-shift" -> "Edit shift" */
export function bankLabel(b) {
    /* Resolve's "col" page is read Color everywhere ("Color", "Color 2"); the bank ids and the deck's printed Col key are unchanged */
    const w = String(b).split("-").map((x, i) => (i === 0 && x === "col" ? "color" : x));
    const t = w.join(" ");
    return t.charAt(0).toUpperCase() + t.slice(1);
}
export const debounce = (fn, ms) => { let t; const d = (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); }; d.cancel = () => clearTimeout(t); d.flush = (...a) => { clearTimeout(t); fn(...a); }; return d; };
