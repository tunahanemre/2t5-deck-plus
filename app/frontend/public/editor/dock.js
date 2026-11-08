// dock.js: the live-test instrument (rotating dial with decaying trail, press ripple, ticks/s arc gauge with
// peak hold, accel / output readouts, "handled by" chip). Hosted by the bottom Live test drawer.
// One rAF task integrates the springs and draws two canvases; it sleeps when everything is at rest.
import { h, icon, clamp, lerp, loop, Spring, reduced, fmt, compact } from "./util.js";
import { keycaps, appTile } from "./ui.js";

const DEG = Math.PI / 180;
/* The dial face has 60 marks, 6 degrees apart. One tick the deck reports moves the needle exactly one mark, so one click reads as one tick (it was 15 degrees = 2.5 marks, which looked like 2-3 ticks per click). */
const MARKS = 60, DEG_PER_TICK = 360 / MARKS;
const tok = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();

/** "1 minutes" -> "1 minute": a unit word is singular for exactly one */
const unitFor = (n, u) => (Math.abs(Number(n)) === 1 && /^[a-z]{4,}s$/i.test(String(u)) && !/ss$/i.test(u) ? String(u).slice(0, -1) : u);
export function createDock() {
    /* ---- DOM ---- */
    const dialCv = h("canvas", { class: "dk-cv dial-cv", "aria-hidden": "true" });
    const dialName = h("div", { class: "dk-name" }, h("b", {}, "—"), h("span", {}, ""));
    const dial = h("div", { class: "dk-dial" }, dialCv, dialName);
    const gaugeCv = h("canvas", { class: "dk-cv gauge-cv", "aria-hidden": "true" });
    const gNum = h("div", { class: "dk-gnum" }, h("b", {}, "0"), h("span", {}, "ticks/s"));
    const gauge = h("div", { class: "dk-gauge" }, gaugeCv, gNum);
    const rRaw = h("b", { class: "ro-v" }, "—"), rAcc = h("b", { class: "ro-v" }, "—"), rFine = h("b", { class: "pill off" }, "off");
    const rLat = h("b", { class: "ro-v" }, "\u2014");
    const latBox = h("div", { class: "ro" }, h("small", { "data-tip": "Time from the event reaching the app deck to the action being posted. Not the deck\u2019s MIDI latency." }, "Latency"), rLat);
    const reads = h("div", { class: "dk-read" },
        h("div", { class: "ro" }, h("small", { "data-tip": "Raw encoder ticks: how many notches the knob reported" }, "Turns"), rRaw), h("div", { class: "ro" }, h("small", { "data-tip": "Acceleration: fast turns are boosted" }, "Speed boost"), rAcc), h("div", { class: "ro" }, h("small", { "data-tip": "Fine mode: a slower, more precise response" }, "Slow mode"), rFine), latBox);
    const handled = h("div", { class: "dk-handled" });
    const actName = h("div", { class: "dk-act" }, "No input yet");
    const outV = h("b", { class: "dk-outv" }, "—"), outU = h("span", { class: "dk-outu" }, "");
    const outCaps = h("div", { class: "dk-outcaps", hidden: true });
    const outNote = h("span", { class: "dk-note", hidden: true, role: "status" }, icon("info", 16, "c-ic"), h("span", {}, "Editor focused: not fired"));
    const why = h("div", { class: "dk-why" });
    const out = h("div", { class: "dk-out" }, h("div", { class: "dk-top" }, handled, actName), h("div", { class: "ro dk-outbox" }, h("small", {}, "Output"), h("div", { class: "dk-outrow" }, outV, outU, outCaps, outNote), why));
    const idleMsg = h("div", { class: "dk-idle" }, "Turn a knob or press a key on the deck");
    gauge.append(idleMsg);
    const spCv = h("canvas", { class: "dk-cv", "aria-hidden": "true" });
    const spLabel = h("small", {}, "Waiting for input");
    const spark = h("div", { class: "dk-spark" }, spCv, spLabel);
    const el = h("div", { class: "dock idle" }, dial, gauge, spark, reads, out);

    /* ---- state ---- */
    const D = {
        visible: false, kind: "knob", name: "", hasEvent: false,
        angle: new Spring("snap", 0), key: new Spring("snap", 0), slider: new Spring("snap", 0.5), gauge: new Spring("soft", 0),
        trail: [], ripples: [], peak: { v: 0, t: 0 }, hist: [], lastEvt: 0, outTarget: null, outShown: 0, outDec: 0, outSign: "",
        gaugeVal: 0, lastNum: -1, colors: null, sizes: { dw: 168, dh: 168, gw: 220, gh: 168, sw: 200, sh: 120, dpr: 1 },
    };
    const colors = () => (D.colors = D.colors || { accent: tok("--accent"), hi: tok("--accent-hi"), hl1: tok("--hl-1"), hl2: tok("--hl-2"), hl3: tok("--hl-3"), t1: tok("--t1"), t2: tok("--t2"), t3: tok("--t3"), dialHi: tok("--dial-hi"), dialLo: tok("--dial-lo"), s3: tok("--s3"), glow: tok("--accent-glow") });

    function sizeCanvas(cv, w, hh) {
        const dpr = Math.min(2, window.devicePixelRatio || 1);
        cv.width = Math.round(w * dpr); cv.height = Math.round(hh * dpr);
        cv.style.width = w + "px"; cv.style.height = hh + "px";
        D.sizes.dpr = dpr;
    }
    const ro = new ResizeObserver(() => {
        const d = dial.getBoundingClientRect(), g = gauge.getBoundingClientRect();
        if (d.width) { D.sizes.dw = d.width; D.sizes.dh = d.height; sizeCanvas(dialCv, d.width, d.height); }
        if (g.width) { D.sizes.gw = g.width; D.sizes.gh = g.height; sizeCanvas(gaugeCv, g.width, g.height); }
        const sp = spark.getBoundingClientRect(); if (sp.width) { D.sizes.sw = sp.width; D.sizes.sh = sp.height; sizeCanvas(spCv, sp.width, sp.height); }
        wake(true);
    });
    ro.observe(dial); ro.observe(gauge); ro.observe(spark);

    /* ---- drawing ---- */
    function drawDial(now) {
        const c = colors(), { dw, dh, dpr } = D.sizes, ctx = dialCv.getContext("2d");
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0); ctx.clearRect(0, 0, dw, dh);
        const cx = dw / 2, cy = dh / 2, R = Math.min(dw, dh) / 2 - 4;
        const idle = !D.hasEvent || now - D.lastEvt > 4000;
        ctx.globalAlpha = idle ? 0.4 : 1;
        // ticks: 60, every 5th longer
        for (let i = 0; i < MARKS; i++) {
            const a = i * DEG_PER_TICK * DEG - Math.PI / 2, long = i % 5 === 0, r1 = R, r0 = R - (long ? 8 : 4);
            ctx.strokeStyle = long ? c.hl3 : c.hl2; ctx.lineWidth = 1; ctx.beginPath();
            ctx.moveTo(cx + Math.cos(a) * r0, cy + Math.sin(a) * r0); ctx.lineTo(cx + Math.cos(a) * r1, cy + Math.sin(a) * r1); ctx.stroke();
        }
        const press = D.key.x; // 0..1 depress
        const bodyR = R - 14;
        if (D.kind === "key" || D.kind === "button") {
            const w = 96, hgt = 72, k = 1 - 0.04 * press;
            ctx.save(); ctx.translate(cx, cy); ctx.scale(k, k);
            const g = ctx.createLinearGradient(0, -hgt / 2, 0, hgt / 2); g.addColorStop(0, c.dialHi); g.addColorStop(1, c.dialLo);
            ctx.fillStyle = g; rr(ctx, -w / 2, -hgt / 2, w, hgt, 12); ctx.fill();
            ctx.strokeStyle = press > 0.3 ? c.hi : c.hl3; ctx.lineWidth = 1.5; rr(ctx, -w / 2, -hgt / 2, w, hgt, 12); ctx.stroke();
            ctx.restore();
        } else if (D.kind === "slider") {
            const tw = 10, th = 112, tx = cx - tw / 2, ty = cy - th / 2;
            ctx.fillStyle = c.s3; rr(ctx, tx, ty, tw, th, 5); ctx.fill();
            const p = clamp(D.slider.x, 0, 1), y = ty + (1 - p) * (th - 20);
            ctx.fillStyle = c.accent; ctx.globalAlpha *= 0.7; rr(ctx, tx, ty + th - (th * p), tw, th * p, 5); ctx.fill(); ctx.globalAlpha = idle ? 0.4 : 1;
            const g = ctx.createLinearGradient(0, y, 0, y + 20); g.addColorStop(0, c.dialHi); g.addColorStop(1, c.dialLo);
            ctx.fillStyle = g; rr(ctx, cx - 22, y, 44, 20, 6); ctx.fill(); ctx.strokeStyle = c.hl3; ctx.lineWidth = 1; rr(ctx, cx - 22, y, 44, 20, 6); ctx.stroke();
        } else {
            const sc = 1 - 0.015 * press;
            const g = ctx.createRadialGradient(cx - bodyR * 0.3, cy - bodyR * 0.35, bodyR * 0.1, cx, cy, bodyR); g.addColorStop(0, c.dialHi); g.addColorStop(1, c.dialLo);
            ctx.fillStyle = g; ctx.beginPath(); ctx.arc(cx, cy, bodyR * sc, 0, Math.PI * 2); ctx.fill();
            ctx.strokeStyle = c.hl2; ctx.lineWidth = 1; ctx.stroke();
            // decaying angle trail (arc on the rim radius, last 90 degrees)
            const a0 = D.angle.x;
            ctx.lineCap = "round"; ctx.lineWidth = 3;
            for (let i = 1; i < D.trail.length; i++) {
                const p = D.trail[i - 1], q = D.trail[i], age = now - q.t;
                const al = 0.5 * Math.exp(-age / 380); if (al < 0.01) continue;
                if (Math.abs(a0 - q.a) > 90 * DEG * 1.4) continue;
                ctx.strokeStyle = c.accent; ctx.globalAlpha = al * (idle ? 0.4 : 1);
                ctx.beginPath(); ctx.arc(cx, cy, bodyR + 3, p.a - Math.PI / 2, q.a - Math.PI / 2, q.a < p.a); ctx.stroke();
            }
            ctx.globalAlpha = idle ? 0.4 : 1;
            // needle
            const a = a0 - Math.PI / 2;
            ctx.strokeStyle = c.hi; ctx.lineWidth = 2; ctx.lineCap = "round"; ctx.beginPath();
            ctx.moveTo(cx + Math.cos(a) * bodyR * 0.52, cy + Math.sin(a) * bodyR * 0.52); ctx.lineTo(cx + Math.cos(a) * bodyR * 0.92, cy + Math.sin(a) * bodyR * 0.92); ctx.stroke();
        }
        // press ripples: two rings from the centre
        ctx.globalAlpha = 1;
        for (const r of D.ripples) {
            const t = clamp((now - r.t0) / 640, 0, 1); if (t <= 0) continue;
            const e = 1 - Math.pow(1 - t, 3);
            ctx.strokeStyle = c.accent; ctx.lineWidth = 1.5; ctx.globalAlpha = 0.5 * (1 - e);
            ctx.beginPath(); ctx.arc(cx, cy, (0.4 + 0.6 * e) * R, 0, Math.PI * 2); ctx.stroke();
        }
        ctx.globalAlpha = 1;
    }
    function rr(ctx, x, y, w, hgt, r) { ctx.beginPath(); ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + hgt, r); ctx.arcTo(x + w, y + hgt, x, y + hgt, r); ctx.arcTo(x, y + hgt, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath(); }

    const SEG = 48, SWEEP = 240, START = 150;
    function drawGauge(now) {
        const c = colors(), { gw, gh, dpr } = D.sizes, ctx = gaugeCv.getContext("2d");
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0); ctx.clearRect(0, 0, gw, gh);
        const R = Math.min(gw / 2 - 6, (gh - 12) / 1.5), cx = gw / 2, cy = 6 + R;
        const v = clamp(D.gauge.x, 0, 80), lit = (v / 80) * SEG;
        ctx.lineCap = "round"; ctx.lineWidth = 2;
        for (let i = 0; i < SEG; i++) {
            const a = (START + ((i + 0.5) / SEG) * SWEEP) * DEG, on = i + 0.5 <= lit;
            let col = c.hl3;
            if (on) col = i >= SEG - 6 ? mix(c.accent, c.hi, (i - (SEG - 6)) / 5) : c.accent;
            ctx.strokeStyle = col; ctx.beginPath();
            ctx.moveTo(cx + Math.cos(a) * (R - 8), cy + Math.sin(a) * (R - 8)); ctx.lineTo(cx + Math.cos(a) * R, cy + Math.sin(a) * R); ctx.stroke();
        }
        // peak hold: holds 900ms then falls linearly in 400ms
        const age = now - D.peak.t; let pv = D.peak.v;
        if (age > 900) pv = Math.max(v, D.peak.v * (1 - clamp((age - 900) / 400, 0, 1)));
        if (age > 1300 && pv <= v) { D.peak.v = v; }
        if (pv > v + 0.5) {
            const a = (START + (clamp(pv, 0, 80) / 80) * SWEEP) * DEG;
            ctx.strokeStyle = c.t1; ctx.lineWidth = 2; ctx.beginPath();
            ctx.moveTo(cx + Math.cos(a) * (R - 11), cy + Math.sin(a) * (R - 11)); ctx.lineTo(cx + Math.cos(a) * (R + 2), cy + Math.sin(a) * (R + 2)); ctx.stroke();
        }
    }
    /* the one history trace: ticks/s over the last 5 seconds, filling the middle of the strip */
    function drawSpark(now) {
        const c = colors(), { sw, sh, dpr } = D.sizes, ctx = spCv.getContext("2d");
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0); ctx.clearRect(0, 0, sw, sh);
        const top = 28, bot = sh - 8, H = bot - top, W = sw;
        ctx.strokeStyle = c.hl1; ctx.lineWidth = 1; ctx.beginPath(); ctx.moveTo(0, bot + .5); ctx.lineTo(W, bot + .5); ctx.stroke();
        const pts = D.hist.filter((p) => now - p.t < 5000);
        const has = pts.length > 2 && pts.some((p) => p.v > 0) && now - pts[pts.length - 1].t < 5000;
        spLabel.textContent = has ? "ticks/s, last 5 s" : "idle"; spark.classList.toggle("empty", !has);
        if (has) {
            const X = (p) => W - ((now - p.t) / 5000) * W, Y = (p) => bot - (clamp(p.v, 0, 80) / 80) * H;
            ctx.beginPath(); pts.forEach((p, i) => (i ? ctx.lineTo(X(p), Y(p)) : ctx.moveTo(X(p), Y(p))));
            ctx.strokeStyle = c.t2; ctx.lineWidth = 1.5; ctx.lineJoin = "round"; ctx.stroke();
            ctx.lineTo(X(pts[pts.length - 1]), bot); ctx.lineTo(X(pts[0]), bot); ctx.closePath();
            const g = ctx.createLinearGradient(0, top, 0, bot); g.addColorStop(0, "rgba(255,255,255,.10)"); g.addColorStop(1, "rgba(255,255,255,0)"); ctx.fillStyle = g; ctx.fill();
        }
    }
    function mix(a, b, t) { return t < 0.5 ? a : b; }

    /* ---- the loop ---- */
    let sleepAt = 0;
    function frame(dt, now) {
        if (!D.visible) return false;
        const t = performance.now();
        D.angle.step(dt); D.key.step(dt); D.slider.step(dt); D.gauge.step(dt);
        if (t - D.lastEvt > 400) { D.gauge.t = 0; D.rate = 0; } // no turn for 400 ms: the rate falls to 0
        if (D.kind === "knob" || D.kind === "dial") { const last = D.trail[D.trail.length - 1]; if (!last || t - last.t >= 16) D.trail.push({ a: D.angle.x, t }); while (D.trail.length && t - D.trail[0].t > 700) D.trail.shift(); }
        D.ripples = D.ripples.filter((r) => t - r.t0 < 640 + 120);
        // output tween
        if (D.outTarget != null) { D.outShown = lerp(D.outShown, D.outTarget, 1 - Math.exp(-dt / 0.045)); if (Math.abs(D.outShown - D.outTarget) < Math.pow(10, -D.outDec) / 2) D.outShown = D.outTarget; outV.textContent = D.outSign + fmt(Math.abs(D.outShown), D.outDec); }
        const n = Math.round(D.gauge.x);
        if (n !== D.lastNum) { D.lastNum = n; gNum.firstChild.textContent = String(n); }
        drawDial(t); drawGauge(t); drawSpark(t);
        const active = !D.angle.rest || !D.key.rest || !D.slider.rest || !D.gauge.rest || D.ripples.length || D.trail.length > 1 && t - D.trail[D.trail.length - 1].t < 700 || t - D.lastEvt < 1500 || t - D.peak.t < 1500 || (D.outTarget != null && D.outShown !== D.outTarget) || D.hist.some((p) => t - p.t < 5200);
        if (!active) { el.classList.toggle("idle", t - D.lastEvt > 4000); }
        return !!active;
    }
    function wake(draw) { if (!D.visible) return; if (draw) { drawDial(performance.now()); drawGauge(performance.now()); drawSpark(performance.now()); } loop.add(frame); }

    /** The real turn rate in ticks per second, from the time between consecutive turn events (the engine batches 50 ms, so a gap is never read as shorter than that): one isolated click reads 1, a slow turn its true pace, a fast turn its true speed. Lightly smoothed (half old, half new) inside a run so it does not flicker; a gap over 400 ms starts fresh. The engine's own ticksPerSec (a 250 ms window average, 4 for one click) is no longer shown. Display only: speed and acceleration are untouched. */
    function turnRate(ev, ticks) {
        if (!ticks) return D.rate || 0; // a momentum-tail step carries no raw ticks: leave the rate as it is
        const at = Number.isFinite(ev.t) ? ev.t : Date.now(), n = Math.abs(ticks), prev = D.rateAt;
        D.rateAt = at;
        const gap = prev ? Math.max(0, at - prev) : 1000, inst = n / clamp(gap / 1000, 0.05, 1);
        D.rate = gap < 400 && D.rate ? D.rate * 0.5 + inst * 0.5 : inst;
        return D.rate;
    }

    /* ---- events ---- */
    function setHandled(ev, appName, appFor) {
        handled.replaceChildren();
        if (ev.out && /editor focused/i.test(String(ev.out.text || ""))) handled.append(h("span", { class: "chip st-none" }, icon("square-dashed", 16, "c-ic"), h("span", {}, "Not fired")));
        else if (!ev.presetId) handled.append(h("span", { class: "chip st-none" }, icon("square-dashed", 16, "c-ic"), h("span", {}, "Unhandled")));
        else if (ev.presetId === "_global") handled.append(h("span", { class: "chip st-inh" }, icon("link", 16, "c-ic"), h("span", {}, "Global")));
        else { const a = appFor(ev.presetId); handled.append(h("span", { class: "chip app" }, a ? appTile(a, { size: 16, url: a.iconUrl }) : null, h("span", {}, appName(ev.presetId)))); }
    }
    return {
        el,
        visible(v) { D.visible = !!v; if (v) { D.colors = null; wake(true); } },
        push(ev, ctx) {
            const kind = ctx.kind || "knob";
            D.hasEvent = true; D.lastEvt = performance.now(); el.classList.remove("idle");
            if (D.name !== ev.control) { D.name = ev.control; D.kind = kind; D.trail = []; D.angle.snapTo(D.angle.x); D.slider.snapTo(0.5); }
            D.kind = kind;
            dialName.firstChild.textContent = ctx.label || ev.control;
            dialName.lastChild.textContent = (ctx.kindLabel || "");
            actName.textContent = ctx.action || "No assignment";
            setHandled(ev, ctx.appName, ctx.appFor);
            rFine.textContent = ev.fine ? "on" : "off"; rFine.classList.toggle("off", !ev.fine);
            if (ev.kind === "turn") {
                const ticks = ev.rawTicks || 0;
                if (kind === "slider") D.slider.t = clamp(D.slider.t + ticks / 127, 0, 1); else D.angle.t += ticks * DEG_PER_TICK * DEG;
                const tps = turnRate(ev, ticks);
                D.gauge.t = Math.min(80, tps); D.gaugeVal = tps;
                if (D.gauge.t > D.peak.v || performance.now() - D.peak.t > 1300) { D.peak.v = Math.max(D.gauge.t, D.peak.v * 0.0 + D.gauge.t); D.peak.t = performance.now(); }
                D.hist.push({ t: performance.now(), v: tps }); if (D.hist.length > 200) D.hist.shift();
                rRaw.textContent = (ticks > 0 ? "+" : "") + ticks; rAcc.textContent = "×" + fmt(ev.accel ?? 1, 2);
            } else {
                rRaw.textContent = "—"; rAcc.textContent = "—";
                if (ev.kind === "press") { D.key.t = 1; D.ripples.push({ t0: performance.now() }, { t0: performance.now() + 120 }); }
                else { D.key.t = 0; D.ripples.push({ t0: performance.now() }); }
            }
            // latency: only when this event carries it (executor time; never invented, never for the Resolve path)
            if (typeof ev.latencyMs === "number" && Number.isFinite(ev.latencyMs)) { rLat.textContent = ev.latencyMs < 1 ? "<1 ms" : `${fmt(ev.latencyMs, ev.latencyMs < 10 ? 1 : 0)} ms`; } else rLat.textContent = "\u2014";
            // the derivation: shown only when the numbers really reconcile, never a figure that does not add up
            why.textContent = ""; why.removeAttribute("data-tip"); rAcc.removeAttribute("data-tip");
            if (ev.kind === "turn" && ev.out && ev.out.value != null && ctx.per && ev.rawTicks) {
                const raw = ev.rawTicks, base = Math.abs(raw) * Math.abs(ctx.per.val), got = Math.abs(Number(ev.out.value)), acc = ev.accel || 1, tol = Math.max(0.011, got * 0.02);
                const u = ev.out.unit || ctx.per.unit || "units", sign = (raw < 0 ? "-" : "") ;
                if (Math.abs(base - got) <= tol) { if (acc > 1.01) rAcc.setAttribute("data-tip", "Measured, but this action does not use the speed boost"); why.textContent = `${raw} \u00d7 ${fmt(Math.abs(ctx.per.val), 3)} = ${sign}${fmt(got, 3)} ${unitFor(got, u)}`; why.setAttribute("data-tip", `${raw} turns \u00d7 ${fmt(Math.abs(ctx.per.val), 3)} ${u} per notch${acc > 1.01 ? ". The speed boost is measured but this action does not use it." : ""}`); }
                else if (Math.abs(base * acc - got) <= tol) { why.textContent = `${raw} \u00d7 ${fmt(Math.abs(ctx.per.val), 3)} \u00d7 ${fmt(acc, 2)} boost = ${sign}${fmt(got, 3)} ${u}`; why.setAttribute("data-tip", `${raw} turns \u00d7 ${fmt(Math.abs(ctx.per.val), 3)} ${u} per notch \u00d7 ${fmt(acc, 2)} speed boost`); }
            }
            // output block. Truthful: nothing handled it -> "No assignment"; editor focused -> a calm note; a unit only when it means something
            const o = ev.out;
            outCaps.hidden = true; outV.hidden = false; outNote.hidden = true; outV.classList.remove("dk-nov");
            const focusedEditor = !!(o && /editor focused/i.test(String(o.text || "")));
            if (focusedEditor) { outV.hidden = true; outU.textContent = ""; outNote.hidden = false; D.outTarget = null; }
            else if (!ev.presetId || !ev.action) { outV.textContent = "No assignment"; outV.classList.add("dk-nov"); outU.textContent = ""; D.outTarget = null; }
            else if (!o) { outV.textContent = "\u2014"; outU.textContent = ""; D.outTarget = null; }
            else if (o.value != null && ev.kind === "turn") {
                const val = Math.abs(Number(o.value)), neg = /^[-\u2212]/.test(o.text || "") || Number(o.value) < 0, pos = /^\+/.test(o.text || "");
                D.outDec = Number.isInteger(val) ? 0 : val >= 10 ? 0 : val >= 1 ? 1 : val >= 0.1 ? 2 : 3;
                D.outSign = val === 0 ? "" : neg ? "-" : pos ? "+" : ""; D.outTarget = val; if (D.outShown === 0 || !Number.isFinite(D.outShown)) D.outShown = val * 0.6;
                const generic = !o.unit || /^units?$/i.test(o.unit);
                outU.textContent = val === 0 || (generic && !o.unit) ? "" : unitFor(val, o.unit || "");
            } else if (o.text) {
                D.outTarget = null;
                if (/^[A-Za-z0-9]+(\+[A-Za-z0-9=\-\[\]]+)*$/.test(o.text) && /\+|^[A-Z]/.test(o.text)) { outV.hidden = true; outCaps.hidden = false; outCaps.replaceChildren(keycaps(o.text)); outU.textContent = ""; }
                else { outV.textContent = o.text; outU.textContent = o.unit && o.unit !== "key" ? o.unit : ""; }
            }
            wake();
        },
        reset() { D.hasEvent = false; el.classList.add("idle"); },
    };
}
