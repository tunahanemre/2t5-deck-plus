// PROVENANCE: copied 2026-11-03 from the DECKMAP app's frontend/src/deckmap.js (DECKMAP is unchanged).
// Everything above the '2T5 DECK+ EDITOR EXTENSIONS' marker is the DECKMAP original; the extensions add live-test overlays.
// deckmap.js — pure rendering module. Knows how to draw a device.Profile as
// an SVG map and how to update the visual state of individual controls. It
// never calls the Wails bindings and never fetches anything itself — main.js
// owns data and orchestration, this module only draws what it's told.
//
// Nothing about a specific device is hardcoded here: every position, shape,
// and label comes from the Profile passed into createDeckMap. A second
// device profile renders correctly without touching this file.

const SVG_NS = "http://www.w3.org/2000/svg";

/**
 * "BeforeAfter" -> "Before After", "ClrBW" -> "Clr BW", "P1btn" -> "P1btn"
 * (no split point). Purely a display nicety over the engraved label text —
 * the underlying label string from the profile is never modified.
 */
function splitLabel(label) {
    return String(label || "").replace(/([a-z0-9])([A-Z])/g, "$1 $2");
}

function el(tag, attrs) {
    const node = document.createElementNS(SVG_NS, tag);
    if (attrs) {
        for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
    }
    return node;
}

/** Renders `text` into `parent` as one or more centered tspans. */
function renderLines(parent, text, cx, baseY, fontSize, lineHeight, cls) {
    const words = splitLabel(text).split(" ").filter(Boolean);
    const lines = words.length > 1 ? words : [text];
    const totalH = (lines.length - 1) * lineHeight;
    const startY = baseY - totalH / 2;
    lines.forEach((line, i) => {
        const t = el("text", {
            x: cx,
            y: startY + i * lineHeight,
            "font-size": fontSize,
            class: cls,
        });
        t.textContent = line;
        parent.appendChild(t);
    });
}

// Resolves a control's shape node + label size given its Rect and Kind.
// Falls back defensively: unrecognized `shape` strings fall back to
// `kind`-derived defaults rather than breaking the render, per docs/api.md's
// guidance that the backend makes no promises about the exact shape set.
function shapeKindFor(rect, kind) {
    const shape = (rect.shape || "").toLowerCase();
    if (shape === "circle" || shape === "rect" || shape === "bar") return shape;
    console.warn(`deckmap: unrecognized layout shape %o for kind %o — falling back`, rect.shape, kind);
    if (kind === "knob" || kind === "encoderPress") return "circle";
    if (kind === "slider") return "bar";
    return "rect";
}

/**
 * Builds the SVG deck and returns a handle with imperative update methods.
 * `onSelect(controlId)` fires on click/Enter/Space of a control.
 */
