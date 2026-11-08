// stage.js: the floating deck, edge-light, leader-line callouts, live pulses, drag-target preview.
// The ONLY things drawn over the photo are fill:none strokes that trace each control's authored shape.
import { h, s, icon, clamp, loop, reduced, anim, EASE_OUT, EASE_IN, willChange, store, bankLabel, springTo, springSet, Spring } from "./util.js";
import { PHOTO, HOT, GROUPS, GROUP_OF, SLIDER_COLOR, LEDS } from "./hotspots.js";

const [VX, VY, VW, VH] = PHOTO.viewBox;
const CALIB = new URLSearchParams(location.search).has("calib");

export function createStage({ zone, controls, info, onFit, onSelect, onHover, onDelete, onEscape, onStep }) {
    const ids = controls.map((c) => c.id).filter((id) => HOT[id]);
    const nodes = new Map();
    /* ---- DOM ---- */
    const deck = h("div", { class: "deck" },
        h("div", { class: "sh sh-far" }), h("div", { class: "sh sh-mid" }), h("div", { class: "sh sh-contact" }));
    const svg = s("svg", { class: "deck-svg" + (CALIB ? " calib" : ""), viewBox: `${VX} ${VY} ${VW} ${VH}`, role: "group", "aria-label": "Loupedeck control map" });
    const photo = s("image", { href: PHOTO.src2x, x: 0, y: 0, width: PHOTO.w, height: PHOTO.h, class: "deck-photo", "pointer-events": "none" });
    photo.addEventListener("error", () => { if (photo.getAttribute("href") !== PHOTO.src) photo.setAttribute("href", PHOTO.src); }); // the 2x bitmap failed: the 1x file is the fallback
    const gEdges = s("g", { class: "edges" }), gHits = s("g", { class: "hits", tabindex: "-1" }); // Chromium makes an SVG group with focusin listeners a tab stop: -1 keeps the deck ONE stop
    const glideEl = s("rect", { class: "edge-glide" }); // the selection ring while it travels from one control to the next (a stroke, never a fill)
    gEdges.append(glideEl);
    /* the four state LEDs: the photo shows them dark; a lit one is drawn over it as the real part looks (a white core, a soft halo). Opacity only, never a fill that spills over a key. */
    const gLeds = s("g", { class: "leds", "pointer-events": "none" }), ledEl = {};
    { const defs = s("defs"), rg = s("radialGradient", { id: "led-glow" });
      for (const [o, a] of [[0, 0.85], [0.22, 0.45], [0.55, 0.12], [1, 0]]) rg.append(s("stop", { offset: String(o), "stop-color": "#fff3e0", "stop-opacity": String(a) }));
      defs.append(rg); gLeds.append(defs);
      for (const [k, [x, y]] of Object.entries(LEDS)) { const g = s("g", { class: "led", "data-led": k }); g.append(s("circle", { cx: x, cy: y, r: 17, fill: "url(#led-glow)" }), s("circle", { cx: x, cy: y, r: 3.3, fill: "#fff" })); ledEl[k] = g; gLeds.append(g); } }
    function setLeds(on) { for (const k of Object.keys(ledEl)) ledEl[k].classList.toggle("on", !!(on && on[k])); }
    svg.append(photo, gLeds, gEdges, gHits);
    deck.append(svg);
    const coWrap = h("div", { class: "callouts", "aria-hidden": "true" });
    const coSvg = s("svg", { class: "co-svg" });
    coWrap.append(coSvg);
    /* the "N more" button is a real control: it lives beside the (aria-hidden) chip layer, never inside it */
    const coCtl = h("div", { class: "callouts co-ctl" });
    zone.append(deck, coWrap, coCtl);
    const coAll = () => [...coWrap.querySelectorAll(".co-chip"), ...coCtl.querySelectorAll(".co-more")];

    const shapeEl = (hs, cls, grow = 0) => {
        const g = grow;
        if (hs.shape === "circle") return s("circle", { class: cls, cx: hs.cx, cy: hs.cy, r: hs.w / 2 + g });
        return s("rect", { class: cls, x: hs.cx - hs.w / 2 - g, y: hs.cy - hs.h / 2 - g, width: hs.w + 2 * g, height: hs.h + 2 * g, rx: hs.shape === "stadium" ? hs.w / 2 + g : hs.rx + g });
    };
    for (const id of ids) {
        const hs = HOT[id];
        const g = s("g", { class: "ctl", "data-id": id, "data-kind": hs.kind });
        { const sc0 = SLIDER_COLOR[id]; if (sc0) { // a slider (and the button above it) lights in its own dot colour: edge, soft halo, pulse
            g.classList.add("sc"); const n = parseInt(sc0.slice(1), 16), rgb = [(n >> 16) & 255, (n >> 8) & 255, n & 255];
            g.style.setProperty("--ac", sc0); g.style.setProperty("--ac-hi", "rgb(" + rgb.map((v) => Math.round(v + (255 - v) * 0.35)).join(",") + ")"); g.style.setProperty("--ac-soft", "rgba(" + rgb.join(",") + ", .30)"); } }
        const soft = shapeEl(hs, "soft"), edge = shapeEl(hs, "edge");
        const selSoft = shapeEl(hs, "edge-sel-soft"), sel = shapeEl(hs, "edge-sel", 0);
        sel.setAttribute("pathLength", "1");
        const live = shapeEl(hs, "edge-live"), comet = shapeEl(hs, "comet"), fe = shapeEl(hs, "edge-focus", 5);
        comet.setAttribute("pathLength", "1");
        if (hs.shape === "circle") { sel.setAttribute("transform", `rotate(-90 ${hs.cx} ${hs.cy})`); comet.setAttribute("transform", `rotate(-90 ${hs.cx} ${hs.cy})`); }
        g.append(soft, edge, selSoft, sel, live, comet, fe);
        gEdges.append(g);
        const hit = shapeEl(hs, "hit");
        hit.setAttribute("tabindex", id === ids[0] ? "0" : "-1"); hit.setAttribute("role", "button"); hit.setAttribute("data-id", id);
        hit.setAttribute("aria-label", info(id).name);
        gHits.append(hit);
        nodes.set(id, { id, hs, g, edge, sel, live, comet, fe, hit, m: { op: 0, speed: 0, phase: 0, dir: 1, last: 0, tps: 0, fine: false } });
    }

    /* ---- geometry ---- */
    /* callout geometry: CO_GAP = minimum gap between two parallel leader tracks (px); CO_LEAD = gap between the deck edge and the first row of boxes (room for the bus lanes); CO_ROWGAP = gap between the two tiers; CO_MINK = the smallest deck scale, as a share of its normal size */
    const CO_GAP = 8, CO_LEAD = 52, CO_ROWGAP = 16, CO_MINK = 0.58, CO_CLEAR = 6; // CO_CLEAR: a leader keeps this far from every control but its own
    const G = { zw: 0, zh: 0, dw: 0, dh: 0, s: 1, k: 1, dx: 0, dy: 0, bottom: 0, callouts: false, placed: false };
    const stageEl = zone.parentElement;
    let verifyN = 0, noGlide = 0, morphRaf = 0;
    /** the deck's current transform (what is on screen right now) */
    function deckState() { const p = deck._sp && deck._sp.p; return { tx: p && p.tx ? p.tx.x : G.dx, ty: p && p.ty ? p.ty.x : G.dy, s: p && p.s ? p.s.x : G.k, dw: G.dw, dh: G.dh }; }
    /** run fn with the deck jumping straight to its final place (callouts measured and laid out for the final size before anything is shown) */
    function withoutGlide(fn) { noGlide++; try { return fn(); } finally { noGlide--; } }
    /** one ease-out from `from` to the deck's final place, no overshoot, no second step */
    function morph(from, ms, done) {
        cancelAnimationFrame(morphRaf);
        const to = { tx: G.dx, ty: G.dy, s: G.k };
        /* the element's own box changes size when the stage width does; the morph runs on the visual size and centre (as if from the old box), then lands on the real box, so the deck never jumps in size */
        const fdw = from.dw || G.dw, fdh = from.dh || G.dh, c0 = { x: from.tx + fdw / 2, y: from.ty + fdh / 2, w: from.s * fdw }, c1 = { x: to.tx + G.dw / 2, y: to.ty + G.dh / 2, w: to.s * G.dw };
        const put = (e) => { const w = c0.w + (c1.w - c0.w) * e, x = c0.x + (c1.x - c0.x) * e, y = c0.y + (c1.y - c0.y) * e; springSet(deck, { tx: x - G.dw / 2, ty: y - G.dh / 2, s: w / G.dw }); };
        if (reduced() || ms <= 0) { springSet(deck, to); if (done) done(); return; }
        put(0); const t0 = performance.now();
        const step = (now) => {
            const u = Math.max(0, Math.min(1, (now - t0) / ms)), e = 1 - Math.pow(1 - u, 3); // the first rAF timestamp can precede t0: never a negative step (that was a one-frame overshoot)
            put(e);
            if (u < 1) morphRaf = requestAnimationFrame(step); else { springSet(deck, to); if (done) done(); }
        };
        morphRaf = requestAnimationFrame(step);
    }
    /** would the callout bands (the last measured ones, else typical) leave the deck at least the minimum readable scale in the stage as it is now? */
    function fitsCallouts(extra = 0) { const bT = G.coT > 0 ? G.coT + 20 : G.coT === 0 ? 24 : 124 + 20, bB = G.coB ?? 124; return !!G.botEdge && G.botEdge - bT - bB >= G.dh * 0.55 + extra; }
    /** opts.bottom / opts.callouts = a state change (the deck glides to its new place on the deck spring); no opts = resize (snaps). */
    function layout(opts = {}) {
        if (gh && gh.built && !opts.verify) ghostKill(0); // the deck is about to move: a drawn ghost layer would be stale (an intro still waiting measures when it starts)
        const glide = !noGlide && (!!opts.glide || (opts.bottom != null && opts.bottom !== G.bottom) || (opts.callouts != null && opts.callouts !== G.callouts));
        if (opts.bottom != null) G.bottom = opts.bottom;
        if (opts.callouts != null) G.callouts = opts.callouts;
        G.zw = zone.clientWidth; G.zh = zone.clientHeight;
        { const vv = window.visualViewport; if (vv) { const vis = Math.floor(vv.height + vv.offsetTop - zone.getBoundingClientRect().top); if (vis > 0) G.zh = Math.min(G.zh, vis); } } // never plan for height the window does not show (info bars, banners)
        if (!G.zw || !G.zh) return;
        if (!opts.verify) { G.adj = 0; G.kMax = null; verifyN = 0; }
        const padX = Math.max(G.zw < 760 ? 16 : 24, Math.min(40, G.zw * 0.025)); // narrow stage: the deck takes every pixel it can (its printed labels are baked into the photo)
        G.s = 0.8 * Math.min((G.zw - 2 * padX) / VW, (G.zh - 48) / VH, 1.6); // 20% smaller than the full fit at every size (Tunahan 2026-11-05: "this is too big, we can reduce size %20 for all")
        G.dw = VW * G.s; G.dh = VH * G.s;
        // optically centred in the free area (a hair above the middle); the live drawer / callout rows only scale it down when they would be covered
        // the resting place first (callouts off): the live drawer's room, a hair above optical centre. The photo never moves when the live drawer opens.
        // closed: the deck is centred in the free height under the toolbar; open: it keeps clear of the live drawer's room (and glides there, on the deck spring)
        const live = G.bottom > 0 ? (parseInt(getComputedStyle(document.documentElement).getPropertyValue("--live-h"), 10) || 200) + 16 : 0;
        const bar = document.getElementById("stageBar"), barB = bar ? Math.max(0, bar.getBoundingClientRect().bottom - zone.getBoundingClientRect().top) + 8 : 0;
        // with the live drawer open the photo keeps a 56 px gutter above it (it scales down, to 0.5 at most, rather than crowd the strip)
        const gapB = G.bottom > 0 ? 56 : 12, gutT0 = 20;
        const room0 = G.zh - live - gutT0 - gapB;
        let k = Math.min(1, Math.max(0.5, room0 / G.dh)), hh = G.dh * k;
        let cy = G.bottom > 0 ? Math.min(G.zh * 0.47, G.zh - live - gapB - hh / 2) : barB + (G.zh - barB) / 2;
        if (cy - hh / 2 < gutT0) cy = gutT0 + hh / 2;
        /* The deck and the callout bands it really has (none, bottom only, both) are ONE block: the deck is scaled to what that block needs, then the block is centred in the stage, so spare height splits evenly above and below and never piles up under the deck. */
        const botEdge = G.zh - (G.bottom > 0 ? live + gapB : 16);
        G.botEdge = botEdge;
        /* the resting centre (Callouts off): Callouts on keeps this exact centre and only scales around it, so the toggle never slides the deck */
        const offCy = (() => { const TOP = 24, m0 = 10 * G.s, kk = Math.min(1, Math.max(CO_MINK, (botEdge - TOP + 2 * m0) / G.dh)), h0 = G.dh * kk, slack = Math.max(0, botEdge - TOP - (h0 - 2 * m0 * kk)); return slack / 2 + TOP - m0 * kk + h0 / 2; })();
        if (G.callouts) {
            const cT = G.coT ?? 124, cB = G.coB ?? 124, bT = cT > 0 ? cT + 20 : 24, bB = cB; // each band = its rows + the 28 px lead to the deck; the top one sits 20 px under the title row
            const MINK = CO_MINK; // the deck never gets smaller than this share of its normal size: when the space runs out, the callouts give way (fewer tiers, then none), not the deck
            G.fits = botEdge - bT - bB >= G.dh * 0.55;
            k = Math.min(1, Math.max(MINK, (botEdge - bT - bB) / G.dh)); if (G.kMax) k = Math.min(k, G.kMax); hh = G.dh * k;
            const slack = Math.max(0, botEdge - bT - bB - hh);
            cy = slack / 2 + bT + hh / 2 + (G.adj || 0);
            { const room = 2 * Math.min(offCy - bT, botEdge - bB - offCy), kS = room / G.dh; // the block (bands + deck) around the resting centre
              if (kS >= Math.min(k, 1) * 0.75 && kS >= MINK) { k = Math.min(k, kS); hh = G.dh * k; cy = offCy; G.adj = 0; }
              else { const lo = bT + hh / 2, hi = botEdge - bB - hh / 2; if (lo <= hi) cy = Math.min(hi, Math.max(lo, offCy)); else cy = Math.max(6 + hh / 2, Math.min(offCy, botEdge - hh / 2)); } }
            { const rowsFor = (a) => (a - CO_LEAD < 28 ? 0 : a - CO_LEAD < 28 * 2 + CO_ROWGAP ? 1 : 2); G.coRows = { top: rowsFor(cy - hh / 2 - 6), bottom: rowsFor(botEdge - (cy + hh / 2)) }; } // what the space around the deck really holds
        } else {
            G.coRows = null;
            const TOP = 24, m0 = 10 * G.s; // the case edge sits inside the photo's margin
            k = Math.min(1, Math.max(CO_MINK, (botEdge - TOP + 2 * m0) / G.dh)); hh = G.dh * k;
            const slack = Math.max(0, botEdge - TOP - (hh - 2 * m0 * k));
            cy = slack / 2 + TOP - m0 * k + hh / 2 + (G.adj || 0);
        }
        G.k = k;
        G.dx = Math.round((G.zw - G.dw) / 2); G.dy = Math.round(cy - G.dh / 2); // whole pixels at rest: no sub-pixel resampling of the photo
        deck.style.width = G.dw + "px"; deck.style.height = G.dh + "px";
        if (!G.placed) { springSet(deck, { tx: G.dx, ty: G.dy, s: G.k }); G.placed = true; }
        else if (glide) { deck.style.willChange = "transform"; springTo(deck, { tx: G.dx, ty: G.dy, s: G.k }, "deck", () => { deck.style.willChange = ""; }); }
        else { springSet(deck, { tx: G.dx, ty: G.dy, s: G.k }); }
        svg.style.setProperty("--inv", String(1 / (G.s * G.k)));
        // the soft shadows are sized to what the zone can hold, so the zone has no scrollable overflow (scrollWidth == clientWidth, nothing to scroll to)
        deck.style.setProperty("--sh-w", Math.max(0, Math.floor(G.zw / G.k) - 2) + "px");
        deck.style.setProperty("--sh-below", Math.max(0, Math.floor((G.zh - (G.dy + G.dh / 2 + (G.dh * G.k) / 2)) / G.k) - 1) + "px");
        // the toolbar aligns to the deck's grid
        const left = G.dx + (G.dw - G.dw * G.k) / 2 + 10 * G.s * G.k; // the case edge, not the photo margin or its shadow
        stageEl.style.setProperty("--deck-x", Math.round(Math.min(48, Math.max(16, left))) + "px");
        zone.style.setProperty("--deck-bottom", Math.round(G.dy + (G.dh + G.dh * G.k) / 2) + "px");
        stageEl.style.setProperty("--deck-top", Math.round(G.dy + (G.dh - G.dh * G.k) / 2) + "px");
        layoutHits();
        if (onFit && !opts.verify) { const ok = fitsCallouts(); if (ok !== G.lastFit) { G.lastFit = ok; setTimeout(() => onFit(ok), 0); } }
        if (coOn) buildCallouts(true);
    }
    /** photo px -> zone px (after deck scale) */
    const toZone = (px, py) => ({ x: G.dx + G.dw / 2 + ((px - VX) * G.s - G.dw / 2) * G.k, y: G.dy + G.dh / 2 + ((py - VY) * G.s - G.dh / 2) * G.k });
    const sc = () => G.s * G.k; // photo px -> zone px
    function bboxZone(id) {
        const hs = HOT[id], a = toZone(hs.cx - hs.w / 2, hs.cy - hs.h / 2), b = toZone(hs.cx + hs.w / 2, hs.cy + hs.h / 2);
        return { x: a.x, y: a.y, w: b.x - a.x, h: b.y - a.y };
    }
    /* hit areas: at least 28 screen px, never overlapping a neighbour */
    function layoutHits() {
        const px = 28 / sc();
        const boxes = ids.map((id) => { const hs = HOT[id]; const w = Math.max(hs.w, px), hh = Math.max(hs.h, px); return { id, cx: hs.cx, cy: hs.cy, w, h: hh, hs }; });
        for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
            const a = boxes[i], b = boxes[j];
            const ox = (a.w + b.w) / 2 - Math.abs(a.cx - b.cx), oy = (a.h + b.h) / 2 - Math.abs(a.cy - b.cy);
            if (ox > 0 && oy > 0) {
                if (ox < oy) { const d = ox / 2 + 0.5; a.w = Math.max(a.hs.w, a.w - d); b.w = Math.max(b.hs.w, b.w - d); }
                else { const d = oy / 2 + 0.5; a.h = Math.max(a.hs.h, a.h - d); b.h = Math.max(b.hs.h, b.h - d); }
            }
        }
        for (const bx of boxes) {
            const hit = nodes.get(bx.id).hit, hs = bx.hs;
            if (hs.shape === "circle") { hit.setAttribute("r", Math.min(bx.w, bx.h) / 2); }
            else { hit.setAttribute("x", bx.cx - bx.w / 2); hit.setAttribute("y", bx.cy - bx.h / 2); hit.setAttribute("width", bx.w); hit.setAttribute("height", bx.h); hit.setAttribute("rx", hs.shape === "stadium" ? Math.min(bx.w, bx.h) / 2 : hs.rx); }
        }
    }
    const ro = new ResizeObserver(() => { if (zone.clientWidth === G.zw && zone.clientHeight === G.zh && G.placed) return; layout(); }); // a size already laid out (the callout / inspector morph did it) is not laid out again
    ro.observe(zone);
    if (window.visualViewport) window.visualViewport.addEventListener("resize", () => layout());
    if (document.fonts && document.fonts.ready) document.fonts.ready.then(() => layout()); // chips are measured in the final font
    photo.addEventListener("load", () => layout());
    window.addEventListener("resize", () => layout());
    /* watchdog: some windows change height without telling anyone (a browser info bar arriving after load); the stage re-reads what it is really given twice a second and lays out again the moment it differs */
    const seen = { w: 0, h: 0, vh: 0 };
    setInterval(() => { if (document.hidden) return; const vv = window.visualViewport, w = zone.clientWidth, h = zone.clientHeight, vh = vv ? Math.round(vv.height + vv.offsetTop - zone.getBoundingClientRect().top) : 0; if (w !== seen.w || h !== seen.h || vh !== seen.vh) { seen.w = w; seen.h = h; seen.vh = vh; if (w !== G.zw || Math.min(h, vh > 0 ? vh : h) !== G.zh) layout(); } }, 500);
    /** hard rule: no two leaders of different boxes lie on the same track. Reports every pair of segments (from different routes) that run parallel closer than CO_GAP (8 px) with overlapping extent, plus the right-angle crossings and any route running through another control. Dev check, read-only. */
    window.__coCheck = () => {
        const R = [...coSvg.querySelectorAll(".co-route")].map((p) => ({ id: p.dataset.id, key: p.dataset.key, pts: (p.getAttribute("d").match(/[ML][\d.\-]+ [\d.\-]+/g) || []).map((q) => q.slice(1).split(" ").map(Number)) }));
        const S = R.map((r) => ({ id: r.id, key: r.key, s: r.pts.slice(1).map((q, i) => [r.pts[i][0], r.pts[i][1], q[0], q[1]]) }));
        const overlaps = [], thru = []; let crossings = 0;
        for (let i = 0; i < S.length; i++) for (let j = i + 1; j < S.length; j++) {
            if (S[i].key === S[j].key) continue; // members of one grouped box merge on purpose
            for (const a of S[i].s) for (const b of S[j].s) {
                const av = a[0] === a[2], bv = b[0] === b[2];
                if (av === bv) {
                    const [p, q] = av ? [0, 1] : [1, 0];
                    if (Math.abs(a[p] - b[p]) < CO_GAP && Math.min(Math.max(a[q], a[q + 2]), Math.max(b[q], b[q + 2])) - Math.max(Math.min(a[q], a[q + 2]), Math.min(b[q], b[q + 2])) > 1) overlaps.push(S[i].id + "~" + S[j].id);
                } else {
                    const v = av ? a : b, hz = av ? b : a;
                    if (v[0] > Math.min(hz[0], hz[2]) + .5 && v[0] < Math.max(hz[0], hz[2]) - .5 && hz[1] > Math.min(v[1], v[3]) + .5 && hz[1] < Math.max(v[1], v[3]) - .5) crossings++;
                }
            }
        }
        const zr = zone.getBoundingClientRect();
        const clearV = [], labelV = [];
        for (const r of S) { const mine = new Set(S.filter((x) => x.key === r.key).map((x) => x.id));
            for (const sg of r.s) {
                for (const id of ids) { if (mine.has(id)) continue; const gp = segGap(sg, outlineZone(id)); if (gp < CO_CLEAR - 0.01) { clearV.push(r.id + ">" + id + ":" + gp.toFixed(1)); if (gp <= 0) thru.push(r.id + ">" + id); } }
                for (const id of ids) if (isLabelled(id) && segGap(sg, labelZone(id)) < 1.5) labelV.push(r.id + ">label of " + id);
            } }
        /* leader length: the polyline's length against the shortest possible orthogonal route (sideways to the box's span, then straight to its edge). "long" = more than 1.5x that. */
        const sr = coSvg.getBoundingClientRect(), chipsBy = new Map([...coWrap.querySelectorAll(".co-chip")].map((c) => [c.dataset.key, c.getBoundingClientRect()]));
        const lens = R.map((r) => { let len = 0; for (let i = 1; i < r.pts.length; i++) len += Math.abs(r.pts[i][0] - r.pts[i - 1][0]) + Math.abs(r.pts[i][1] - r.pts[i - 1][1]); const c = chipsBy.get(r.key), p0 = r.pts[0], pe = r.pts[r.pts.length - 1];
            let ideal = len; if (c) { const L = c.left - sr.left + 10, Rr = c.right - sr.left - 10; ideal = Math.max(0, L - p0[0], p0[0] - Rr) + Math.abs(pe[1] - p0[1]); } return { id: r.id, len: Math.round(len), ideal: Math.round(ideal) }; });
        const longest = lens.slice().sort((a, b) => b.len - a.len).slice(0, 5), long = lens.filter((l) => l.len > 1.5 * l.ideal + 16).map((l) => l.id + ":" + l.len + "/" + l.ideal);
        /* origins: where a leader leaves its control. Centred on the edge it leaves from (keys: mid-edge; sliders: the top end of the track; knobs and round buttons: the ring). A knob leaving downwards starts at its lower right on purpose (clear of its printed name). Reported when more than 2 px off-axis or off the edge. */
        const origins = [];
        for (const r of R) { const o = outlineZone(r.id), p0 = r.pts[0], cx = o.x + o.w / 2, cy = o.y + o.h / 2, lb = isLabelled(r.id) ? labelZone(r.id) : null;
            const cands = [[cx, o.y], [cx, lb ? lb.y + lb.h + 4 : o.y + o.h], [o.x, cy], [o.x + o.w, cy]]; // the middle of each edge; a knob going down: under its printed name
            const best = Math.min(...cands.map((q) => Math.max(Math.abs(p0[0] - q[0]), Math.abs(p0[1] - q[1]))));
            if (best > 2.6) origins.push(r.id + ": origin " + best.toFixed(1) + " px from the middle of an edge"); }
        return { routes: S.length, overlaps: [...new Set(overlaps)], through: [...new Set(thru)], clearance: [...new Set(clearV)], labels: [...new Set(labelV)], crossings, longest, long, origins };
    };
    window.__ldLayout = () => ({ ...G, zoneH: zone.clientHeight, zoneW: zone.clientWidth, innerH: innerHeight, vvH: window.visualViewport && window.visualViewport.height, dpr: devicePixelRatio, deckRect: deck.getBoundingClientRect().toJSON(), zoneRect: zone.getBoundingClientRect().toJSON() });
    zone.addEventListener("scroll", () => { if (zone.scrollLeft || zone.scrollTop) { zone.scrollLeft = 0; zone.scrollTop = 0; } }); // belt and braces for engines without overflow: clip

    /* ---- selection, hover, focus ---- */
    let selected = null, hovered = null, hoveredReal = null, allEdges = false, enteredOnce = false;
    /* selection hand-off: the orange ring glides from the old control's outline to the new one on the snap spring (~220 ms), then the real edge takes over.
       Reduced motion: no travel, the new edge fades in (opacity only). */
    const rectOf = (id) => { const hs = HOT[id]; return { x: hs.cx - hs.w / 2, y: hs.cy - hs.h / 2, w: hs.w, h: hs.h, r: hs.shape === "circle" ? hs.w / 2 : hs.shape === "stadium" ? hs.w / 2 : hs.rx }; };
    const gl = { cur: null, task: null, to: null };
    const paintGlide = (c) => { glideEl.setAttribute("x", c.x); glideEl.setAttribute("y", c.y); glideEl.setAttribute("width", c.w); glideEl.setAttribute("height", c.h); glideEl.setAttribute("rx", c.r); };
    function endGlide(commit = true) {
        if (gl.task) { gl.task.dead = true; gl.task = null; }
        glideEl.style.opacity = "0"; gl.cur = null;
        if (gl.to) { const n = nodes.get(gl.to); if (n) { if (commit) n.g.classList.add("sel-on"); n.g.classList.remove("gliding"); } gl.to = null; }
    }
    function handOff(fromRect, toId) {
        if (gl.to && gl.to !== toId) { const o = nodes.get(gl.to); if (o) o.g.classList.remove("gliding"); }
        gl.to = toId; const n = nodes.get(toId); n.g.classList.add("gliding");
        if (gl.task) gl.task.dead = true;
        const a = gl.cur || fromRect, b = rectOf(toId), sp = new Spring("snap", 0); sp.t = 1;
        gl.cur = a; paintGlide(a); glideEl.style.opacity = "1";
        const task = { dead: false };
        gl.task = task;
        loop.add((dt) => {
            if (task.dead) return false;
            sp.step(dt);
            const t = sp.x, L = (k) => a[k] + (b[k] - a[k]) * t;
            const c = { x: L("x"), y: L("y"), w: Math.max(1, L("w")), h: Math.max(1, L("h")), r: Math.max(0, L("r")) };
            gl.cur = c; paintGlide(c);
            if (sp.rest) { gl.cur = b; endGlide(); return false; }
            return true;
        });
    }
    function setSelected(id, instant) {
        const prev = selected;
        if (selected && nodes.has(selected) && selected !== id) {
            const o = nodes.get(selected); o.g.classList.remove("sel-on");
            if (!instant) anim(o.sel, [{ opacity: 1 }, { opacity: 0 }], { duration: 100, easing: EASE_IN });
        }
        selected = id && nodes.has(id) ? id : null;
        stageEl.classList.toggle("has-sel", !!selected);
        if (selected) { const c = toZone(HOT[selected].cx, HOT[selected].cy); stageEl.style.setProperty("--lx", Math.round(c.x) + "px"); stageEl.style.setProperty("--ly", Math.round(c.y + 44) + "px"); }
        if (!selected) { endGlide(false); lastSelGroup = null; if (coOn) updateCalloutHighlight(); return; }
        const n = nodes.get(selected);
        n.sel.getAnimations().forEach((a) => a.cancel());
        const travel = false; // Tunahan 2026-11-04: the highlight never travels from the previous control; the old edge fades out, the new one draws in place
        if (travel) {
            n.g.classList.remove("sel-on");
            handOff(prev && nodes.has(prev) && prev !== selected ? rectOf(prev) : null, selected);
        } else {
            endGlide(); n.g.classList.add("sel-on");
            if (reduced() || instant) anim(n.sel, [{ opacity: 0, strokeDashoffset: 0 }, { opacity: 1, strokeDashoffset: 0 }], { duration: 120 });
            else anim(n.sel, [{ strokeDashoffset: 1, opacity: 1 }, { strokeDashoffset: 0, opacity: 1 }], { duration: 180, easing: EASE_OUT });
        }
        if (coOn) { if (lastSelGroup !== (GROUP_OF[selected] || null)) { lastSelGroup = GROUP_OF[selected] || null; if (lastSelGroup && coGroup) { coGroup = null; buildCallouts(true); } else updateCalloutHighlight(); } else updateCalloutHighlight(); }
    }
    function setHover(id) {
        if (hovered === id) return;
        hoveredReal = id;
        if (hovered && nodes.has(hovered)) nodes.get(hovered).g.classList.remove("hover");
        hovered = id;
        if (id) coIntroFinish(); // a pointer on a control ends the callouts intro at once
        if (id) ghostShow(id); else ghostLeave();
        if (id && nodes.has(id)) nodes.get(id).g.classList.add("hover");
        if (id && coOn) focusGroup(id);
        if (coOn) updateCalloutHighlight();
    }
    /* delegate pointer events on the hit layer */
    gHits.addEventListener("pointerover", (e) => { const id = e.target.dataset && e.target.dataset.id; if (!id) return; setHover(id); if (onHover) onHover(id); });
    gHits.addEventListener("pointerout", (e) => { const id = e.target.dataset && e.target.dataset.id; if (!id) return; setHover(null); if (onHover) onHover(null); });
    gHits.addEventListener("click", (e) => { const id = e.target.dataset && e.target.dataset.id; if (id) { pressFeel(id); onSelect(id); } });
    gHits.addEventListener("keydown", (e) => {
        const id = e.target.dataset && e.target.dataset.id; if (!id) return;
        if (e.metaKey || e.ctrlKey || e.altKey) return;
        if (e.key === "Enter" || e.key === " ") { e.preventDefault(); pressFeel(id); onSelect(id); }
        else if (e.key.startsWith("Arrow")) arrowNav(e, id);
        else if (e.key === "Home" || e.key === "End") { e.preventDefault(); const row = rowOf(id); const pick = e.key === "Home" ? row[0] : row[row.length - 1]; if (pick) nodes.get(pick).hit.focus(); }
        else if (e.key === "Delete" || e.key === "Backspace") { e.preventDefault(); if (onDelete) onDelete(id); }
        else if (e.key === "Escape") { if (onEscape) onEscape(id); }
    });
    /** the controls on the same physical row as `id`, left to right */
    function rowOf(id) { const me = HOT[id]; return ids.filter((o) => Math.abs(HOT[o].cy - me.cy) <= 48).sort((a, b) => HOT[a].cx - HOT[b].cx); }
    /** roving tabindex: the deck is ONE tab stop; arrows move inside it */
    let rover = ids[0];
    function setRover(id) { if (rover === id || !nodes.has(id)) return; nodes.get(rover).hit.setAttribute("tabindex", "-1"); rover = id; nodes.get(id).hit.setAttribute("tabindex", "0"); }
    gHits.addEventListener("focusin", (e) => { const id = e.target.dataset && e.target.dataset.id; if (!id) return; setRover(id); const kf = e.target.matches(":focus-visible"); if (kf) { nodes.get(id).g.classList.add("kf"); ghostShow(id); if (coOn) { focusGroup(id, true); updateCalloutHighlight(); } } });
    gHits.addEventListener("focusout", (e) => { const id = e.target.dataset && e.target.dataset.id; if (!id) return; nodes.get(id).g.classList.remove("kf"); ghostLeave(); });
    function pressFeel(id) {
        const n = nodes.get(id); if (!n || reduced()) return;
        anim(n.edge, [{ strokeWidth: 1 }, { strokeWidth: 2.5 }, { strokeWidth: 1 }], { duration: 160 });
    }
    function arrowNav(e, from) {
        if (e.shiftKey) return;
        const d = { ArrowRight: [1, 0], ArrowLeft: [-1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }[e.key]; if (!d) return;
        const me = HOT[from]; let best = null, bs = 1e9;
        for (const id of ids) {
            if (id === from) continue;
            const o = HOT[id], dx = o.cx - me.cx, dy = o.cy - me.cy, along = dx * d[0] + dy * d[1];
            if (along <= 1) continue;
            const across = Math.abs(dx * d[1] + dy * d[0]);
            const score = along + across * 2.2;
            if (score < bs) { bs = score; best = id; }
        }
        if (best) { e.preventDefault(); nodes.get(best).hit.focus(); }
    }

    function setAllEdges(on) {
        allEdges = !!on; svg.classList.toggle("all", allEdges);
        if (allEdges && !reduced()) {
            let i = 0; for (const n of nodes.values()) { anim(n.edge, [{ opacity: 0 }, { opacity: 1 }], { duration: 180, delay: Math.min(300, i * 12), easing: EASE_OUT, fill: "backwards" }); i++; }
        }
    }

    /* ---- callouts: only the focused group, identical actions merged, orthogonal bus routing ---- */
    let gh = null, lastCoGhost = null, ghSeq = 0, ghLeaveT = 0; // the ghost callout layer (normal mode): gh = {kind: "intro" | "hover", ...}
    let coOn = false, lastCo = null, coGroup = null, lastSelGroup = null, coHint = { hidden: 0, group: "" };
    const dwellT = { t: 0 };
    function setCallouts(on) { ghostKill(0); coIntroFinish(); coOn = !!on; coWrap.classList.toggle("on", coOn); coCtl.classList.toggle("on", coOn); if (!coOn) { coGroup = null; coAll().forEach((c) => c.remove()); coSvg.replaceChildren(); } }
    function focusGroup() { /* callouts no longer follow the hover: the drawn set is the same whatever was hovered or when Callouts was switched on */ }
    let sideOv = {}, coBest = null; // box key -> side forced by the overlap repair (a box whose leader cannot find a free track moves to the other side)
    /* a box goes to the side of the deck nearest its controls (the shorter way out); the group's old up/down flag no longer decides */
    const sideFor = (e) => { if (G.coRows && G.coRows.top === 0 && G.coRows.bottom > 0) return "bottom"; if (G.coRows && G.coRows.bottom === 0 && G.coRows.top > 0) return "top"; const y = toZone(0, e.cy).y, top = G.dy + G.dh / 2 - (G.dh / 2) * G.k, bot = G.dy + G.dh / 2 + (G.dh / 2) * G.k; return y - top <= bot - y ? "top" : "bottom"; };
    /** a control's real hit area in zone px (what a pointer can land on) */
    function hitZone(id) {
        const n = nodes.get(id), hs = n.hs, hit = n.hit;
        if (hs.shape === "circle") { const r = +hit.getAttribute("r"), a = toZone(hs.cx - r, hs.cy - r), b = toZone(hs.cx + r, hs.cy + r); return { x: a.x, y: a.y, w: b.x - a.x, h: b.y - a.y }; }
        const x = +hit.getAttribute("x"), y = +hit.getAttribute("y"), w = +hit.getAttribute("width"), hh = +hit.getAttribute("height"), a = toZone(x, y), b = toZone(x + w, y + hh);
        return { x: a.x, y: a.y, w: b.x - a.x, h: b.y - a.y };
    }
    /** a control's real outline in zone px (the drawn shape of 1530ecc, not the larger hit area) */
    function outlineZone(id) { const hs = HOT[id], a = toZone(hs.cx - hs.w / 2, hs.cy - hs.h / 2), b = toZone(hs.cx + hs.w / 2, hs.cy + hs.h / 2); return { x: a.x, y: a.y, w: b.x - a.x, h: b.y - a.y }; }
    /** the printed name under a knob or dial */
    function labelZone(id) { const hs = HOT[id], a = toZone(hs.cx - 46, hs.cy + hs.h / 2 - 2), b = toZone(hs.cx + 46, hs.cy + hs.h / 2 + 34); return { x: a.x, y: a.y, w: b.x - a.x, h: b.y - a.y }; }
    const isLabelled = (id) => HOT[id].kind === "knob" || HOT[id].kind === "dial";
    /** distance from an axis-aligned segment to a rect (0 when they touch) */
    function segGap(sg, r) { const x1 = Math.min(sg[0], sg[2]), x2 = Math.max(sg[0], sg[2]), y1 = Math.min(sg[1], sg[3]), y2 = Math.max(sg[1], sg[3]); return Math.hypot(Math.max(r.x - x2, x1 - (r.x + r.w), 0), Math.max(r.y - y2, y1 - (r.y + r.h), 0)); }
    /** does an axis-aligned segment [x1,y1,x2,y2] pass within CO_CLEAR of the rect? */
    function segHits(sg, r) {
        const m = CO_CLEAR, rx = r.x - m, ry = r.y - m, rw = r.w + 2 * m, rh = r.h + 2 * m;
        if (sg[0] === sg[2]) return sg[0] > rx && sg[0] < rx + rw && Math.max(sg[1], sg[3]) > ry && Math.min(sg[1], sg[3]) < ry + rh;
        return sg[1] > ry && sg[1] < ry + rh && Math.max(sg[0], sg[2]) > rx && Math.min(sg[0], sg[2]) < rx + rw;
    }
    /** items: [{id,label,icon,state,risk}] for every assigned control of the bank. */
    let settling = false;
    function buildCallouts(relayout, items) { if (!coOn) return; return buildCore(relayout, items, null); }
    /* g = null: the real callout layer (Callouts on). g = {} : a ghost layer in normal mode (intro / hover): same router and look, but at the deck's NORMAL size, in the space it leaves; it never plans bands, never scales or moves the deck, never drops a box to "N more". */
    function buildCore(relayout, items, g) {
        if (!g) coIntroFinish();
        if (!g && items) lastCo = items;
        const srcItems = g ? g.items : lastCo;
        if (!srcItems) return;
        const moreHadFocus = coCtl.contains(document.activeElement);
        coAll().forEach((c) => c.remove()); coSvg.replaceChildren();
        if (G.coRows && G.coRows.top === 0 && G.coRows.bottom === 0) { coAll().forEach((c) => c.remove()); coSvg.replaceChildren(); coHint = { hidden: 0, group: "", groups: [], solo: false, total: 0 }; return; } // no room around the deck at its smallest size: the callouts give way
        const present = srcItems.filter((i) => i.label && HOT[i.id]);
        // every group with assignments is drawn, in a fixed order (the picked control's group, then the group chosen from the "more" chip, then the deck's own order),
        // so the result never depends on which group was hovered or on when Callouts was switched on. What does not fit is counted and reachable from the "more" chip.
        let gids = Object.keys(GROUPS).filter((g) => present.some((i) => GROUP_OF[i.id] === g));
        const solo = coGroup && gids.includes(coGroup) ? coGroup : null; // chosen from the "more" chip: that group alone, with all the room
        if (solo) gids = [solo]; else coGroup = null;
        // the order is the deck's own, never the selection's: selecting a control must not re-pack the boxes
        coHint = { hidden: 0, group: solo ? GROUPS[solo].label : "", groups: [], solo: !!solo, total: present.length };
        if (!gids.length) { if (!g && !settling && (G.coT || G.coB)) { G.coT = 0; G.coB = 0; settling = true; try { layout({ glide: true }); } finally { settling = false; } } return; }
        const entries = [];
        for (const gid of gids) {
            /* the modifier row is ONE box: "Hold ⇧ Shift · ⌃ Ctrl · ⌘ Cmd · ⌥ Alt   Tab Next timeline tab". Each key keeps its own short leader into it (the allowed merge), in every layer: when a modifier is latched the box lists that layer's assignments for these keys. */
            if (gid === "modkeys") {
                const ORDER = ["Shift", "Ctrl", "Cmd", "Alt", "Tab"], its = present.filter((i) => GROUP_OF[i.id] === gid).sort((x, y) => ORDER.indexOf(x.id) - ORDER.indexOf(y.id));
                if (its.length >= 2) {
                    const holds = its.filter((i) => /^Holds /i.test(i.label)), rest = its.filter((i) => !holds.includes(i)), segs = [];
                    if (holds.length) segs.push({ hold: true, items: holds.map((i) => ({ id: i.id, name: i.label.replace(/^Holds\s+/i, "") })) });
                    for (const i of rest) segs.push({ items: [{ id: i.id, name: info(i.id).name }], text: i.label });
                    const pts = its.map((i) => toZone(HOT[i.id].cx, HOT[i.id].cy));
                    entries.push({ key: "modgroup", label: its.map((i) => i.label).join(" \u00B7 "), icon: its[0].icon, mode: null, state: its.some((i) => i.state === "set") ? "set" : "inh", risk: its.find((i) => i.risk)?.risk || null, members: its.map((i) => i.id), gid, segs, cx: pts.reduce((s, p) => s + p.x, 0) / pts.length, cy: its.reduce((s, i) => s + HOT[i.id].cy, 0) / its.length });
                    continue;
                }
            }
            // merge identical actions into one chip with a count
            const cl = new Map();
            for (const it of present.filter((i) => GROUP_OF[i.id] === gid)) {
                const k = it.icon + "|" + it.label + "|" + (it.mode || ""); const c = cl.get(k) || { key: k, label: it.label, icon: it.icon, mode: it.mode || null, state: "inh", risk: null, members: [], gid };
                c.members.push(it.id); if (it.state === "set") c.state = "set"; if (it.risk) c.risk = it.risk; cl.set(k, c);
            }
            // an identical action merges into one chip only while its controls sit together (same row, close by): far-apart ones keep their own short lines
            const parts = [];
            for (const c of cl.values()) {
                const left = c.members.slice();
                while (left.length) {
                    const comp = [left.shift()];
                    for (let i = 0; i < comp.length; i++) for (let j = left.length - 1; j >= 0; j--) { const a = HOT[comp[i]], b = HOT[left[j]]; if (Math.abs(a.cx - b.cx) <= 320 && Math.abs(a.cy - b.cy) <= 90) comp.push(left.splice(j, 1)[0]); }
                    parts.push({ ...c, key: c.key + "#" + comp[0], members: comp });
                }
            }
            const ge = parts.map((c) => {
                const pts = c.members.map((id) => toZone(HOT[id].cx, HOT[id].cy));
                return Object.assign(c, { cx: pts.reduce((t, p) => t + p.x, 0) / pts.length, cy: c.members.reduce((t, id) => t + HOT[id].cy, 0) / c.members.length });
            });
            ge.sort((a, b) => b.members.length - a.members.length || a.cx - b.cx);
            entries.push(...ge);
        }
        const chips = new Map();
        for (const e of entries) {
            const kname = (n) => ({ Cmd: "\u2318", Shift: "\u21E7 Shift", Ctrl: "\u2303 Ctrl", Alt: "\u2325 Alt" }[n] || n);
            const body = e.segs
                ? e.segs.flatMap((sg, si) => [si ? h("span", { class: "co-sep" }, "\u00B7") : null, sg.hold ? h("span", { class: "co-t" }, "Hold") : null, ...sg.items.map((it) => h("span", { class: "co-k", "data-seg": it.id }, kname(it.name))), sg.text ? h("span", { class: "co-t" }, sg.text) : null].filter(Boolean))
                : [h("span", { class: "co-t" }, e.label), e.mode ? h("span", { class: "co-mode" }, e.mode) : null, e.members.length > 1 ? h("span", { class: "co-n" }, "×" + e.members.length) : null];
            const c = h("div", { class: "co-chip st-" + e.state + (e.risk ? " risk-" + e.risk : "") + (e.segs ? " co-multi" : ""), "data-key": e.key }, icon(e.icon, 16, "co-ic"), ...body.filter(Boolean));
            c.style.visibility = "hidden"; coWrap.append(c); chips.set(e.key, c);
            Object.assign(e, { w: c.offsetWidth, h: c.offsetHeight, side: sideFor(e) });
        }
        /* a side whose boxes are wider than one row drops its little icons (the words say it all): narrower boxes sit under their controls instead of being pushed sideways */
        for (const side of ["top", "bottom"]) {
            const list = entries.filter((x) => x.side === side); if (list.length < 2) continue;
            const total = list.reduce((s, e) => s + e.w + 20, 0);
            if (total <= 1.0 * (G.zw - 24)) continue;
            list.forEach((e) => { chips.get(e.key).classList.add("compact"); e.w = chips.get(e.key).offsetWidth; });
            if (!pack(list).ok) list.forEach((e) => { chips.get(e.key).classList.remove("compact"); e.w = chips.get(e.key).offsetWidth; });
        }
        const keep = [];
        for (const e of entries) { const trial = [...keep, e]; const r = { top: pack(trial.filter((x) => x.side === "top")), bottom: pack(trial.filter((x) => x.side === "bottom")) }; if (r.top.ok && r.bottom.ok) keep.push(e); else { coHint.hidden += e.members.length; if (!coHint.groups.includes(e.gid)) coHint.groups.push(e.gid); } }
        for (const e of entries) if (!keep.includes(e)) chips.get(e.key).remove();
        /* not everything fits around the deck: a chip says how many are left out; one click shows those groups on their own, another brings the full set back.
           It is packed like any other chip (last, at the right), so it never lands on a callout: the lowest-priority bottom chips give way for it. */
        let moreE = null;
        if (!g && (coHint.solo || coHint.hidden > 0)) {
            for (let tries = 0; tries < 12 && !moreE; tries++) {
                const names = coHint.groups.map((g) => GROUPS[g].label), solo = coHint.solo, nx = solo ? null : coHint.groups[0];
                const label = solo ? coHint.group + " only. Show all" : coHint.hidden + " more in " + (names.length > 2 ? names.length + " other groups" : names.join(" and "));
                const el = h("button", { type: "button", class: "co-more", "data-key": "__more", "data-tip": solo ? "Back to the callouts of every group that fit" : "These callouts do not fit around the deck. Click to show " + GROUPS[nx].label.toLowerCase() + " on its own." }, icon("layers", 16, "co-ic"), h("span", { class: "co-t" }, label));
                el.addEventListener("click", () => { coGroup = solo ? null : nx; buildCallouts(true); });
                el.style.visibility = "hidden"; coCtl.append(el);
                const E = { key: "__more", more: true, members: [], gid: null, side: "bottom", w: el.offsetWidth, h: el.offsetHeight, cx: G.zw - 16 - el.offsetWidth / 2, cy: 0 };
                const trial = [...keep, E];
                if (pack(trial.filter((x) => x.side === "top")).ok && pack(trial.filter((x) => x.side === "bottom")).ok) { moreE = E; chips.set("__more", el); break; }
                el.remove();
                let i = keep.length - 1; while (i >= 0 && keep[i].side !== "bottom") i--;
                if (i < 0) break;
                const d = keep.splice(i, 1)[0]; chips.get(d.key).remove(); coHint.hidden += d.members.length; if (!coHint.groups.includes(d.gid)) coHint.groups.push(d.gid);
            }
        }
        const keepAll = moreE ? [...keep, moreE] : keep;
        const R = { top: pack(keepAll.filter((x) => x.side === "top")), bottom: pack(keepAll.filter((x) => x.side === "bottom")) };
        /* the bands the rows really need: when they differ from what the deck was scaled for, scale again (once) so rows fit and the deck is as large as possible */
        { const rh = (rows) => (rows.length ? CO_LEAD + rows.reduce((t, row) => t + Math.max(24, ...row.map((e) => e.h)), 0) + CO_ROWGAP * (rows.length - 1) : 0);
          const nT = Math.max(CO_LEAD + 72, rh(R.top.rows)), nB = Math.max(CO_LEAD + 72, rh(R.bottom.rows)); // never less than two rows a side: a held modifier or a layer change that adds or drops a row must not re-plan (and so move) the deck
          if (!g && !settling && (Math.abs(nT - (G.coT ?? -9)) > 3 || Math.abs(nB - (G.coB ?? -9)) > 3)) { G.coT = nT; G.coB = nB; settling = true; try { layout({ glide: true }); } finally { settling = false; } return; } }
        const dTop = G.dy + G.dh / 2 - (G.dh / 2) * G.k, dBot = G.dy + G.dh / 2 + (G.dh / 2) * G.k;
        /* a second row's stem has to come down through a gap in the first row: a box that has no gap over it slides (within its neighbours) just far enough to have one, so no leader ever steps around the whole row */
        for (const side of ["top", "bottom"]) {
            const rows = R[side].rows; if (rows.length < 2) continue;
            const r0 = rows[0].slice().sort((a, b) => a.left - b.left), gaps = [];
            for (let i = 0; i < r0.length - 1; i++) gaps.push((r0[i].left + r0[i].w + r0[i + 1].left) / 2);
            const r1 = rows[1].slice().sort((a, b) => a.left - b.left);
            r1.forEach((e, i) => {
                if (gaps.some((g) => g >= e.left + 12 && g <= e.left + e.w - 12)) return;
                const mid = e.left + e.w / 2; let g = null; for (const x of gaps) if (g == null || Math.abs(x - mid) < Math.abs(g - mid)) g = x;
                if (g == null) return;
                const nl = g < e.left + 12 ? g - 12 : g - e.w + 12, prev = r1[i - 1], next = r1[i + 1];
                if (prev && nl < prev.left + prev.w + 12) return; if (next && nl + e.w > next.left - 12) return; if (nl < 12 || nl + e.w > G.zw - 12) return;
                e.left = nl;
            });
        }
        const placed = [];
        for (const side of ["top", "bottom"]) R[side].rows.forEach((row, ri, all) => {
            const rowH = Math.max(24, ...row.map((e) => e.h));
            const y = Math.max(6, side === "top" ? dTop - CO_LEAD - rowH - ri * (rowH + CO_ROWGAP) : dBot + CO_LEAD + ri * (rowH + CO_ROWGAP));
            for (const e of row) { e.x = e.left; e.y = y; e.rowH = rowH; e.ri = ri; placed.push(e); }
        });
        // lanes: greedy, fewest crossings first
        const lanesFor = (side) => placed.filter((e) => e.side === side && !e.more).sort((a, b) => Math.abs(b.cx - (b.x + b.w / 2)) - Math.abs(a.cx - (a.x + a.w / 2)));
        /* one leader per control, from AN ANCHOR ON THE CONTROL to its box: a vertical over the deck surface (with one short sideways step when a neighbouring control is in the way),
           out through the gutter to a bus lane, and from the lane a single stem into the box. Grouped boxes merge their controls on that one bus. */
        const rectsAll = ids.map((id) => ({ id, r: outlineZone(id) }));
        // a knob's printed name sits right under it: its leader keeps out of that strip
        const labRects = ids.filter((id) => HOT[id].kind === "knob" || HOT[id].kind === "dial").map((id) => { const hs = HOT[id], a = toZone(hs.cx - 46, hs.cy + hs.h / 2 - 2), b = toZone(hs.cx + 46, hs.cy + hs.h / 2 + 34); return { id, r: { x: a.x, y: a.y, w: b.x - a.x, h: b.y - a.y } }; });
        const overlapSegs = (A, B) => { let n = 0; for (const a of A) for (const b of B) { const av = a[0] === a[2], bv = b[0] === b[2]; if (av !== bv) continue; if (av ? Math.abs(a[0] - b[0]) < CO_GAP && Math.min(Math.max(a[1], a[3]), Math.max(b[1], b[3])) - Math.max(Math.min(a[1], a[3]), Math.min(b[1], b[3])) > 1 : Math.abs(a[1] - b[1]) < CO_GAP && Math.min(Math.max(a[0], a[2]), Math.max(b[0], b[2])) - Math.max(Math.min(a[0], a[2]), Math.min(b[0], b[2])) > 1) n++; } return n; };
        const segsOf = (e, lane, doneR = [], dsx = 0) => {
            const top = e.side === "top";
            const y0 = top ? e.y + e.rowH : e.y;
            const base = top ? dTop : dBot;
            const ly = top ? base - 12 - lane * CO_GAP : base + 12 + lane * CO_GAP; // lanes are CO_GAP apart
            // the anchor sits on the control's own rim, on the side the leader leaves from (so the printed key names stay readable); a knob leaving downwards starts at its lower right, clear of its printed name
            const anchors = e.members.map((id) => {
                const hs = HOT[id], lab = hs.kind === "knob" || hs.kind === "dial";
                if (!top && lab) { const c = toZone(hs.cx, hs.cy + hs.h / 2 + 34 + 4); return { id, x: c.x, y: c.y, side: 0 }; } // a knob leaving downwards starts just under its printed name, centred, and goes straight down
                const c = toZone(hs.cx, hs.cy + (top ? -hs.h / 2 : hs.h / 2)); return { id, x: c.x, y: c.y, side: 0 };
            });
            // a stem from a second row must come down through a gap between the first row's chips, never behind one: if no gap lies over this chip, it steps sideways in the 8 px between the rows
            let chipX = clamp(e.cx + dsx, e.x + 10, e.x + e.w - 10), jog = null;
            const nearer = placed.filter((o) => o.side === e.side && o.ri < e.ri);
            if (nearer.some((o) => chipX > o.x - 4 && chipX < o.x + o.w + 4)) {
                let free = [[6, G.zw - 6]];
                for (const o of nearer) free = free.flatMap(([a, b]) => { const l = o.x - 3, r = o.x + o.w + 3; if (r <= a || l >= b) return [[a, b]]; return [[a, Math.min(b, l)], [Math.max(a, r), b]]; }).filter(([a, b]) => b - a >= 2);
                let best = null;
                const lo1 = Math.min(...nearer.map((o) => o.x)), hi1 = Math.max(...nearer.map((o) => o.x + o.w)), pen = (x) => Math.abs(x - chipX) + (x < lo1 || x > hi1 ? 90 : 0); // a gap inside the first row beats the open space beside it
                for (const [a, b] of free) { const x = clamp(chipX, a + 1, b - 1); if (best == null || pen(x) < pen(best)) best = x; }
                if (best != null) { if (best > e.x + 10 && best < e.x + e.w - 10) chipX = best; else jog = best; }
            }
            const chipX0 = chipX, jyy = y0 + (top ? 4 : -4);
            const segs = jog == null ? [[chipX, y0, chipX, ly]] : (() => { const jy = y0 + (top ? 4 : -4); return [[chipX, y0, chipX, jy], [Math.min(chipX, jog), jy, Math.max(chipX, jog), jy], [jog, jy, jog, ly]]; })();
            if (jog != null) chipX = jog;
            const stem = jog == null ? [[chipX, ly], [chipX, y0]] : [[jog, ly], [jog, jyy], [chipX0, jyy], [chipX0, y0]]; // from the bus up (or down) into the box, reversed to read control -> box
            const xs = [];
            for (const an of anchors) {
                const slider = HOT[an.id].shape === "stadium", others = rectsAll.filter((o) => o.id !== an.id && !(slider && o.id === an.id + "btn")).map((o) => o.r); // a slider's leader leaves straight up from the track's end, over its own P button
                if (an.side) others.push(...labRects.filter((o) => o.id === an.id).map((o) => o.r));
                let pick = [[an.x, an.y, an.x, ly]];
                /* a vertical run never lies on a track another leader already uses (same x within 4 px, overlapping y): each leader gets its own track, so no two lines read as one rail */
                const used = (sg) => doneR.some((d) => overlapSegs([sg], d.segs) > 0);
                const offs = slider ? [0] : [0, ...Array.from({ length: 24 }, (_, n) => (n + 1) * 3).flatMap((d) => (an.side ? [d, -d] : [d, -d])).filter((d) => !an.side || d > -30)]; // finest steps first: a leader leaves its control by the smallest sideways step that clears every neighbour and every track already drawn
                let fallback = null;
                for (const dx of offs) {
                    const x = an.x + dx, cand = dx ? [[an.x, an.y, x, an.y], [x, an.y, x, ly]] : [[an.x, an.y, an.x, ly]];
                    if (cand.some((sg) => others.some((r) => segHits(sg, r)))) continue;
                    if (cand.some(used)) { fallback = fallback || cand; continue; }
                    pick = cand; fallback = null; break;
                }
                if (fallback) pick = fallback;
                segs.push(...pick); an.segs = pick; xs.push(pick[pick.length - 1][0]);
                /* ONE continuous polyline from the control's anchor to its box: anchor -> (sideways step) -> bus lane -> along the bus -> stem into the box. A highlighted route is drawn as this single path, never as separate pieces. */
                { const pts = [[pick[0][0], pick[0][1]]]; for (const sg of pick) pts.push([sg[2], sg[3]]); for (const q of stem) pts.push(q);
                  an.route = pts.filter((q, i) => !i || Math.abs(q[0] - pts[i - 1][0]) > 0.01 || Math.abs(q[1] - pts[i - 1][1]) > 0.01); }
            }
            const lo = Math.min(chipX, ...xs), hi = Math.max(chipX, ...xs);
            if (hi - lo > 0.5) segs.push([lo, ly, hi, ly]);
            return { segs, ly, linked: e.members.slice(), anchors };
        };
        const cross = (A, B) => { let n = 0; for (const a of A) for (const b of B) { const av = a[0] === a[2], bv = b[0] === b[2]; if (av === bv) continue; const v = av ? a : b, hz = av ? b : a; if (v[0] > Math.min(hz[0], hz[2]) + 0.5 && v[0] < Math.max(hz[0], hz[2]) - 0.5 && hz[1] > Math.min(v[1], v[3]) + 0.5 && hz[1] < Math.max(v[1], v[3]) - 0.5) n++; } return n; };
        /* two leaders may cross at a right angle, but never lie on the same track: a collinear overlap (same y or same x, shared run) costs more than any crossing, so the lane choice moves one of them */
        const overlap = (A, B) => { let n = 0; for (const a of A) for (const b of B) { const av = a[0] === a[2], bv = b[0] === b[2]; if (av !== bv) continue; if (av ? Math.abs(a[0] - b[0]) < CO_GAP && Math.min(Math.max(a[1], a[3]), Math.max(b[1], b[3])) - Math.max(Math.min(a[1], a[3]), Math.min(b[1], b[3])) > 1 : Math.abs(a[1] - b[1]) < CO_GAP && Math.min(Math.max(a[0], a[2]), Math.max(b[0], b[2])) - Math.max(Math.min(a[0], a[2]), Math.min(b[0], b[2])) > 1) n++; } return n; };
        const done = []; // both sides share one list: a leader going up and one going down can still lie on the same track (Shift above, Col below)
        for (const side of ["top", "bottom"]) {
            for (const e of lanesFor(side)) {
                let best = null;
                const tried = new Set();
                for (let lane = 0; lane < 5; lane++) for (const dsx of [0, 8, -8, 16, -16, 24, -24, 32, -32, 40, -40, 56, -56, 72, -72]) {
                    const o = segsOf(e, lane, done, dsx), tk = lane + ":" + o.segs[0][0].toFixed(0); if (tried.has(tk)) continue; tried.add(tk);
                    const bad = done.reduce((t, d) => t + overlap(o.segs, d.segs), 0), c = bad * 1000 + done.reduce((t, d) => t + cross(o.segs, d.segs), 0) + lane * 0.01 + Math.abs(dsx) * 0.001;
                    if (!best || c < best.c) best = { c, o, bad };
                    if (best.c < 1) break;
                }
                e.route = best.o; e.bad = best.bad; done.push(best.o);
            }
        }
        /* hard rule: no two leaders closer than CO_GAP on parallel tracks. A leader the quick search could not place cleanly is routed again with a grid search (A*) that treats every other leader, control and box as blocked. */
        hardRepair(placed, labRects);
        let n = 0;
        const gU = s("g", { class: "co-gu" }), gL = s("g", { class: "co-gl" }), gA = s("g", { class: "co-ga" }), gR = s("g", { class: "co-gr" }); coSvg.append(gU, gL, gR, gA);
        for (const e of placed) {
            const c = chips.get(e.key);
            c.style.transform = `translate(${Math.round(e.x)}px, ${Math.round(e.y)}px)`; c.style.visibility = ""; if (moreHadFocus && c.classList.contains("co-more")) c.focus({ preventScroll: true });
            if (e.more) continue;
            if (false) anim(c, reduced() ? [{ opacity: 0 }, { opacity: 1 }] : [{ opacity: 0, translate: "0 6px" }, { opacity: 1, translate: "0 0" }], { duration: reduced() ? 120 : 180, delay: reduced() ? 0 : 170 + Math.min(220, n * 16), easing: EASE_OUT, fill: "backwards" });
            const dOf = (sgs) => sgs.map((sg) => `M${sg[0].toFixed(1)} ${sg[1].toFixed(1)} L${sg[2].toFixed(1)} ${sg[3].toFixed(1)}`).join(" ");
            const mine = new Set(e.route.anchors.flatMap((an) => an.segs)), allSl = e.route.anchors.every((an) => SLIDER_COLOR[an.id]);
            gU.append(s("path", { d: dOf(e.route.segs), class: "co-under", "data-key": e.key }));
            const p = s("path", { d: dOf(e.route.segs.filter((sg) => !mine.has(sg))), class: "co-line" + (allSl ? " co-sl" : ""), "data-key": e.key }); gL.append(p);
            if (allSl) { const c1 = SLIDER_COLOR[e.route.anchors[0].id]; if (new Set(e.route.anchors.map((an) => SLIDER_COLOR[an.id])).size === 1) p.style.setProperty("--c", c1); }
            for (const an of e.route.anchors) {
                const sc0 = SLIDER_COLOR[an.id], ml = s("path", { d: dOf(an.segs), class: "co-line co-m", "data-key": e.key, "data-id": an.id }); if (sc0) ml.style.setProperty("--c", sc0); gL.append(ml);
                { const rp = s("path", { d: an.route.map((q, i) => (i ? "L" : "M") + q[0].toFixed(1) + " " + q[1].toFixed(1)).join(" "), class: "co-route", "data-key": e.key, "data-id": an.id }); if (sc0) rp.style.setProperty("--c", sc0); gR.append(rp); const fp = s("path", { d: rp.getAttribute("d"), class: "co-flow", "data-key": e.key, "data-id": an.id }); if (sc0) fp.style.setProperty("--c", sc0); gR.append(fp); const fc = s("path", { d: rp.getAttribute("d"), class: "co-flow co-core", "data-key": e.key, "data-id": an.id }); gR.append(fc); }
                const dot = s("circle", { class: "co-end", "data-key": e.key, "data-id": an.id, cx: an.x.toFixed(1), cy: an.y.toFixed(1), r: 3 }); if (sc0) dot.style.setProperty("--c", sc0); gA.append(dot);
            }
            { const cols = new Set(e.members.map((id) => SLIDER_COLOR[id] || "")); if (cols.size === 1 && [...cols][0]) c.style.setProperty("--c", [...cols][0]); } // a box for one slider takes that slider's colour
            if (!e.route.linked.length) c.classList.add("nolink");
            if (false) anim(p, [{ opacity: 0 }, { opacity: 1 }], { duration: reduced() ? 120 : 200, delay: reduced() ? 0 : 170 + Math.min(220, n * 16), fill: "backwards" });
            n++;
        }
        coItemsPlaced = placed; chipMembers = new Map(placed.map((e) => [e.key, e.members]));
        /* closed loop: measure the block that was really drawn (top row of boxes to the lower band or the deck) against the stage; a block that overflows is scaled down, one that is off-centre is nudged, up to three passes */
        /* second check against the DOM itself: whatever the arithmetic said, if the deck or a box lies outside the stage as drawn, plan again from the real size */
        if (!g && verifyN < 3) { const zr = zone.getBoundingClientRect(), dr = deck.getBoundingClientRect(), vv = window.visualViewport, vis = vv ? vv.height + vv.offsetTop : innerHeight, lim = Math.min(zr.bottom, vis); if (dr.bottom > lim + 2 || dr.top > zr.top + G.zh * 0.5) { verifyN++; settling = true; try { layout({ verify: true, glide: false }); } finally { settling = false; } return; } }
        if (!g && verifyN < 3 && placed.length) {
            const top = Math.min(dTop, ...placed.map((e) => e.y)), bottom = Math.max(dBot, ...placed.map((e) => e.y + e.rowH)), gT = top, gB = G.botEdge - bottom;
            if (gB < -1 || gT < 6) { verifyN++; const over = Math.max(0, 12 - gT) + Math.max(0, -gB), blockH = bottom - top; G.kMax = Math.max(CO_MINK, G.k * (1 - over / Math.max(1, G.dh * G.k))) ; if (blockH > G.botEdge - 12 || gB < -1) { settling = true; try { layout({ verify: true, glide: false }); } finally { settling = false; } return; } }
            else if (false && Math.abs(gT - gB) > 8 && gT > 6 && gB >= 0) { /* off: re-centring on the rows actually drawn moved the deck whenever a layer changed the rows; the bands are fixed now */ verifyN++; G.adj = (G.adj || 0) + (gB - gT) / 2; settling = true; try { layout({ verify: true, glide: false }); } finally { settling = false; } return; }
        }
        if (g) g.placed = placed;
        updateCalloutHighlight();
    }
    /** grid router for every leader the quick search could not place cleanly (overlap, clearance, a label crossed, or far longer than needed). Per leader: A* on a 4 px grid, orthogonal moves, a penalty per turn, other controls (CO_CLEAR + skin), labels, other boxes and every other leader's tracks (CO_GAP either side, parallel only) blocked. The leader may leave its control from the middle of any of its four edges (a knob going down: just under its printed name); the cheapest way out wins. Clearance relaxes 6 -> 4 -> 2 px only when no way out exists, and that is reported. */
    function hardRepair(placed, labRects) {
        const S = 4, W = G.zw, Hh = G.zh;
        const ctl = ids.map((id) => ({ id, r: outlineZone(id) }));
        window.__coRep = [];
        const snap = placed.map((e) => e.more ? null : { segs: e.route.segs, bad: e.bad, hard: e.hard, an: e.route.anchors.map((an) => ({ x: an.x, y: an.y, route: an.route, segs: an.segs })) });
        const restore = () => placed.forEach((e, k) => { const s = snap[k]; if (!s) return; e.route.segs = s.segs; e.bad = s.bad; e.hard = s.hard; e.route.anchors.forEach((an, m) => Object.assign(an, s.an[m])); });
        let order = placed.slice(), failedKeys = new Set();
        for (let attempt = 0; attempt < 3; attempt++) {
            if (attempt) { restore(); window.__coRep = []; order = [...placed.filter((q) => failedKeys.has(q.key)), ...placed.filter((q) => !failedKeys.has(q.key))]; } // a leader that found no way out goes first next time: the others route around it
            const failed = repairPass(order);
            if (!failed.length) break;
            failed.forEach((k) => failedKeys.add(k));
        }
        function repairPass(list) {
        const failedNow = [];
        const mineOf = (e) => new Set(e.members);
        const violates = (e) => { const mine = mineOf(e); return e.route.segs.some((sg) => ctl.some((c) => !mine.has(c.id) && segGap(sg, c.r) < CO_CLEAR - 0.01) || labRects.some((l) => segGap(sg, l.r) < 1.5)); };
        const plen = (pts) => pts.reduce((s, q, k) => s + (k ? Math.abs(q[0] - pts[k - 1][0]) + Math.abs(q[1] - pts[k - 1][1]) : 0), 0);
        for (const e of list) {
            if (e.more) continue;
            const top = e.side === "top", y0 = top ? e.y + e.rowH : e.y, yE = y0;
            const ideal = (an) => Math.max(0, e.x + 10 - an.x, an.x - (e.x + e.w - 10)) + Math.abs(yE - an.y);
            const longE = e.route.anchors.some((an) => an.route && plen(an.route) > 1.2 * ideal(an) + 12);
            const vio = violates(e);
            if (!(e.bad > 0) && !vio && !longE) continue;
            const oldLen = e.route.anchors.reduce((s, an) => s + (an.route ? plen(an.route) : 0), 0), oldBad = e.bad;
            const others = placed.filter((o) => o !== e && !o.more && o.route);
            const segsOther = others.flatMap((o) => o.route.segs);
            let ok = true, usedCL = CO_CLEAR; const newAn = [];
            for (const an of e.route.anchors) {
                const o = outlineZone(an.id), cxz = o.x + o.w / 2, cyz = o.y + o.h / 2, hs = HOT[an.id], slider = hs.shape === "stadium", lab = isLabelled(an.id);
                const ox = cxz - S * Math.floor(cxz / S), oy = cyz - S * Math.floor(cyz / S);
                const nx = Math.ceil((W - ox) / S), ny = Math.ceil((Hh - oy) / S), n = nx * ny;
                const X = (i) => ox + i * S, Y = (j) => oy + j * S, ci = (x) => Math.round((x - ox) / S), cj = (y) => Math.round((y - oy) / S);
                const labBottom = lab ? labelZone(an.id).y + labelZone(an.id).h : 0;
                const want = top ? 0 : 2; // 0 up, 1 right, 2 down, 3 left
                const srcs = [
                    { x: cxz, y: o.y, dir: 0, cost: (want === 0 ? 0 : 6) + (slider ? 30 : 0) },
                    { x: cxz, y: lab ? labBottom + 4 : o.y + o.h, dir: 2, cost: (want === 2 ? 0 : 6) + (slider ? 30 : 0) },
                    { x: o.x, y: cyz, dir: 3, cost: slider ? 0 : 8 },
                    { x: o.x + o.w, y: cyz, dir: 1, cost: slider ? 0 : 8 },
                ];
                let goal = -1, G0 = null, from = null, startCell = null, cells = null, join_ = [-1];
                for (const CL of [CO_CLEAR, 4, 2]) {
                    const solid = new Uint8Array(n), hB = new Uint8Array(n), vB = new Uint8Array(n), xV = new Uint8Array(n), xH = new Uint8Array(n), forced = new Uint8Array(n * 4);
                    const fill = (arr, x1, y1, x2, y2) => { for (let j = Math.max(0, Math.ceil((y1 - oy) / S)); j <= Math.min(ny - 1, Math.floor((y2 - oy) / S)); j++) for (let i = Math.max(0, Math.ceil((x1 - ox) / S)); i <= Math.min(nx - 1, Math.floor((x2 - ox) / S)); i++) arr[j * nx + i] = 1; };
                    for (const c of ctl) if (c.id === an.id) fill(solid, c.r.x + 3, c.r.y + 3, c.r.x + c.r.w - 3, c.r.y + c.r.h - 3); else fill(solid, c.r.x - CL, c.r.y - CL, c.r.x + c.r.w + CL, c.r.y + c.r.h + CL);
                    for (const l of labRects) { const pad = l.id === an.id ? 1 : CL; fill(solid, l.r.x - pad, l.r.y - pad, l.r.x + l.r.w + pad, l.r.y + l.r.h + pad); }
                    for (const q of placed) { if (q.more) continue; const pad = q === e ? 0.5 : 2; fill(solid, q.x - pad, q.y - pad, q.x + q.w + pad, q.y + q.rowH + pad); }
                    fill(solid, -1, -1, -0.5, Hh); fill(solid, W + 0.5, -1, W + 1, Hh);
                    for (const sg of segsOther) {
                        if (sg[0] === sg[2]) { fill(xV, sg[0] - S / 2 + 0.01, Math.min(sg[1], sg[3]), sg[0] + S / 2 - 0.01, Math.max(sg[1], sg[3])); fill(vB, sg[0] - CO_GAP - 0.6, Math.min(sg[1], sg[3]) - 2, sg[0] + CO_GAP + 0.6, Math.max(sg[1], sg[3]) + 2); }
                        else { fill(xH, Math.min(sg[0], sg[2]), sg[1] - S / 2 + 0.01, Math.max(sg[0], sg[2]), sg[1] + S / 2 - 0.01); fill(hB, Math.min(sg[0], sg[2]) - 2, sg[1] - CO_GAP - 0.6, Math.max(sg[0], sg[2]) + 2, sg[1] + CO_GAP + 0.6); }
                    }
                    /* members of one box may join the leader of a member already routed (a T junction): the shared stretch is drawn once, each member's own route still reads control -> box */
                    const join = new Int32Array(n).fill(-1);
                    newAn.forEach((q, pi) => { for (let k = 1; k < q.pts.length - 1; k++) { const a0 = q.pts[k - 1], b0 = q.pts[k], vert = Math.abs(a0[0] - b0[0]) < 0.01, code = pi * 4000 + (k - 1) * 2 + (vert ? 1 : 0);
                        if (vert) { const ii = ci(a0[0]); if (Math.abs(X(ii) - a0[0]) > 2 || ii < 0 || ii >= nx) continue; for (let jj = Math.max(0, Math.ceil((Math.min(a0[1], b0[1]) - oy) / S)); jj <= Math.min(ny - 1, Math.floor((Math.max(a0[1], b0[1]) - oy) / S)); jj++) join[jj * nx + ii] = code; }
                        else { const jj = cj(a0[1]); if (Math.abs(Y(jj) - a0[1]) > 2 || jj < 0 || jj >= ny) continue; for (let ii = Math.max(0, Math.ceil((Math.min(a0[0], b0[0]) - ox) / S)); ii <= Math.min(nx - 1, Math.floor((Math.max(a0[0], b0[0]) - ox) / S)); ii++) join[jj * nx + ii] = code; } } });
                    const gx1 = e.x + 10, gx2 = e.x + e.w - 10, gj = top ? Math.ceil((y0 + 1 - oy) / S) : Math.floor((y0 - 1 - oy) / S), gi1 = Math.ceil((gx1 - ox) / S), gi2 = Math.floor((gx2 - ox) / S);
                    const D = [[0, -1], [1, 0], [0, 1], [-1, 0]];
                    G0 = new Float32Array(n * 4).fill(1e9); from = new Int32Array(n * 4).fill(-1);
                    const heap = [];
                    const push = (f, s) => { heap.push([f, s]); let i = heap.length - 1; while (i > 0) { const p = (i - 1) >> 1; if (heap[p][0] <= heap[i][0]) break; [heap[p], heap[i]] = [heap[i], heap[p]]; i = p; } };
                    const pop = () => { const r0 = heap[0], l = heap.pop(); if (heap.length) { heap[0] = l; let i = 0; for (;;) { let a = 2 * i + 1, b = a + 1, m = i; if (a < heap.length && heap[a][0] < heap[m][0]) m = a; if (b < heap.length && heap[b][0] < heap[m][0]) m = b; if (m === i) break; [heap[m], heap[i]] = [heap[i], heap[m]]; i = m; } } return r0; };
                    const hE = (i, j) => Math.abs(j - gj) + (i < gi1 ? gi1 - i : i > gi2 ? i - gi2 : 0);
                    for (const sc of srcs) { const si = ci(sc.x), sj = cj(sc.y); if (si < 0 || sj < 0 || si >= nx || sj >= ny) continue; const s0 = (sj * nx + si) * 4 + sc.dir; G0[s0] = sc.cost; forced[s0] = 1; push(sc.cost + hE(si, sj), s0); }
                    let it = 0; goal = -1;
                    while (heap.length && it++ < 260000) {
                        const [, s] = pop(), d = s & 3, c = s >> 2, i = c % nx, j = (c / nx) | 0, g = G0[s];
                        if (j === gj && i >= gi1 && i <= gi2 && d === (top ? 0 : 2)) { goal = s; break; }
                        if (join[c] >= 0 && g > 14 && ((join[c] & 1) ? (d & 1) : !(d & 1))) { goal = s; break; } // joins an earlier member of the same box, arriving at a right angle
                        for (let nd = 0; nd < 4; nd++) {
                            if (((nd + 2) & 3) === d || (forced[s] && nd !== d)) continue;
                            const ni = i + D[nd][0], nj = j + D[nd][1]; if (ni < 0 || nj < 0 || ni >= nx || nj >= ny) continue;
                            const nc = nj * nx + ni; if (solid[nc]) continue;
                            if (nd & 1 ? hB[nc] || (!forced[s] && hB[c]) : vB[nc] || (!forced[s] && vB[c])) continue; // a move is blocked from a blocked cell too (a turn on someone else's track must not continue along it)
                            const ng = g + 1 + (nd !== d ? 8 : 0) + (nd & 1 ? (xV[nc] ? 5 : 0) : (xH[nc] ? 5 : 0)), ns = nc * 4 + nd;
                            if (ng < G0[ns]) { G0[ns] = ng; from[ns] = s; push(ng + hE(ni, nj), ns); }
                        }
                    }
                    if (goal >= 0) { usedCL = Math.min(usedCL, CL); const gc = goal >> 2, chipGoal = ((gc / nx) | 0) === gj && gc % nx >= gi1 && gc % nx <= gi2 && (goal & 3) === (top ? 0 : 2); join_[0] = join[gc] >= 0 && !chipGoal ? join[gc] : -1; break; }
                }
                if (goal < 0) { ok = false; if (e.bad > 0 || vio) failedNow.push(e.key); window.__coRep.push([an.id, "fail"]); break; }
                cells = []; for (let s = goal; s >= 0; s = from[s]) cells.push([X((s >> 2) % nx), Y(((s >> 2) / nx) | 0)]);
                cells.reverse();
                const pts = [cells[0]]; for (let k = 1; k < cells.length; k++) { const a = pts[pts.length - 1], b = cells[k], nxt = cells[k + 1]; if (nxt && ((b[0] - a[0]) * (nxt[1] - b[1]) - (b[1] - a[1]) * (nxt[0] - b[0])) === 0) continue; pts.push(b); }
                const jcode = join_[0];
                if (jcode >= 0) { const q = newAn[Math.floor(jcode / 4000)], k = (jcode % 4000) >> 1, vert = jcode & 1, a0 = q.pts[k], b0 = q.pts[k + 1], last = pts[pts.length - 1], mp = vert ? [a0[0], last[1]] : [last[0], a0[1]]; pts[pts.length - 1] = mp; pts.push(...q.pts.slice(k + 1)); }
                else pts.push([pts[pts.length - 1][0], y0]);
                const dd = pts.filter((q, k) => !k || Math.abs(q[0] - pts[k - 1][0]) > 0.01 || Math.abs(q[1] - pts[k - 1][1]) > 0.01);
                newAn.push({ an, pts: dd, sx: cells[0][0], sy: cells[0][1] });
            }
            if (!ok) continue;
            if (!(oldBad > 0) && !vio && newAn.reduce((s, q) => s + plen(q.pts), 0) >= oldLen * 0.9) continue; // only a real shortening replaces a clean route
            const all = [];
            for (const { an, pts, sx, sy } of newAn) { an.x = sx; an.y = sy; an.route = pts; an.segs = pts.slice(1).map((q, k) => [pts[k][0], pts[k][1], q[0], q[1]]); all.push(...an.segs); }
            e.route.segs = all; e.bad = 0; e.hard = true; window.__coRep.push([e.members.join("+"), "ok", usedCL]);
        }
        return failedNow;
        }
    }
    let coItemsPlaced = [], chipMembers = new Map();
    function pack(list) {
        const gap = 20, lo = 12, hi = list.length && list[0].side === "bottom" && G.ghostHiB ? G.ghostHiB : G.zw - 12; // 20 px between boxes: a far-tier leader needs a gap it can pass through with its clearance
        const sorted = list.slice().sort((a, b) => a.cx - b.cx);
        const capRows = sorted.length && G.coRows ? G.coRows[sorted[0].side] : 2; // how many tiers the space on this side holds
        if (sorted.length && capRows === 0) return { ok: false, rows: [] };
        const fit = (arr) => {
            if (!arr.length) return { ok: true, items: [] };
            const total = arr.reduce((t, e) => t + e.w, 0) + gap * (arr.length - 1);
            if (total > hi - lo) return { ok: false };
            const it = arr.map((e) => ({ e, left: e.cx - e.w / 2 }));
            for (let i = 0; i < it.length; i++) it[i].left = Math.max(it[i].left, i ? it[i - 1].left + it[i - 1].e.w + gap : lo);
            for (let i = it.length - 1; i >= 0; i--) { const right = i === it.length - 1 ? hi : it[i + 1].left - gap; it[i].left = Math.min(it[i].left, right - it[i].e.w); }
            for (let i = 0; i < it.length; i++) it[i].left = Math.max(it[i].left, i ? it[i - 1].left + it[i - 1].e.w + gap : lo);
            return { ok: true, items: it.map((o) => Object.assign(o.e, { left: o.left })) };
        };
        /* brick: three or more boxes on a side are laid in two offset tiers, neighbours alternating near / far from the deck (like bricks). A box then has a whole step of room on its own tier, sits over its control, and the far tier's stems pass through the gaps of the near tier: no two neighbours share a line, and no stem runs behind a box. */
        const a = sorted.filter((_, i) => i % 2 === 0), b = sorted.filter((_, i) => i % 2 === 1);
        /* nearest free slot: left to right, each box takes the tier where it lands closest to its own control (the near tier wins ties), so leaders stay short and boxes sit under their controls */
        const one = fit(sorted);
        if (capRows === 1) return one.ok ? { ok: true, rows: [one.items] } : { ok: false, rows: [] };
        if (one.ok && (sorted.length < 2 || one.items.every((e) => Math.abs(e.left - (e.cx - e.w / 2)) < 2))) return { ok: true, rows: [one.items] }; // every box already sits under its control: one tier
        if (sorted.length >= 2) {
            const end = [lo - gap, lo - gap], rows = [[], []];
            for (const e of sorted) {
                const want = e.cx - e.w / 2; let bestR = 0, bestL = 0, bestC = 1e9;
                for (let rr = 0; rr < 2; rr++) { const left = Math.max(want, end[rr] + gap), c = Math.abs(left - want) + rr * 14; if (c < bestC) { bestC = c; bestR = rr; bestL = left; } }
                rows[bestR].push(e); end[bestR] = bestL + e.w;
            }
            const f0 = fit(rows[0]), f1 = fit(rows[1]);
            if (f0.ok && f1.ok) return { ok: true, rows: f1.items.length ? [f0.items, f1.items] : [f0.items] };
        }
        if (one.ok) return { ok: true, rows: [fit(sorted).items] };
        const fa = fit(a), fb = fit(b);
        if (fa.ok && fb.ok) return { ok: true, rows: [fb.items, fa.items] };
        return { ok: false, rows: [] };
    }
    let hlOv = null; // a ghost hover callout: the control it belongs to counts as hovered (keyboard focus has no pointer hover)
    function updateCalloutHighlight() {
        const hovered = hlOv || hoveredReal, hov = new Set(), pick = new Set();
        for (const [k, m] of chipMembers) { if (hovered && m.includes(hovered)) hov.add(k); if (selected && m.includes(selected)) pick.add(k); }
        coSvg.querySelectorAll(".co-line,.co-end,.co-under").forEach((p) => { const k = p.dataset.key; p.classList.toggle("hi", hov.has(k)); p.classList.toggle("picked", pick.has(k)); });
        const segOn = (p) => p.dataset.id === hovered || (chipKey === p.dataset.key && (!chipSeg || chipSeg === p.dataset.id));
        coSvg.querySelectorAll(".co-route").forEach((p) => { const id = p.dataset.id; p.classList.toggle("picked", id === selected); p.classList.toggle("hi", id !== selected && segOn(p)); });
        coSvg.querySelectorAll(".co-flow").forEach((p) => p.classList.toggle("on", segOn(p)));
        coWrap.querySelectorAll(".co-k").forEach((k) => { k.classList.toggle("hi", k.dataset.seg === hovered || (chipSeg === k.dataset.seg)); k.classList.toggle("picked", k.dataset.seg === selected); }); // a key's row in the group box lights with its line // data flowing control -> box while the control or its box is hovered (CSS shows it only when motion is allowed)
        coWrap.querySelectorAll(".co-chip").forEach((c) => { const k = c.dataset.key; c.classList.toggle("hi", hov.has(k)); c.classList.toggle("picked", pick.has(k)); });
    }
    let chipHover = [], chipKey = null, chipSeg = null;
    coWrap.addEventListener("pointerover", (e) => { const c = e.target.closest(".co-chip"); if (!c) return; chipKey = c.dataset.key; chipSeg = (e.target.closest(".co-k") || {}).dataset?.seg || null; updateCalloutHighlight(); chipHover = chipSeg ? [chipSeg] : (chipMembers.get(c.dataset.key) || []); chipHover.forEach((id) => nodes.get(id)?.g.classList.add("hover")); });
    coWrap.addEventListener("pointerout", (e) => { const c = e.target.closest(".co-chip"); if (!c) return; chipHover.forEach((id) => nodes.get(id)?.g.classList.remove("hover")); chipHover = []; chipKey = null; chipSeg = null; updateCalloutHighlight(); });
    coWrap.addEventListener("click", (e) => { const c = e.target.closest(".co-chip"); if (c) { const sg = e.target.closest(".co-k"), m = chipMembers.get(c.dataset.key) || []; if (sg) onSelect(sg.dataset.seg); else if (m.length) onSelect(m[0]); } });

    /* ---- ghost callouts (normal mode, Callouts off) ----
       The deck keeps its normal size and place. A ghost layer is the same router and the same look as the real callouts, drawn only in the space the normal-size deck leaves above and below it;
       a box that does not fit there is skipped (never shrinks the deck). Two uses: an INTRO (the layer's callouts one by one in reading order, hold, fade out together) and a HOVER / focus callout (that control's box + leader only). */
    const GH = { STAG: 75, IN: 170, HOLD: 1200, OUT: 200, FAST: 100, HOV_IN: 120, GRACE: 90 };
    const ghostOk = () => !coOn && G.placed && !!G.zw && !document.body.classList.contains("test-mode") && !document.body.classList.contains("view-list");
    /** the tiers the normal-size deck leaves free: the same arithmetic as the callout bands, measured from the deck as it is */
    function ghostRows() {
        const dTop = G.dy + G.dh / 2 - (G.dh / 2) * G.k, dBot = G.dy + G.dh / 2 + (G.dh / 2) * G.k, rowsFor = (a) => (a - CO_LEAD < 28 ? 0 : a - CO_LEAD < 28 * 2 + CO_ROWGAP ? 1 : 2);
        return { top: rowsFor(dTop - 6), bottom: rowsFor(G.botEdge - dBot) };
    }
    function buildGhost(items) {
        const keep = { rows: G.coRows, hint: coHint }, g = { items };
        G.coRows = ghostRows(); coWrap.classList.add("ghost");
        /* the engine status pill (#status, bottom right of the stage) is an obstacle like the inspector: bottom-row boxes stop 6 px (+ the box gap) short of it whenever they would reach its height */
        { const st = document.getElementById("status"), zr = zone.getBoundingClientRect(), r = st && st.getBoundingClientRect(), dBot = G.dy + G.dh / 2 + (G.dh / 2) * G.k;
          if (r && r.width && r.height && dBot + CO_LEAD + 28 + 6 > r.top - zr.top) G.ghostHiB = Math.min(G.zw - 12, r.left - zr.left - 6 - 12); }
        try { buildCore(false, items, g); } finally { G.coRows = keep.rows; coHint = keep.hint; G.ghostHiB = null; }
        return g.placed || [];
    }
    /** remove the ghost layer now; ms > 0 = what was visible fades out (on a copy, so the next ghost can start at once) */
    function ghostKill(ms) {
        if (!gh) return;
        const g = gh; gh = null; ghSeq++; clearTimeout(ghLeaveT); g.timers.forEach(clearTimeout);
        coWrap.getAnimations({ subtree: true }).forEach((a) => a.cancel());
        if (ms > 0 && coWrap.querySelector(".co-chip")) {
            const clone = coWrap.cloneNode(true); clone.classList.add("co-dying");
            if (g.started) for (const el of clone.querySelectorAll("[data-key]")) if (!g.started.has(el.dataset.key)) el.remove();
            clone.querySelectorAll("[style*='opacity: 0']").forEach((el) => el.remove());
            zone.insertBefore(clone, coCtl);
            if (reduced()) clone.remove(); else clone.animate([{ opacity: 1 }, { opacity: 0 }], { duration: ms, easing: EASE_IN, fill: "forwards" }).onfinish = () => clone.remove();
        }
        coAll().forEach((c) => c.remove()); coSvg.replaceChildren(); coWrap.classList.remove("ghost"); coWrap.style.opacity = "";
        hlOv = null; chipMembers = new Map(); coItemsPlaced = [];
    }
    /** reading order: rows top to bottom (controls within ~48 photo px of each other share a row), each row left to right */
    function readingOrder(list) {
        const rows = [];
        for (const e of list.slice().sort((a, b) => a.cy - b.cy)) { const r = rows[rows.length - 1]; if (r && Math.abs(e.cy - r.cy) <= 48) r.items.push(e); else rows.push({ cy: e.cy, items: [e] }); }
        return rows.flatMap((r) => r.items.sort((a, b) => a.cx - b.cx));
    }
    /** the intro: the layer's callouts appear one by one, hold, fade out together. Any pointer, click or key cancels it. Reduced motion: none. */
    function introCallouts() {
        if (reduced() || !ghostOk() || !lastCoGhost) return;
        ghostKill(GH.FAST);
        const seq = ++ghSeq, g = gh = { kind: "intro", timers: [], started: new Set() };
        const run = () => {
            if (seq !== ghSeq || gh !== g) return;
            if (!ghostOk()) { ghostKill(0); return; }
            const order = readingOrder(buildGhost(lastCoGhost).filter((e) => !e.more)); g.built = true;
            if (!order.length) { ghostKill(0); return; }
            const mine = (key) => [...coSvg.querySelectorAll(".co-under,.co-line,.co-end")].filter((el) => el.dataset.key === key);
            for (const e of order) { const c = coWrap.querySelector('.co-chip[data-key="' + CSS.escape(e.key) + '"]'); e.__chip = c; e.__svg = mine(e.key); if (c) c.style.opacity = "0"; e.__svg.forEach((el) => { el.style.opacity = "0"; }); }
            order.forEach((e, i) => g.timers.push(setTimeout(() => {
                if (gh !== g) return;
                g.started.add(e.key);
                if (e.__chip) { e.__chip.style.opacity = ""; anim(e.__chip, [{ opacity: 0, translate: "0 " + (e.side === "top" ? 4 : -4) + "px" }, { opacity: 1, translate: "0 0" }], { duration: GH.IN, easing: EASE_OUT }); }
                for (const el of e.__svg) {
                    el.style.opacity = "";
                    if (el.tagName === "path") { el.setAttribute("pathLength", "1"); const a = anim(el, [{ opacity: 0, strokeDasharray: "1 1", strokeDashoffset: 1 }, { opacity: 1, strokeDasharray: "1 1", strokeDashoffset: 0 }], { duration: GH.IN + 40, easing: EASE_OUT }); a.onfinish = () => el.removeAttribute("pathLength"); }
                    else anim(el, [{ opacity: 0 }, { opacity: 1 }], { duration: GH.IN, easing: EASE_OUT });
                }
            }, i * GH.STAG)));
            const tEnd = (order.length - 1) * GH.STAG + GH.IN + GH.HOLD;
            g.timers.push(setTimeout(() => { if (gh !== g) return; coWrap.animate([{ opacity: 1 }, { opacity: 0 }], { duration: GH.OUT, easing: EASE_IN, fill: "forwards" }); g.timers.push(setTimeout(() => { if (gh === g) ghostKill(0); }, GH.OUT + 20)); }, tEnd));
        };
        // the page is still settling (fonts, the deck's own entrance): measure and draw once it has
        g.timers.push(setTimeout(() => { (document.fonts && document.fonts.ready ? document.fonts.ready : Promise.resolve()).then(run); }, 520));
    }
    /** hover / keyboard focus: that control's callout only, at once; a short grace before it leaves so moving between neighbours does not flicker */
    function ghostShow(id) {
        clearTimeout(ghLeaveT);
        if (!ghostOk()) return;
        if (gh && gh.kind === "hover" && gh.id === id) return;
        const items = (lastCoGhost || []).filter((i) => i.id === id && i.label);
        ghostKill(GH.FAST);
        if (!items.length) return;
        const g = gh = { kind: "hover", id, timers: [], started: null };
        const placed = buildGhost(items); g.built = true;
        if (!placed.length) { ghostKill(0); return; }
        hlOv = id; updateCalloutHighlight();
        if (!reduced()) coWrap.animate([{ opacity: 0 }, { opacity: 1 }], { duration: GH.HOV_IN, easing: EASE_OUT });
    }
    function ghostLeave() { clearTimeout(ghLeaveT); ghLeaveT = setTimeout(() => { if (gh && gh.kind === "hover") ghostKill(GH.FAST); }, GH.GRACE); }
    const introCancel = () => { if (gh && gh.kind === "intro") ghostKill(GH.FAST); };
    document.addEventListener("pointerdown", introCancel, true); document.addEventListener("keydown", introCancel, true);

    /* ---- Callouts ON: intro of the full set, and its quick reverse ----
       The boxes are already at their final places (the router ran); only their appearance is staged. One by one in reading order: the leader DRAWS from the control's origin dot out to the box, then the box fades in (4 px slide) as the leader arrives.
       Budget: the whole set within CI.TOTAL (1.5 s) from the first leader starting to the last box fully visible. Reduced motion: nothing (instant). Anything that interrupts (rebuild, toggle, L, hover, layer change) calls coIntroFinish: the end state at once. */
    const CI = { LEAD: 220, BOX_AT: 200, BOX: 160, TOTAL: 1500, MIN: 30, MAX: 70, OUT_BOX: 120, OUT_LEAD: 150 };
    let coIn = null;
    function coIntroFinish() {
        if (!coIn) return;
        const c = coIn; coIn = null; c.timers.forEach(clearTimeout);
        coWrap.getAnimations({ subtree: true }).forEach((a) => a.cancel()); coCtl.getAnimations({ subtree: true }).forEach((a) => a.cancel());
        coWrap.classList.remove("co-intro"); coCtl.classList.remove("co-intro");
        coSvg.querySelectorAll("[pathLength]").forEach((el) => el.removeAttribute("pathLength"));
    }
    function coIntro() {
        coIntroFinish();
        if (!coOn || reduced() || !coItemsPlaced.length) return;
        const order = readingOrder(coItemsPlaced.filter((e) => !e.more)); if (!order.length) return;
        const n = order.length, stag = n > 1 ? clamp((CI.TOTAL - CI.BOX_AT - CI.BOX) / (n - 1), CI.MIN, CI.MAX) : 0, c = coIn = { timers: [] };
        coWrap.classList.add("co-intro"); // the strong route and the data flow stay out until the intro is over
        const by = (sel, key) => [...coSvg.querySelectorAll(sel)].filter((el) => el.dataset.key === key);
        const fb = { fill: "both", easing: EASE_OUT };
        order.forEach((e, i) => {
            const d0 = i * stag, chip = coWrap.querySelector('.co-chip[data-key="' + CSS.escape(e.key) + '"]');
            const drawn = [...by(".co-line.co-m", e.key).map((el) => [el, 0, 0.62]), ...by(".co-line:not(.co-m)", e.key).map((el) => [el, 0.38, 1])];
            for (const [el, a, b] of drawn) { el.setAttribute("pathLength", "1"); el.animate([{ strokeDasharray: "1 1", strokeDashoffset: 1 }, { strokeDasharray: "1 1", strokeDashoffset: 0 }], { ...fb, delay: d0 + a * CI.LEAD, duration: (b - a) * CI.LEAD }); }
            by(".co-under", e.key).forEach((el) => el.animate([{ opacity: 0 }, { opacity: 1 }], { ...fb, delay: d0, duration: CI.LEAD }));
            by(".co-end", e.key).forEach((el) => { el.style.transformBox = "fill-box"; el.style.transformOrigin = "center"; el.animate([{ opacity: 0, transform: "scale(.6)" }, { opacity: 1, transform: "scale(1)" }], { ...fb, delay: d0, duration: 120 }); });
            if (chip) chip.animate([{ opacity: 0, translate: "0 " + (e.side === "top" ? 4 : -4) + "px" }, { opacity: 1, translate: "0 0" }], { ...fb, delay: d0 + CI.BOX_AT, duration: CI.BOX });
        });
        coCtl.querySelectorAll(".co-more").forEach((el) => el.animate([{ opacity: 0 }, { opacity: 1 }], { ...fb, delay: (n - 1) * stag + CI.BOX_AT, duration: CI.BOX }));
        c.total = (n - 1) * stag + CI.BOX_AT + CI.BOX;
        c.timers.push(setTimeout(() => { if (coIn === c) coIntroFinish(); }, c.total + 30));
        return c.total;
    }
    /** Callouts OFF: everything at once, no stagger. Boxes fade 120 ms, leaders retract 150 ms. The caller removes the layer afterwards. */
    function coOutro() {
        coIntroFinish();
        if (reduced() || !coOn) return 0;
        const o = { fill: "forwards", easing: EASE_IN };
        coWrap.querySelectorAll(".co-chip").forEach((el) => el.animate([{ opacity: 1 }, { opacity: 0 }], { ...o, duration: CI.OUT_BOX }));
        coCtl.querySelectorAll(".co-more").forEach((el) => el.animate([{ opacity: 1 }, { opacity: 0 }], { ...o, duration: CI.OUT_BOX }));
        coSvg.querySelectorAll(".co-line").forEach((el) => { el.setAttribute("pathLength", "1"); el.animate([{ strokeDasharray: "1 1", strokeDashoffset: 0 }, { strokeDasharray: "1 1", strokeDashoffset: 1 }], { ...o, duration: CI.OUT_LEAD }); });
        coSvg.querySelectorAll(".co-under,.co-end").forEach((el) => el.animate([{ opacity: 1 }, { opacity: 0 }], { ...o, duration: CI.OUT_LEAD }));
        coSvg.querySelectorAll(".co-route,.co-flow").forEach((el) => el.animate([{ opacity: 0 }, { opacity: 0 }], { ...o, duration: CI.OUT_LEAD }));
        return CI.OUT_LEAD;
    }

    /* ---- live pulses (driven by /api/events) ---- */
    const mtasks = new Set();
    function pulse(ev) {
        if (!document.body.classList.contains("test-mode")) return;
        const n = nodes.get(ev.control); if (!n) return;
        const hs = n.hs;
        if (ev.kind === "press" || ev.kind === "release") {
            const rm = reduced();
            const down = ev.kind === "press";
            n.live.getAnimations().forEach((a) => a.cancel());
            if (rm) { anim(n.live, [{ opacity: 0 }, { opacity: 1, offset: 0.5 }, { opacity: 0 }], { duration: 400 }); return; }
            if (down) {
                anim(n.live, [{ opacity: 1, strokeWidth: 1.5, offset: 0 }, { opacity: 1, strokeWidth: 3, offset: 0.23 }, { opacity: 1, strokeWidth: 1.5, offset: 0.47 }, { opacity: 0, strokeWidth: 1.5, offset: 1 }], { duration: 600, easing: EASE_OUT });
                const ring = n.live.cloneNode(); ring.setAttribute("class", "ring"); n.g.append(ring);
                const to = hs.shape === "circle" ? 1.35 : 1.18;
                const a = anim(ring, [{ transform: "scale(1)", opacity: 0.7 }, { transform: `scale(${to})`, opacity: 0 }], { duration: 520, easing: EASE_OUT });
                a.onfinish = () => ring.remove();
            } else anim(n.live, [{ opacity: 0.5, strokeWidth: 1.5 }, { opacity: 0, strokeWidth: 1.5 }], { duration: 160, easing: EASE_OUT });
            return;
        }
        if (ev.kind === "turn") {
            if (reduced()) { n.live.getAnimations().forEach((a) => a.cancel()); anim(n.live, [{ opacity: 0 }, { opacity: 1, offset: 0.5 }, { opacity: 0 }], { duration: 400 }); return; }
            const m = n.m, tps = Math.abs(ev.ticksPerSec || 0);
            m.tps = tps; m.dir = (ev.rawTicks || 1) > 0 ? 1 : -1; m.fine = !!ev.fine; m.last = performance.now();
            m.target = clamp(0.35 + (tps / 80) * 0.65, 0.35, 1); m.op = Math.max(m.op, m.target);
            m.speed = 0.15 + tps * 0.012;
            n.g.classList.add("turning"); n.comet.classList.toggle("fine", m.fine);
            n.live.getAnimations().forEach((a) => a.cancel());
            if (!mtasks.has(n)) { mtasks.add(n); loop.add(tickMotion); }
        }
    }
    function tickMotion(dt) {
        const now = performance.now();
        for (const n of Array.from(mtasks)) {
            const m = n.m;
            if (now - m.last > 120) { const f = Math.exp(-dt / 0.14); m.op *= f; m.speed *= Math.exp(-dt / 0.2); }
            m.phase = (m.phase + m.dir * m.speed * dt) % 1;
            n.live.style.opacity = String(m.op);
            n.comet.style.opacity = String(m.op * 0.9);
            n.comet.style.strokeDashoffset = String(-m.phase);
            if (m.op < 0.01) { n.live.style.opacity = "0"; n.comet.style.opacity = "0"; n.g.classList.remove("turning"); m.op = 0; mtasks.delete(n); }
        }
        return mtasks.size > 0;
    }

    /* ---- drag-to-assign preview (action library) ---- */
    let dropId = null;
    function dragOver(cx, cy, compat) {
        const zr = zone.getBoundingClientRect();
        let best = null, bd = 1e9;
        const px = cx - zr.left, py = cy - zr.top;
        for (const id of ids) {
            const b = bboxZone(id);
            const dx = Math.max(b.x - px, 0, px - (b.x + b.w)), dy = Math.max(b.y - py, 0, py - (b.y + b.h));
            const d = Math.hypot(dx, dy);
            const ok = compat(id);
            const n = nodes.get(id);
            n.g.classList.toggle("near", ok && d <= 24);
            if (ok && d <= 24 && d < bd) { bd = d; best = id; }
        }
        for (const id of ids) nodes.get(id).g.classList.toggle("drop", id === best);
        if (best !== dropId) { dropId = best; if (best) { const n = nodes.get(best); n.sel.getAnimations().forEach((a) => a.cancel()); anim(n.sel, [{ strokeDashoffset: 1, opacity: 1 }, { strokeDashoffset: 0, opacity: 1 }], { duration: reduced() ? 1 : 260, easing: EASE_OUT }); } }
        return best;
    }
    function dragEnd() { dropId = null; for (const n of nodes.values()) n.g.classList.remove("near", "drop"); }
    function centerOf(id) { const zr = zone.getBoundingClientRect(), b = bboxZone(id); return { x: zr.left + b.x + b.w / 2, y: zr.top + b.y + b.h / 2 }; }

    function refreshLabels() { for (const [id, n] of nodes) { const i = info(id); n.hit.setAttribute("aria-label", `${i.name}: ${i.action || "unassigned"}`); } }
    /** lens: map id -> "ov" (overridden here) | "chg" (changed from default) | "inh". Edge highlights only. */
    function setLens(map) {
        svg.classList.toggle("lens", !!map);
        let li = 0;
        for (const [id, n] of nodes) { const v = map ? map[id] : null; n.g.classList.toggle("lens-ov", v === "ov" || v === "chg"); n.g.classList.toggle("lens-inh", v === "inh"); if (v) n.g.style.setProperty("--li", String(li++)); else n.g.style.removeProperty("--li"); }
    }
    function ping(id) {
        const n = nodes.get(id); if (!n || reduced()) return;
        n.live.getAnimations().forEach((a) => a.cancel());
        anim(n.live, [{ opacity: 1, strokeWidth: 3 }, { opacity: 1, strokeWidth: 1.5, offset: .4 }, { opacity: 0, strokeWidth: 1.5 }], { duration: 1200, easing: EASE_OUT });
        const ring = n.live.cloneNode(); ring.setAttribute("class", "ring"); n.g.append(ring);
        const a = anim(ring, [{ transform: "scale(1)", opacity: .8 }, { transform: `scale(${n.hs.shape === "circle" ? 1.6 : 1.25})`, opacity: 0 }], { duration: 700, easing: EASE_OUT }); a.onfinish = () => ring.remove();
    }
    return {
        refreshLabels, setLens, ping, setLeds, focusGroup, get coHint() { return coHint; },
        deckBox() { const d = deck.getBoundingClientRect(), m = 10 * G.s * G.k; return { l: d.left + m, t: d.top + m, r: d.right - m, b: d.bottom - m }; }, // the visible case, in client pixels
        deckState, withoutGlide, morph, layout, introCallouts, coIntro, coOutro, coIntroFinish, setGhostItems(items) { lastCoGhost = items; }, ghostCancel() { ghostKill(GH.FAST); }, fitsCallouts, setForced(v) { if (!!v !== !!G.forced) { G.forced = !!v; layout(); } }, setSelected, setAllEdges, setCallouts, buildCallouts, pulse, dragOver, dragEnd, centerOf,
        focus(id) { nodes.get(id)?.hit.focus(); },
        firstHit() { return nodes.get(ids[0])?.hit; },
        has: (id) => nodes.has(id),
        get selected() { return selected; },
        get calloutsOn() { return coOn; },
        enter() { if (reduced()) { deck.style.opacity = "1"; return; } anim(deck, [{ opacity: 0, translate: "0 8px" }, { opacity: 1, translate: "0 0" }], { duration: 480, easing: EASE_OUT }); },
    };
}
