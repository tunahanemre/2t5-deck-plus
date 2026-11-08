// calibration.js: the Mouse calibration drawer. Every mouse target of the app with its status, a guided one-target-at-a-time
// capture (deck button, countdown or the pointer now), the picture back with a ring, confirm / redo, a dry-run test, and
// "recalibrate everything for this layout". No dialogs, no sliding highlight; every state is in the page.
import { h, icon, store, reduced } from "./util.js";
import { api, calibPreviewUrl, ApiError } from "./api.js";
import { S } from "./state.js";
import { openMenu, toast, confirmButton, switchCtl, selectField } from "./ui.js";

const KINDS = [["drag-h", "Wheel master sliders"], ["click", "Temperature and tint sliders"], ["box", "Value boxes"]];
/* status: [dot tone, word, text tone]. Not calibrated is a normal first-time state: amber dot, quiet text. Red is only for refusals and failures. */
const STATUS = { calibrated: ["ok", "Calibrated", "ok"], stale: ["warn", "Needs this layout", "warn"], needs_calibration: ["warn", "Not calibrated", "mute"], legacy: ["old", "Old style", "mute"], imported: ["ok", "Imported", "mute"], unknown: ["idle", "Not ready", "mute"] };
const cap1 = (s) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : s);
/** one naming style for every target: sentence case, "temp" spelled out */
const tname = (l) => cap1(String(l || "").toLowerCase().replace(/^temp\b/, "temperature"));
const fmtDisplay = (d) => String(d || "").replace(/(\d+)x(\d+)@(\d+)/, "$1 × $2 @$3x");
const ctxText = (cx) => (cx ? `${cx.layout ? cap1(cx.layout) + " page" : "Page unknown"} · ${cap1(String(cx.displayName || fmtDisplay(cx.display)).toLowerCase())} · window ${Math.round(cx.window.W)}\u00A0×\u00A0${Math.round(cx.window.H)}` : "");
const BLOCKING = new Set(["app_not_running", "no_window", "ambiguous_windows", "layout_unknown"]);
const METHODS = [["deck-key", "Press a deck button"], ["countdown", "5-second countdown"], ["cursor", "Capture now"]];