export function createDeckMap(container, profile, { onSelect } = {}) {
    const { canvas, rects } = profile.layout;
    const svg = el("svg", {
        class: "deck-svg",
        viewBox: `0 0 ${canvas.w} ${canvas.h}`,
        role: "group",
        "aria-label": `${profile.identity.name} control map`,
    });

    svg.appendChild(
        el("rect", { class: "deck-plate", x: 0, y: 0, width: canvas.w, height: canvas.h, rx: 24 })
    );

    /** @type {Map<string, {group:SVGElement, shape:SVGElement, cx:number, cy:number, rect:object, control:object}>} */
    const nodes = new Map();

    for (const control of profile.controls) {
        const rect = rects[control.id];
        if (!rect) continue; // profile.go's own load-time validation guarantees this never happens
        const kind = shapeKindFor(rect, control.kind);
        const cx = rect.x + rect.w / 2;
        const cy = rect.y + rect.h / 2;

        const group = el("g", {
            class: "ctrl state-unbound",
            "data-control-id": control.id,
            tabindex: "0",
            role: "button",
            "aria-label": control.label || control.id,
        });

        let shape;
        if (kind === "circle") {
            shape = el("circle", { class: "ctrl-shape", cx, cy, r: Math.min(rect.w, rect.h) / 2 });
        } else if (kind === "bar") {
            shape = el("rect", {
                class: "ctrl-shape",
                x: rect.x,
                y: rect.y,
                width: rect.w,
                height: rect.h,
                rx: rect.w / 2,
            });
        } else {
            shape = el("rect", {
                class: "ctrl-shape",
                x: rect.x,
                y: rect.y,
                width: rect.w,
                height: rect.h,
                rx: 8,
            });
        }
        group.appendChild(shape);

        // Slider bars: label above, key line below (there's no room inside
        // a 34px-wide bar). Everything else: label/key centered inside.
        const labelGroup = el("g", { class: "ctrl-text" });
        group.appendChild(labelGroup);

        const title = el("title");
        title.textContent = control.label || control.id;
        group.appendChild(title);

        group.addEventListener("click", () => onSelect && onSelect(control.id));
        group.addEventListener("keydown", (e) => {
            if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                onSelect && onSelect(control.id);
            }
        });

        svg.appendChild(group);
        nodes.set(control.id, { group, shape, labelGroup, cx, cy, rect, control, kind });
    }

    container.replaceChildren(svg);

    // ---- text layout for one control given its current bound state ----
    function layoutText(node, state) {
        const { labelGroup, rect, control, kind, cx, cy } = node;
        labelGroup.replaceChildren();

        const isSmall = Math.min(rect.w, rect.h) <= 40;
        const hasKey = !!(state.keyText && (state.status === "own" || state.status === "inherited"));

        if (kind === "bar") {
            renderLines(labelGroup, control.label, cx, rect.y - 10, 11, 12, "ctrl-label");
            if (hasKey) {
                renderLines(labelGroup, state.keyText, cx, rect.y + rect.h + 16, 10, 11, "ctrl-key");
            }
            return;
        }

        if (isSmall || !hasKey) {
            const fontSize = isSmall ? 8.5 : 11;
            renderLines(labelGroup, control.label, cx, cy, fontSize, fontSize + 2, "ctrl-label");
            return;
        }

        // Bigger shape, bound: label above center, key below center.
        renderLines(labelGroup, control.label, cx, cy - Math.min(rect.w, rect.h) * 0.16, 10.5, 12, "ctrl-label");
        renderLines(labelGroup, state.keyText, cx, cy + Math.min(rect.w, rect.h) * 0.22, 10, 11, "ctrl-key");
    }

    return {
        svg,
        controlIds() {
            return Array.from(nodes.keys());
        },

        /**
         * Sets one control's visual state.
         * state: { status: "own"|"inherited"|"silenced"|"unbound", keyText?: string }
         */
        setState(controlId, state) {
            const node = nodes.get(controlId);
            if (!node) return;
            node.group.classList.remove("state-own", "state-inherited", "state-silenced", "state-unbound");
            node.group.classList.add(`state-${state.status}`);
            layoutText(node, state);
        },

        setSelected(controlId) {
            for (const [id, node] of nodes) {
                node.group.classList.toggle("is-selected", id === controlId);
            }
        },

        clearSelection() {
            for (const [, node] of nodes) node.group.classList.remove("is-selected");
        },

        /** Briefly flashes a control to mark a live hardware event. */
        flash(controlId) {
            const node = nodes.get(controlId);
            if (!node) return;
            node.group.classList.remove("is-live");
            // Force reflow so a re-trigger while still-flashing restarts the animation.
            void node.group.getBoundingClientRect();
            node.group.classList.add("is-live");
            window.clearTimeout(node._flashTimer);
            node._flashTimer = window.setTimeout(() => node.group.classList.remove("is-live"), 520);
        },
    };
}


/* ======================================================================
   2T5 DECK+ EDITOR EXTENSIONS (not in DECKMAP)
   decorateDeck(handle, profile) adds, per control, a live-test overlay:
   knob tick + pulse ring, button lamp, slider thumb, and a "G" tag shown
   when the assignment is inherited from Global. Animation runs in one rAF
   loop; prefers-reduced-motion drops spin/pulse and keeps a simple flash.
   ====================================================================== */
export function decorateDeck(handle, profile) {
    const rects = profile.layout.rects;
    const reduce = window.matchMedia && matchMedia("(prefers-reduced-motion: reduce)").matches;
    const live = new Map(); // id -> {angle, glow, lamp, pulses[], thumb, kind, parts}

    for (const control of profile.controls) {
        const r = rects[control.id];
        const group = handle.svg.querySelector(`[data-control-id="${CSS.escape(control.id)}"]`);
        if (!r || !group) continue;
        const cx = r.x + r.w / 2, cy = r.y + r.h / 2;
        const rad = Math.min(r.w, r.h) / 2;
        const parts = { group, cx, cy, rad, r };
        const over = el("g", { class: "ctrl-live", "pointer-events": "none" });

        // G tag (inherited from Global)
        const g = el("g", { class: "g-tag" });
        g.appendChild(el("circle", { cx: r.x + r.w - 2, cy: r.y + 2, r: 7 }));
        const gt = el("text", { x: r.x + r.w - 2, y: r.y + 2.5, "font-size": 8.5 });
        gt.textContent = "G";
        g.appendChild(gt);
        over.appendChild(g);

        if (control.kind === "knob" || control.kind === "encoderPress") {
            parts.ring = el("circle", { class: "pulse-ring", cx, cy, r: rad, opacity: 0 });
            parts.tick = el("line", { class: "knob-tick", x1: cx, y1: cy - rad * 0.38, x2: cx, y2: cy - rad * 0.92 });
            over.append(parts.ring, parts.tick);
        } else if (control.kind === "slider") {
            parts.thumb = el("rect", { class: "slider-thumb", x: r.x - 3, y: r.y + r.h / 2 - 5, width: r.w + 6, height: 10, rx: 4 });
            over.appendChild(parts.thumb);
        }
        group.appendChild(over);
        live.set(control.id, { ...parts, kind: control.kind, angle: 0, glow: 0, lamp: false, pos: 0.5, pulses: [] });
    }

    let raf = 0, last = 0;
    function frame(t) {
        const dt = Math.min(0.05, (t - (last || t)) / 1000);
        last = t;
        let busy = false;
        for (const s of live.values()) {
            if (s.tick) {
                s.glow = Math.max(0, s.glow - dt * 1.6);
                s.tick.setAttribute("transform", `rotate(${s.angle} ${s.cx} ${s.cy})`);
                s.tick.style.opacity = String(0.35 + 0.65 * s.glow);
                if (s.glow > 0) busy = true;
            }
            if (s.ring) {
                const p = s.pulses;
                for (let i = p.length - 1; i >= 0; i--) {
                    p[i].age += dt * 2.2;
                    if (p[i].age >= 1) p.splice(i, 1);
                }
                const top = p[p.length - 1];
                if (top) {
                    s.ring.setAttribute("r", String(s.rad + (4 + top.size * 22) * top.age));
                    s.ring.setAttribute("opacity", String((1 - top.age) * 0.85));
                    s.ring.setAttribute("stroke-width", String(1 + top.size * 3));
                    busy = true;
                } else s.ring.setAttribute("opacity", "0");
            }
        }
        raf = busy ? requestAnimationFrame(frame) : 0;
        if (!busy) last = 0;
    }
    const kick = () => { if (!raf) raf = requestAnimationFrame(frame); };

    return {
        /** turn: signed ticks, ticksPerSec drives the pulse ring size. */
        turn(id, ticks, tps) {
            const s = live.get(id);
            if (!s) return;
            handle.flash(id);
            if (reduce) return;
            if (s.tick) {
                s.angle += ticks * 14;
                s.glow = 1;
                s.pulses.push({ age: 0, size: Math.min(1, (tps || 0) / 60) });
                if (s.pulses.length > 4) s.pulses.shift();
                kick();
            }
            if (s.thumb) this.slider(id, Math.max(0, Math.min(1, s.pos + ticks / 127)));
        },
        press(id, down) {
            const s = live.get(id);
            if (!s) return;
            s.group.classList.toggle("is-down", !!down);
            if (down) handle.flash(id);
        },
        slider(id, pos) {
            const s = live.get(id);
            if (!s || !s.thumb) return;
            s.pos = pos;
            s.thumb.setAttribute("y", String(s.r.y + (1 - pos) * (s.r.h - 10)));
            s.group.classList.add("is-live");
            clearTimeout(s._t);
            s._t = setTimeout(() => s.group.classList.remove("is-live"), 400);
        },
        setInheritedHidden(b) { handle.svg.classList.toggle("hide-inherited", !!b); },
    };
}