export function createCalibration(ctx) {
    const root = h("div", { class: "cal dr-stack gap24" });
    const C = { app: null, ov: null, err: null, mode: "list", steps: [], i: 0, scope: "", cap: null, saved: false, method: store.get("cal.method", "deck-key"), tests: {}, open: {}, busy: false, loaded: false, finish: false, testingAll: false };
    let pollT = 0, capT = 0, tickT = 0, sig = "", live = false;
    const own = () => C.app === S.app;

    /* ------------------------------------------------ data */
    async function refresh(force) {
        if (!live || !C.app) return;
        const app = C.app;
        try { const ov = await api.calib(app); if (app !== C.app) return; C.ov = ov; C.err = null; C.loaded = true; if (ov.capture && !C.cap && C.mode === "wizard") C.cap = ov.capture; }
        catch (e) { C.err = e instanceof ApiError ? e.message : "Could not read the calibration."; C.loaded = true; }
        paint(force);
    }
    function stop() { clearInterval(pollT); clearInterval(capT); clearInterval(tickT); pollT = capT = tickT = 0; }
    function startPoll() { clearInterval(pollT); pollT = setInterval(() => { if (!(C.mode === "wizard" && C.cap && C.cap.state === "waiting")) refresh(); }, 2000); }
    function dropCapture() { const c = C.cap; C.cap = null; clearInterval(capT); clearInterval(tickT); if (c && (c.state === "waiting" || c.state === "captured")) api.calibDiscard(C.app, c.id).catch(() => {}); }

    /* ------------------------------------------------ guided flow */
    async function begin(scope, only) {
        dropCapture(); C.finish = false; C.saved = false;
        try {
            if (only) { const t = C.ov.targets.find((x) => x.key === only); C.steps = [t]; C.scope = "one"; }
            else { const r = await api.calibSession(C.app, scope); C.steps = r.steps || []; C.scope = scope; if (r.problem && !C.steps.length) { C.err = r.problem.message; return paint(true); } }
        } catch (e) { toast({ msg: e.message, tone: "bad" }); return; }
        if (!C.steps.length) { toast({ msg: "Nothing to calibrate right now.", tone: "info", key: "cal" }); return; }
        C.i = 0; C.mode = "wizard"; paint(true);
    }
    function leaveWizard() { dropCapture(); C.mode = "list"; C.finish = false; C.saved = false; paint(true); refresh(true); }
    async function startCapture() {
        const t = C.steps[C.i]; if (!t) return;
        dropCapture(); C.saved = false;
        const body = { target: t.key, via: C.method, ...(C.method === "countdown" ? { delayMs: 5000 } : {}) };
        try { C.cap = await api.calibStart(C.app, body); } catch (e) { C.cap = { state: "failed", message: e.message, fix: "" }; }
        paint(true); watchCapture();
    }
    function watchCapture() {
        clearInterval(capT); clearInterval(tickT);
        if (!C.cap || C.cap.state !== "waiting") return;
        const id = C.cap.id;
        capT = setInterval(async () => {
            try {
                const c = await api.calibCapture(C.app, id);
                if (!C.cap || C.cap.id !== id) return;
                C.cap = c;
                if (c.state !== "waiting") { clearInterval(capT); clearInterval(tickT); paint(true); if (c.state === "captured" && C.auto) setTimeout(() => { if (C.cap && C.cap.id === id && C.cap.state === "captured") confirmCapture(); }, 2000); }
            } catch (e) { clearInterval(capT); C.cap = { state: "failed", message: e.message, fix: "" }; paint(true); }
        }, 400);
        if (C.cap.via === "countdown") tickT = setInterval(() => { const n = $cd(); if (n) n.textContent = String(Math.max(0, Math.ceil((C.cap.deadlineMs - Date.now()) / 1000))); }, 200);
    }
    const $cd = () => root.querySelector(".cal-count");
    async function confirmCapture() {
        const c = C.cap, t = C.steps[C.i]; if (!c || c.state !== "captured" || C.busy) return;
        C.busy = true;
        try { await api.calibConfirm(C.app, c.id); C.saved = true; C.cap = { ...c, state: "confirmed" }; toast({ msg: `Calibrated ${tname(t.label).toLowerCase()}`, undo: () => ctx.doUndo() }); }
        catch (e) { C.busy = false; toast({ msg: e.message, tone: "bad" }); return; }
        C.busy = false; paint(true);
        setTimeout(() => { if (C.mode !== "wizard") return; if (C.i + 1 < C.steps.length) { C.i++; C.cap = null; C.saved = false; paint(true); } else { C.finish = true; C.cap = null; paint(true); } refresh(); }, 650);
    }
    async function redo() { const c = C.cap; if (c) await api.calibDiscard(C.app, c.id).catch(() => {}); C.cap = null; await startCapture(); }
    function skip() { dropCapture(); if (C.i + 1 < C.steps.length) { C.i++; } else C.finish = true; paint(true); }

    /* ------------------------------------------------ tests */
    async function test(key, mode, confirm) {
        C.tests[key] = { busy: true }; paint(true);
        try { C.tests[key] = await api.calibTest(C.app, key, mode, confirm); } catch (e) { C.tests[key] = { message: e.message, found: false }; }
        paint(true); return C.tests[key];
    }
    async function testAll() {
        if (C.testingAll) return; C.testingAll = true; C.tests = {}; paint(true);
        for (const t of (C.ov?.targets || []).filter((x) => x.status === "calibrated" || x.status === "legacy" || x.status === "imported")) {
            const r = await test(t.key, "move");
            if (!r.found) break;
            await new Promise((ok) => setTimeout(ok, 700));
        }
        C.testingAll = false; paint(true);
    }
    async function clear(key, entry) {
        try { await api.calibClear(C.app, key, entry); toast({ msg: entry ? "Saved layout removed" : "Calibration cleared", undo: () => ctx.doUndo() }); refresh(true); } catch (e) { toast({ msg: e.message, tone: "bad" }); }
    }
    async function setOption(body) { try { await api.calibOptions(C.app, body); refresh(true); toast({ msg: "Saved", undo: () => ctx.doUndo(), key: "cal-opt" }); } catch (e) { toast({ msg: e.message, tone: "bad" }); } }

    /* ------------------------------------------------ small pieces */
    const dot = (st) => h("i", { class: "cal-dot " + (STATUS[st] || STATUS.unknown)[0], "aria-hidden": "true" });
    const nameOf = (id) => (S.profile?.controls || []).find((c) => c.id === id)?.label || id;
    const usedText = (t) => { const u = (t.usedBy || []).slice(0, 3).map((x) => `${String(nameOf(x.control)).replace(/\n/g, " ")} ${x.why || ""}`.trim()); const more = (t.usedBy || []).length - u.length; return u.length ? "Used by " + u.join(", ") + (more > 0 ? ` and ${more} more` : "") : ""; };
    const openSetting = (url, native) => {
        const app = window.go && window.go.main && window.go.main.App;
        if (app && app[native]) { try { app[native](); return; } catch (e) { /* fall through */ } }
        const a = h("a", { href: url, target: "_blank", rel: "noopener" }); document.body.append(a); a.click(); a.remove();
    };
    function schematic(win, pct, label, small) {
        if (!win || !pct) return null;
        const w = win.W || win.w, hgt = win.H || win.h, x = (pct.X ?? pct.x) * 100, y = (pct.Y ?? pct.y) * 100;
        return h("div", { class: "cal-schem" + (small ? " sm" : ""), role: "img", "aria-label": `${label || "The spot"} is ${Math.round(x)} percent across and ${Math.round(y)} percent down the window`, style: { aspectRatio: `${w} / ${hgt}` } }, h("i", { class: "cal-spot", style: { left: x + "%", top: y + "%" } }), small ? null : h("span", { class: "cal-wl" }, `${Math.round(w)} × ${Math.round(hgt)}`));
    }

    /* ------------------------------------------------ list view */
    function banner(ov) {
        const kids = [];
        if (ov.problem) kids.push(h("div", { class: "cal-note bad", role: "alert" }, icon("circle-alert", 16), h("div", {}, h("b", {}, ov.problem.message), ov.problem.fix ? h("p", {}, ov.problem.fix) : null)));
        const p = ov.permissions || {};
        if (p.accessibility === false) kids.push(permRow("Accessibility is off: nothing can be clicked until it is switched on.", p.accessibilityUrl, "OpenAccessibilitySettings"));
        if (p.screenRecording === false) kids.push(permRow("Screen Recording is off: the layout check and the pictures are not available.", p.screenRecordingUrl, "OpenScreenRecordingSettings"));
        for (const n of (ov.notes || []).filter((x) => !/^(Accessibility|Screen Recording) is off/.test(x))) kids.push(h("div", { class: "cal-note", role: "note" }, icon("info", 16), h("div", {}, n)));
        return kids;
    }
    const permRow = (txt, url, native) => h("div", { class: "cal-note warn", role: "status" }, icon("shield-alert", 16), h("div", {}, txt), h("span", { class: "cal-acts" },
        h("button", { type: "button", class: "btn sm", onclick: () => openSetting(url, native) }, icon("external-link"), "Open the setting"), h("button", { type: "button", class: "btn ghost sm", onclick: () => refresh(true) }, "Check again")));

    function row(t, ov, blocked) {
        const open = C.open[t.key], tr = C.tests[t.key], [, word, tone] = STATUS[t.status] || STATUS.unknown;
        const act = h("span", { class: "cal-acts" },
            h("button", { type: "button", class: "btn sm" + (t.status === "calibrated" ? " ghost" : ""), disabled: blocked, onclick: () => begin("", t.key) }, t.status === "calibrated" ? "Recalibrate" : "Calibrate"),
            h("button", { type: "button", class: "btn ghost sm", disabled: !!(tr && tr.busy), onclick: () => test(t.key, "move") }, "Test"),
            h("button", { type: "button", class: "icon-btn xs", "aria-label": `More for ${tname(t.label)}`, "aria-expanded": String(!!open), "aria-haspopup": "menu", "data-tip": "More", onclick: (e) => openMenu({ anchor: e.currentTarget, menu: true, minWidth: 220, ariaLabel: tname(t.label), groups: [{ items: [{ value: "layouts", label: `Saved layouts (${(t.entries || []).length})`, icon: "layers" }, { value: "adv", label: "Advanced: one real click…", icon: "mouse-pointer-click" }] }], onPick: (v) => { C.open[t.key] = C.open[t.key] === v ? null : v; paint(true); } }) }, icon("ellipsis")));
        const body = h("div", { class: "cal-row-b" },
            h("div", { class: "cal-row-t" }, dot(t.status), h("span", { class: "cal-label" }, tname(t.label))),
            h("div", { class: "cal-sub " + tone, ...(t.statusText ? { "data-tip": cap1(t.statusText) } : {}) }, t.status === "unknown" && ov.problem ? "Not ready" : word),
            usedText(t) ? h("div", { class: "cal-sub dim one", "data-tip": usedText(t) }, usedText(t)) : null);
        const el = h("div", { class: "cal-row", "data-key": t.key }, body, act);
        const extra = [];
        if (tr && !tr.busy) extra.push(testResult(t, tr));
        if (tr && tr.busy) extra.push(h("div", { class: "cal-sub" }, "Moving the pointer…"));
        if (open === "layouts") extra.push(layouts(t));
        if (open === "adv") extra.push(advanced(t, ov));
        return h("div", { class: "cal-item" }, el, ...extra);
    }
    function testResult(t, r) {
        const tone = r.found ? (r.warn || !r.moved ? "warn" : "ok") : "bad";
        return h("div", { class: "cal-test " + tone, role: "status" }, h("div", { class: "cal-test-t" }, icon(r.found ? "circle-check" : "circle-alert", 16), h("span", {}, r.message || (r.found ? "Found." : "Not found."))),
            r.warn ? h("p", {}, r.warn) : null,
            r.needsCalibration ? h("button", { type: "button", class: "btn sm neutral-primary", onclick: () => begin("", t.key) }, "Recalibrate") : null);
    }
    function layouts(t) {
        const es = t.entries || [];
        return h("div", { class: "cal-disc" }, es.length ? h("ul", { class: "cal-entries" }, ...es.map((e) => h("li", {}, h("span", {}, `${e.layout ? cap1(e.layout) + " page" : "Page unknown"} · ${fmtDisplay(e.display)} · window ${Math.round(e.winW)} × ${Math.round(e.winH)} · ${new Date(e.at).toLocaleDateString()}`), e.current ? h("span", { class: "chip st-set" }, "this one") : null,
            h("button", { type: "button", class: "btn ghost sm", onclick: () => clear(t.key, e.id) }, "Remove")))) : h("p", { class: "muted" }, "Nothing saved for this target yet."),
            es.length > 1 ? confirmButton({ label: "Clear every saved layout", confirmLabel: "Confirm clear", ico: "trash-2", onConfirm: () => clear(t.key) }) : null);
    }
    function advanced(t, ov) {
        let ok = false;
        const why = !ov.armed ? "Arm the deck first." : !ov.app.frontmost ? `${ov.app.name} is not the front app.` : ov.permissions?.accessibility === false ? "Accessibility is off." : "";
        const go = h("button", { type: "button", class: "btn sm", disabled: true, onclick: () => test(t.key, "click", true) }, "Send one real click");
        const box = h("input", { type: "checkbox", id: "cal-adv-" + t.key, disabled: !!why, onchange: (e) => { ok = e.target.checked; go.disabled = !ok || !!why; } });
        return h("div", { class: "cal-disc" }, h("p", { class: "muted" }, "Sends one real click at this spot. Use it only when the app is on the right page."), h("label", { class: "cal-check", for: box.id }, box, h("span", {}, `${ov.app.name} is on the right page: click once`)), why ? h("p", { class: "muted" }, why) : null, go);
    }
    function listView() {
        const ov = C.ov;
        if (C.err && !ov) return [h("div", { class: "cal-note bad", role: "alert" }, icon("circle-alert", 16), h("div", {}, C.err))];
        if (!ov) return [h("div", { class: "cal-skel" })];
        const sum = ov.summary || {}, blocked = !!(ov.problem && BLOCKING.has(ov.problem.code));
        const need = (sum.needs || 0) + (sum.stale || 0), unk = sum.unknown || 0;
        const out = [];
        out.push(h("div", { class: "cal-ctx", "data-tip": "What the calibration is checked against: the screen, the layout and the window size" }, icon("monitor", 16), h("span", {}, ov.context ? ctxText(ov.context) : "No window found")));
        out.push(...banner(ov));
        out.push(h("div", { class: "cal-pills" }, pill("ok", sum.calibrated || 0, "calibrated"), pill("warn", sum.stale || 0, "need this layout"), pill("warn", sum.needs || 0, "not calibrated"), unk ? pill("idle", unk, "not ready") : null, sum.legacy ? pill("old", sum.legacy, "old style") : null, sum.imported ? pill("ok", sum.imported, "imported from ld-resolve") : null));
        out.push(h("div", { class: "cal-main" },
            need + unk > 0 ? h("button", { type: "button", class: "btn neutral-primary", disabled: blocked, onclick: () => begin("missing") }, `Calibrate ${need + unk} missing`) : h("div", { class: "cal-ok" }, icon("circle-check", 16), "Everything is calibrated for this layout."),
            h("div", { class: "cal-quiet" },
                h("button", { type: "button", class: "btn ghost sm", disabled: blocked, onclick: () => begin("layout") }, "Recalibrate all for this layout"), h("span", { class: "cal-sepdot", "aria-hidden": "true" }, "·"),
                h("button", { type: "button", class: "btn ghost sm", disabled: blocked || C.testingAll || !(sum.calibrated || sum.legacy || sum.imported), onclick: testAll }, C.testingAll ? "Testing…" : "Test all"))));
        if ((ov.blocked || []).length) out.push(h("section", { class: "pf-sec" }, h("h3", {}, "Recent refusals"), h("p", { class: "muted" }, "Times the deck did not click because the spot could not be trusted."),
            ...ov.blocked.slice(0, 4).map((b) => h("div", { class: "cal-ref" }, h("div", {}, h("b", {}, `${new Date(b.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })} ${b.label}`), h("p", {}, b.message)), h("button", { type: "button", class: "btn sm", disabled: blocked, onclick: () => begin("", b.target) }, "Calibrate now")))));
        const allOk = (ov.targets || []).every((t) => t.status === "calibrated");
        for (const [kind, title] of KINDS) {
            const ts = (ov.targets || []).filter((t) => t.kind === kind); if (!ts.length) continue;
            const key = "grp:" + kind, collapsed = C.open[key] === undefined ? allOk : !C.open[key];
            out.push(h("section", { class: "cal-grp" }, h("button", { type: "button", class: "cal-gh", "aria-expanded": String(!collapsed), onclick: () => { C.open[key] = collapsed; paint(true); } }, icon(collapsed ? "chevron-right" : "chevron-down", 16), h("span", {}, title), h("span", { class: "cal-gc" }, `${ts.filter((t) => t.status === "calibrated").length} of ${ts.length}`)),
                ...(collapsed ? [] : ts.map((t) => row(t, ov, blocked)))));
        }
        const sel = selectField({ options: [{ value: "block", label: "Refuse the click if the screen looks different (recommended)" }, { value: "warn", label: "Warn only" }, { value: "off", label: "Off" }], value: ov.settings?.verify || "block", ariaLabel: "Layout check", onChange: (v) => setOption({ verify: v }) });
        const sw = switchCtl({ checked: !!ov.settings?.blockLegacy, label: "Refuse old-style points", text: "Refuse old-style points", onChange: (v) => setOption({ blockLegacy: v }) });
        out.push(h("section", { class: "pf-sec" }, h("h3", {}, "Layout check"), sel.el, h("div", { class: "cal-sw" }, sw.el), h("p", { class: "muted" }, "Old-style points are absolute screen positions saved before this tool existed. They do not follow the window.")));
        return out;
    }
    const pill = (tone, n, txt) => h("span", { class: "cal-pill " + tone }, dot(tone === "ok" ? "calibrated" : tone === "warn" ? "stale" : tone === "old" ? "legacy" : "unknown"), h("b", {}, String(n)), ` ${txt}`);

    /* ------------------------------------------------ wizard */
    function wizard() {
        const ov = C.ov, t = C.steps[C.i], n = C.steps.length;
        if (C.finish) return [h("div", { class: "cal-done" }, h("div", { class: "cal-big-ok" }, icon("circle-check", 32)), h("h3", {}, "All set"), h("p", { class: "muted" }, `Calibrated for ${ctxText(ov?.context) || "this layout"}.`),
            h("div", { class: "cal-main2" }, h("button", { type: "button", class: "btn", onclick: () => { leaveWizard(); setTimeout(testAll, 150); } }, "Test all (dry run)"), h("button", { type: "button", class: "btn neutral-primary", onclick: leaveWizard }, "Done")))];
        if (!t) return [h("p", { class: "muted" }, "Nothing to do."), h("button", { type: "button", class: "btn", onclick: leaveWizard }, "Back")];
        const c = C.cap;
        const out = [h("div", { class: "cal-step" }, h("span", {}, n > 1 ? `Step ${C.i + 1} of ${n}` : "One target"), h("button", { type: "button", class: "btn ghost sm", onclick: leaveWizard }, "Cancel")),
            h("div", { class: "cal-prog", role: "progressbar", "aria-valuemin": "0", "aria-valuemax": String(n), "aria-valuenow": String(C.i + (C.saved ? 1 : 0)) }, h("i", { style: { width: ((C.i + (C.saved ? 1 : 0)) / n) * 100 + "%" } })),
            h("h3", { class: "cal-h" }, tname(t.label))];
        if (ov?.problem && BLOCKING.has(ov.problem.code)) out.push(h("div", { class: "cal-note bad", role: "alert" }, icon("circle-alert", 16), h("div", {}, h("b", {}, ov.problem.message), ov.problem.fix ? h("p", {}, ov.problem.fix) : null)));
        out.push(h("p", { class: "cal-instr" }, t.instruction));
        if (!c) {
            out.push(methodPicker());
            out.push(h("button", { type: "button", class: "btn neutral-primary cal-go", disabled: !!(ov?.problem && BLOCKING.has(ov.problem.code)), onclick: startCapture }, C.method === "deck-key" ? "Start, then press a deck button" : C.method === "countdown" ? "Start the 5-second countdown" : "Capture the pointer now"));
            out.push(h("p", { class: "muted" }, C.method === "deck-key" ? "Hover the spot in " + (ov?.app?.name || "the app") + ", then press any deck button. Your hands stay on the deck." : C.method === "countdown" ? `After you start, switch to ${ov?.app?.name || "the app"} and hover the spot.` : "Only works when the pointer is already over the spot."));
        } else if (c.state === "waiting") {
            out.push(h("div", { class: "cal-wait", role: "status" }, h("div", { class: "cal-ring pulse" }, c.via === "countdown" ? h("span", { class: "cal-count" }, String(Math.max(0, Math.ceil((c.deadlineMs - Date.now()) / 1000)))) : icon("crosshair", 32)),
                h("p", { class: "cal-instr" }, c.via === "countdown" ? `Switch to ${ov?.app?.name || "the app"} and hover it…` : `Now hover it in ${ov?.app?.name || "the app"} and press any deck button.`),
                h("button", { type: "button", class: "btn ghost sm", onclick: () => { dropCapture(); paint(true); } }, "Cancel this capture")));
        } else if (c.state === "captured" || c.state === "confirmed") {
            out.push(h("div", { class: "cal-cap" }, c.hasPreview ? h("figure", { class: "cal-fig" }, h("img", { src: calibPreviewUrl(C.app, c.id), alt: "The screen around the spot that was read, with a ring on it", width: 360, height: 360 }), h("figcaption", {}, `Is the ring on the ${tname(t.label).toLowerCase()}?`)) : h("p", { class: "cal-instr" }, `Is this the ${tname(t.label).toLowerCase()}?`),
                h("div", { class: "cal-where" }, schematic(c.marker?.window, c.marker?.pct, tname(t.label), true), h("span", {}, ctxText(ov?.context) || c.context || "")),
                c.replaces > 0 ? h("p", { class: "muted" }, "This replaces your earlier calibration for this window size.") : null,
                C.saved ? h("div", { class: "cal-saved", role: "status" }, icon("circle-check", 16), "Saved", h("button", { type: "button", class: "btn ghost sm", onclick: () => test(t.key, "move") }, "Test this one")) : h("div", { class: "cal-main2" },
                    h("button", { type: "button", class: "btn neutral-primary", "data-kbd": "Enter", onclick: confirmCapture }, "Yes, save"), h("button", { type: "button", class: "btn", onclick: redo }, "Redo ", h("kbd", { class: "cap sm" }, "R"))),
                C.saved ? null : h("label", { class: "cal-check" }, (() => { const i = h("input", { type: "checkbox", checked: !!C.auto, onchange: (e) => { C.auto = e.target.checked; } }); return i; })(), h("span", {}, "Save automatically after 2 seconds"))));
            if (C.tests[t.key] && C.saved && !C.tests[t.key].busy) out.push(testResult(t, C.tests[t.key]));
        } else if (c.state === "failed" || c.state === "expired" || c.state === "discarded") {
            out.push(h("div", { class: "cal-note bad", role: "alert" }, icon("circle-alert", 16), h("div", {}, h("b", {}, c.message || (c.state === "expired" ? "That took too long." : "The capture was cancelled.")), c.fix ? h("p", {}, c.fix) : null)),
                h("div", { class: "cal-main2" }, h("button", { type: "button", class: "btn neutral-primary", onclick: startCapture }, "Try again"), n > 1 ? h("button", { type: "button", class: "btn", onclick: skip }, "Skip this one") : null));
        }
        return out;
    }
    function methodPicker() {
        return h("div", { class: "cal-seg", role: "radiogroup", "aria-label": "How to capture" }, ...METHODS.map(([v, label]) => h("button", { type: "button", class: "cal-seg-b", role: "radio", "aria-checked": String(C.method === v), tabindex: C.method === v ? "0" : "-1",
            onclick: () => { C.method = v; store.set("cal.method", v); paint(true); },
            onkeydown: (e) => { const d = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0; if (!d) return; e.preventDefault(); const i = (METHODS.findIndex((m) => m[0] === C.method) + d + METHODS.length) % METHODS.length; C.method = METHODS[i][0]; store.set("cal.method", C.method); paint(true); root.querySelectorAll(".cal-seg-b")[i]?.focus(); } }, label)));
    }

    /* ------------------------------------------------ paint */
    function paint(force) {
        if (!own()) return;
        const s = JSON.stringify([C.mode, C.i, C.finish, C.saved, C.method, C.auto, C.open, C.tests, C.cap && [C.cap.id, C.cap.state], C.steps.length, C.testingAll, C.err, C.ov && { ...C.ov, capture: null }]);
        if (!force && s === sig) return; sig = s;
        const focusKey = document.activeElement?.closest?.(".cal-row,.cal-gh") ? document.activeElement.className : "";
        const sc = root.closest(".dr-body"), top = sc ? sc.scrollTop : 0;
        root.replaceChildren(...(C.mode === "wizard" ? wizard() : listView()));
        if (sc) sc.scrollTop = top;
        if (C.mode === "wizard") (root.querySelector(".cal-go, .btn.neutral-primary, .cal-seg-b[aria-checked=true]") || null)?.focus?.({ preventScroll: true });
        void focusKey;
    }
    root.addEventListener("keydown", (e) => {
        if (C.mode !== "wizard" || e.target.matches?.("input,select")) return;
        if (e.key === "Enter" && C.cap && C.cap.state === "captured" && !C.saved && e.target.tagName !== "BUTTON") { e.preventDefault(); confirmCapture(); }
        else if ((e.key === "r" || e.key === "R") && C.cap && C.cap.state === "captured" && !C.saved) { e.preventDefault(); redo(); }
        else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); leaveWizard(); }
    });

    /* ------------------------------------------------ drawer hook */
    return {
        /** drawer definition (drawers.js calls this on open and on rebuild; the body element is reused so polling state survives a rebuild) */
        def() {
            if (C.app !== S.app) { dropCapture(); Object.assign(C, { app: S.app, ov: null, err: null, mode: "list", steps: [], i: 0, cap: null, saved: false, tests: {}, open: {}, finish: false, loaded: false }); sig = ""; }
            live = true; refresh(true); startPoll();
            const nm = (S.apps.find((a) => a.id === S.app) || {}).name || "this app";
            return { icon: "mouse-pointer-click", title: "Mouse calibration", subtitle: `Spots the deck clicks in ${nm}`, body: (() => { const nn = ctx.needsNote && ctx.needsNote("calibration"); return nn ? h("div", { class: "dr-stack gap24" }, nn, root) : root; })(), onClose: () => { live = false; stop(); if (C.mode === "wizard") { C.mode = "list"; } dropCapture(); } };
        },
        has: (id) => !!(ctx.calApps && ctx.calApps[id]),
        scrollTo(key) { C.open["grp:" + (C.ov?.targets.find((t) => t.key === key)?.kind || "")] = true; setTimeout(() => root.querySelector(`.cal-row[data-key="${key}"]`)?.scrollIntoView({ block: "center" }), 50); },
    };
}
