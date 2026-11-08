// drawers.js: right drawers (Action library, Sensitivity, Advanced, History, App profile, Import/export/backup)
// and the bottom Live test drawer. One right drawer at a time; the live drawer may coexist with one.
import { h, $, icon, clamp, fmt, reduced, anim, EASE_OUT, EASE_IN, willChange, debounce, springTo, springSet } from "./util.js";
import { S, GLOBAL, eff, ctlMeta, actionDef, appName, appInfo, isGlobalApp, visiblePacks, PACK_ICON, actionIcon, compat, compatLabel, groupFields } from "./state.js";
import { HOT } from "./hotspots.js";
import { api, iconUrl, ApiError } from "./api.js";
import { isResolve, bankName } from "./state.js";
import { fieldRow, curveCard, perClick, sensPresets } from "./inspector.js";
import { plainCommand, plain, plainControl, plainChange, noteRestore, restoreApp } from "./plain.js";
import { pendingBody } from "./pending.js";
import { createCalibration } from "./calibration.js";
import { riskOf, riskChip, riskSheet } from "./risk.js";
import { depsBody } from "./deps.js";
import { aboutBody } from "./about.js";
import { appTile, textField, confirmButton, segmented, selectField, switchCtl, stateChip, toast, KIND, closeAllPopovers, stepper } from "./ui.js";

const SPRING_SNAP = "linear(0,.054,.179,.33,.482,.619,.734,.825,.894,.943,.977,.998,1.01,1.016,1.018,1.017,1.015,1.012,1.009,1.007,1.005,1.003,1)";
let aiSt = null; // last GET /api/ai-access
let aboutSt = null; // { info } once GET /api/about answered (info null = this host has none)
let hudSt = null; // { rid, on, err }: the hudWithoutResolve setting of the Resolve preset
const rel = (t) => { const d = Math.max(0, (Date.now() - t) / 1000); return d < 10 ? "just now" : d < 60 ? `${Math.round(d)} s ago` : d < 3600 ? `${Math.round(d / 60)} min ago` : d < 86400 ? `${Math.round(d / 3600)} h ago` : `${Math.round(d / 86400)} d ago`; };

export function createDrawers(ctx) {
    ctx.calibration = createCalibration(ctx);
    const body = $("#body"), live = $("#live");
    const shell = h("aside", { class: "drawer", id: "drawer", hidden: true });
    const title = h("h2", { class: "dr-title", id: "dr-title", tabindex: "-1" });
    const sub = h("p", { class: "dr-sub" });
    const act = h("div", { class: "dr-act" });
    const closeBtn = h("button", { type: "button", class: "icon-btn", "data-tip": "Close", "data-kbd": "Esc", "aria-label": "Close drawer", onclick: () => close() }, icon("x", 20));
    const icoBox = h("span", { class: "dr-ico" });
    const head = h("header", { class: "dr-head" }, icoBox, h("div", { class: "dr-titles" }, title, sub), act, closeBtn);
    const bodyEl = h("div", { class: "dr-body" });
    const foot = h("footer", { class: "dr-foot", hidden: true });
    const region = h("div", { class: "sr", role: "status", "aria-live": "polite" });
    shell.append(head, bodyEl, foot, region);
    const scrim = h("div", { class: "scrim", id: "scrim", hidden: true, onclick: () => close() });
    body.append(scrim, shell);
    bodyEl.addEventListener("scroll", () => head.classList.toggle("scrolled", bodyEl.scrollTop > 2), { passive: true });

    let cur = null, opener = null, closing = false;
    const modal = () => !!(cur && cur.def.modal);

    /* ------------------------------------------------ open / swap / close */
    function fill(def) {
        icoBox.replaceChildren(def.iconNode || icon(def.icon, 24));
        title.textContent = def.title; title.setAttribute("data-tip", def.title); sub.textContent = def.subtitle || ""; sub.hidden = !def.subtitle;
        act.replaceChildren(...(def.action ? [def.action] : []));
        bodyEl.replaceChildren(def.body);
        foot.replaceChildren(...(def.footer ? [].concat(def.footer) : [])); foot.hidden = !def.footer;
        shell.setAttribute("role", def.modal ? "dialog" : "complementary");
        if (def.modal) shell.setAttribute("aria-modal", "true"); else shell.removeAttribute("aria-modal");
        // the panel is named "<title> panel": the top-bar button that opens it keeps the bare name, so no two things share one accessible name
        shell.removeAttribute("aria-labelledby"); shell.setAttribute("aria-label", `${def.title} panel`);
        bodyEl.scrollTop = 0; head.classList.remove("scrolled");
    }
    function setInert(on) {
        for (const sel of [".top", ".stage"]) { const e = $(sel); if (e) { if (on) e.setAttribute("inert", ""); else e.removeAttribute("inert"); } }
    }
    function open(name, from) {
        const build = DEFS[name]; if (!build) return;
        closeAllPopovers();
        const def = build();
        if (!def) return;
        if (cur && cur.name === name) { title.focus(); return; }
        const swapping = !!cur && !closing, wasClosing = closing;
        opener = swapping ? opener : (from || document.activeElement);
        const prevModal = modal();
        cur = { name, def }; closing = false;
        ctx.onDrawerChange(name);
        if (swapping) {
            const outA = anim(bodyEl, [{ opacity: 1, transform: "none" }, { opacity: 0, transform: "translateX(8px)" }], { duration: reduced() ? 100 : 140, easing: EASE_IN, fill: "forwards" });
            const go = () => {
                bodyEl.getAnimations().forEach((x) => x.cancel());
                fill(def);
                anim(bodyEl, [{ opacity: 0, transform: "translateX(24px)" }, { opacity: 1, transform: "none" }], { duration: reduced() ? 120 : 220, easing: EASE_OUT });
                afterOpen(def);
            };
            if (outA) outA.onfinish = go; else go();
            if (prevModal && !def.modal) { scrim.hidden = true; setInert(false); }
            if (!prevModal && def.modal) showScrim();
            return;
        }
        fill(def);
        shell.getAnimations().forEach((x) => x.cancel()); scrim.getAnimations().forEach((x) => x.cancel());
        shell.hidden = false;
        /* the drawer rides one interruptible spring: opened while it is still closing, it just turns around with its velocity */
        if (reduced()) { springSet(shell, { tx: 0 }); shell.style.opacity = "1"; anim(shell, [{ opacity: 0 }, { opacity: 1 }], { duration: 120 }); }
        else { if (!wasClosing) springSet(shell, { tx: shell.offsetWidth || 360 }); springTo(shell, { tx: 0 }, "panel"); anim(bodyEl, [{ opacity: 0, transform: "translateY(4px)" }, { opacity: 1, transform: "none" }], { duration: 140, delay: 60, easing: EASE_OUT }); }
        if (def.modal) showScrim();
        afterOpen(def);
    }
    function showScrim() { scrim.hidden = false; setInert(true); anim(scrim, [{ opacity: 0 }, { opacity: 1 }], { duration: 220, easing: EASE_OUT }); }
    function afterOpen(def) {
        region.textContent = def.title + " panel";
        (def.focus ? def.focus() : null) || title.focus({ preventScroll: true });
    }
    function close(opts = {}) {
        if (!cur || closing) return;
        closing = true;
        if (ctx.onDrawerClosing) ctx.onDrawerClosing(); // the inspector space starts closing with the drawer, not after it
        const def = cur.def, wasModal = def.modal;
        if (def.onClose) def.onClose();
        const done = () => {
            shell.hidden = true; closing = false; cur = null; scrim.hidden = true; setInert(false);
            ctx.onDrawerChange(null);
            if (!opts.noFocus && opener && document.contains(opener) && !opener.closest("[inert]")) opener.focus({ preventScroll: true });
            opener = null;
        };
        if (wasModal) anim(scrim, [{ opacity: 1 }, { opacity: 0 }], { duration: 165, easing: EASE_IN, fill: "forwards" });
        if (reduced()) { const a = anim(shell, [{ opacity: 1 }, { opacity: 0 }], { duration: 100, fill: "forwards" }); if (a) a.onfinish = () => { shell.getAnimations().forEach((x) => x.cancel()); shell.style.opacity = ""; done(); }; else done(); }
        else springTo(shell, { tx: (shell.offsetWidth || 360) + 24 }, "panel", done);
    }
    /** rebuild the current drawer in place (selection or data changed) */
    function rebuild() {
        if (!cur || closing) return;
        const def = DEFS[cur.name](); if (!def) { close({ noFocus: true }); return; }
        const sc = bodyEl.scrollTop; cur.def = def; fill(def); bodyEl.scrollTop = sc;
    }
    /* focus trap for modal drawers */
    document.addEventListener("keydown", (e) => {
        if (!modal() || e.key !== "Tab") return;
        const f = Array.from(shell.querySelectorAll("button,input,[tabindex]:not([tabindex='-1']),select,a[href]")).filter((x) => !x.disabled && !x.hidden && x.offsetParent !== null);
        if (!f.length) return;
        const first = f[0], last = f[f.length - 1];
        if (e.shiftKey && (document.activeElement === first || document.activeElement === title)) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    });

    /* ------------------------------------------------ live (bottom) drawer */
    let liveOpen = false, liveOpener = null, liveClosing = false;
    const liveHead = h("header", { class: "lv-head" }, icon("gauge", 20, "lv-ic"), h("h2", { class: "lv-title", id: "lv-title", tabindex: "-1" }, "Live test"), h("span", { class: "lv-hint", "data-tip": "Presses don\u2019t fire while this window is in front. Switch to another app to use the deck." }, "Presses don\u2019t fire while this window is in front.", h("span", { class: "lv-hint2" }, " Switch to another app to use the deck.")),
        h("button", { type: "button", class: "icon-btn pin-btn", id: "pinBtn", "aria-pressed": "false", "data-tip": "Pin the inspector to this control", "aria-label": "Pin the inspector to this control", onclick: (e) => { const on = e.currentTarget.getAttribute("aria-pressed") !== "true"; e.currentTarget.setAttribute("aria-pressed", String(on)); e.currentTarget.firstChild.firstChild.setAttribute("href", on ? "#i-pin" : "#i-pin-off"); S.pinned = on; e.currentTarget.setAttribute("data-tip", on ? "Unpin: follow the deck again" : "Pin the inspector to this control"); e.currentTarget.setAttribute("aria-label", on ? "Unpin the inspector" : "Pin the inspector to this control"); ctx.setPinned(on); } }, icon("pin-off", 20)),
        h("button", { type: "button", class: "icon-btn", "data-tip": "Close", "data-kbd": "Esc", "aria-label": "Close live test", onclick: () => closeLive() }, icon("x", 20)));
    live.append(liveHead, ctx.dock.el);
    live.setAttribute("role", "complementary"); live.setAttribute("aria-labelledby", "lv-title");
    const liveH = () => parseInt(getComputedStyle(live).height, 10) || 200;
    function openLive(from) {
        if (liveOpen) return;
        liveOpen = true; liveOpener = from || document.activeElement;
        live.getAnimations().forEach((x) => x.cancel());
        const wasClosing = liveClosing; liveClosing = false;
        live.hidden = false; ctx.dock.visible(true);
        if (reduced()) { springSet(live, { ty: 0 }); anim(live, [{ opacity: 0 }, { opacity: 1 }], { duration: 120 }); }
        else { if (!wasClosing) springSet(live, { ty: liveH() + 24 }); springTo(live, { ty: 0 }, "panel"); }
        ctx.onLiveChange(true, liveH() + 16);
        $("#lv-title").focus({ preventScroll: true });
    }
    function closeLive(opts = {}) {
        if (!liveOpen || liveClosing) return;
        liveClosing = true; liveOpen = false;
        ctx.onLiveChange(false, 0);
        const done = () => { if (!liveClosing) return; live.hidden = true; liveClosing = false; ctx.dock.visible(false); if (!opts.noFocus && liveOpener && document.contains(liveOpener) && !liveOpener.closest("[inert]")) liveOpener.focus({ preventScroll: true }); liveOpener = null; };
        if (reduced()) { const a = anim(live, [{ opacity: 1 }, { opacity: 0 }], { duration: 100, fill: "forwards" }); if (a) a.onfinish = () => { live.getAnimations().forEach((x) => x.cancel()); done(); }; else done(); }
        else springTo(live, { ty: liveH() + 24 }, "panel", done);
    }

    /* ------------------------------------------------ drawer definitions */
    const sub1 = () => { const m = ctlMeta(S.control); return m ? m.label || S.control : ""; };
    const DEFS = {
        help() {
            const sec = (t, ...kids) => [h("h3", {}, t), ...kids];
            const body = h("div", { class: "help" },
                ...sec("Layers and modifiers", h("p", {}, "A bank is one full set of assignments for the deck. Layers are banks you switch to (Layer 2, Layer 3); modifier banks apply while you hold a key (Shift, Cmd...). Use the tabs above the deck or [ and ].")),
                ...sec("Inherit, Custom, Off", h("ul", {}, h("li", {}, "Inherit Global: the control uses what Global says."), h("li", {}, "Custom: this app sets its own action."), h("li", {}, "Off here: the control does nothing in this app, even if Global sets it."))),
                ...sec("Test mode", h("p", {}, "Press Test, then turn or press the real deck to see what it does. While this window is in front the deck is paused on purpose, so tests never trigger real actions.")),
                ...sec("Armed and Disarmed", h("p", {}, "Armed means the deck fires its actions. Disarmed holds back keys, mouse, scroll, commands and Resolve writes. Click the status pill at the top to switch.")),
                ...sec("Where backups live", h("p", {}, "Presets are backed up automatically before imports, restores and updates. Open Import, export, backup from the ... menu. A restore always saves a safety copy first and is one undo step.")),
                ...sec("Shortcuts", h("p", {}, "Press ? for the full list. Cmd+K opens every command.")));
            return { icon: "circle-help", title: "Help", subtitle: "A short guide", body };
        },
        library() {
            const search = h("input", { type: "text", class: "m-search-in", placeholder: "Search actions", "aria-label": "Search actions", autocomplete: "off", spellcheck: "false" });
            const sbox = h("div", { class: "m-search flat" }, icon("search"), search);
            const list = h("div", { class: "lib" });
            const bodyBox = h("div", { class: "lib-wrap" }, sbox, list);
            const footCap = h("div", { class: "lib-cap" });
            const ck = () => ctlMeta(S.control)?.kind;
            const captionFill = () => { footCap.replaceChildren(...(S.control ? ["Assign to ", h("b", {}, plainControl(ctlMeta(S.control)?.label || S.control))] : ["Select a control on the deck, or drag an action onto it"])); };
            let q = "";
            const build = () => {
                list.replaceChildren();
                const e = eff(); let shown = 0;
                for (const pk of visiblePacks()) {
                    const items = pk.actions.filter((a) => !q || (a.label + " " + (a.help || "") + " " + pk.label).toLowerCase().includes(q.toLowerCase()));
                    if (!items.length) continue;
                    list.append(h("div", { class: "m-grp" }, icon(PACK_ICON[pk.id] || "shapes"), h("span", {}, pk.label)));
                    for (const a of items) {
                        const ok = !S.control || compat(a, ck());
                        const why = ok ? "" : a.kind === "turn" ? `Needs a knob or dial, not a ${ck() === "button" ? "key" : "key or button"}` : `Needs a key, not a ${ck() === "knob" ? "knob" : "knob or dial"}`; // from the action's fit: why this row is grey
                        const row = h("button", { type: "button", class: "lib-row" + (e && e.action === a.id ? " cur" : "") + (ok ? "" : " dim"), "data-act": a.id, "data-tip": ok ? (a.help ? a.help : null) : why + (a.help ? ". " + a.help : ""), "aria-label": `${a.label}. ${S.control ? (ok ? "Assign to " + S.control : why) : "Drag onto a control"}` },
                            icon("grip-vertical", 16, "grip"), icon(actionIcon(a.id), 16, "lib-ic"),
                            h("span", { class: "m-txt" }, h("span", { class: "m-name" }, a.label), a.help ? h("span", { class: "m-sub" }, a.help) : null),
                            h("span", { class: "lib-chip chip" }, compatLabel(a)));
                        wireRow(row, a);
                        list.append(row); shown++;
                    }
                }
                if (!shown) list.append(h("div", { class: "m-empty" }, icon("search-x", 32), h("div", {}, "No actions match ", h("b", {}, `“${q}”`)), h("button", { type: "button", class: "btn ghost sm", onclick: () => { search.value = ""; q = ""; build(); search.focus(); } }, "Clear search")));
            };
            function wireRow(row, a) {
                let sx = 0, sy = 0, dragging = false, chip = null, pid = null, over = null;
                const ok = (id) => compat(a, ctlMeta(id)?.kind);
                const cancel = () => { if (!dragging) return; dragging = false; ctx.stage.dragEnd(); document.body.classList.remove("dragging"); if (chip) { const r = row.getBoundingClientRect(); const b = chip.getBoundingClientRect(); const dx = r.left + 24 - b.left, dy = r.top + r.height / 2 - b.top - b.height / 2; const an = reduced() ? anim(chip, [{ opacity: 1 }, { opacity: 0 }], { duration: 120 }) : anim(chip, [{ transform: chip.style.transform }, { transform: `translate(${parseFloat(chip.dataset.x) + dx}px, ${parseFloat(chip.dataset.y) + dy}px) scale(1)`, opacity: 0.2 }], { duration: 350, easing: SPRING_SNAP }); const c = chip; chip = null; if (an) an.onfinish = () => c.remove(); else c.remove(); } document.removeEventListener("keydown", onEsc, true); };
                const onEsc = (e) => { if (e.key === "Escape") { e.stopPropagation(); e.preventDefault(); cancel(); } };
                row.addEventListener("pointerdown", (e) => { if (e.button !== 0) return; sx = e.clientX; sy = e.clientY; pid = e.pointerId; dragging = false; row.setPointerCapture(pid); });
                row.addEventListener("pointermove", (e) => {
                    if (pid == null) return;
                    if (!dragging && Math.hypot(e.clientX - sx, e.clientY - sy) > 4) {
                        dragging = true; document.body.classList.add("dragging"); document.addEventListener("keydown", onEsc, true);
                        chip = h("div", { class: "drag-chip" }, icon(actionIcon(a.id), 16), h("span", {}, a.label)); document.getElementById("portal").append(chip);
                        if (!reduced()) anim(chip, [{ scale: "1" }, { scale: "1.03" }], { duration: 140, easing: EASE_OUT });
                    }
                    if (dragging && chip) {
                        const x = e.clientX - 20, y = e.clientY - 18; chip.dataset.x = x; chip.dataset.y = y; chip.style.transform = `translate(${x}px, ${y}px)`;
                        over = ctx.stage.dragOver(e.clientX, e.clientY, ok);
                        chip.classList.toggle("bad", !over && !!document.elementFromPoint(e.clientX, e.clientY)?.closest?.(".stage"));
                    }
                });
                const up = (e) => {
                    if (pid == null) return;
                    try { row.releasePointerCapture(pid); } catch (er) { /* already released */ }
                    pid = null;
                    if (dragging) {
                        const target = over; over = null;
                        if (target) {
                            document.removeEventListener("keydown", onEsc, true); document.body.classList.remove("dragging"); dragging = false;
                            const c = ctx.stage.centerOf(target), b = chip.getBoundingClientRect();
                            const an = reduced() ? anim(chip, [{ opacity: 1 }, { opacity: 0 }], { duration: 120 }) : anim(chip, [{ transform: chip.style.transform, opacity: 1 }, { transform: `translate(${c.x - b.width / 2}px, ${c.y - b.height / 2}px) scale(.6)`, opacity: 0 }], { duration: 140, easing: EASE_OUT });
                            const cc = chip; chip = null; if (an) an.onfinish = () => cc.remove(); else cc.remove();
                            ctx.stage.dragEnd();
                            ctx.assignTo(target, a.id);
                        } else cancel();
                        row.dataset.swallow = "1"; setTimeout(() => delete row.dataset.swallow, 0);
                    }
                };
                row.addEventListener("pointerup", up); row.addEventListener("pointercancel", up);
                row.addEventListener("click", () => {
                    if (row.dataset.swallow) return;
                    if (!S.control) { toast({ msg: "Select a control on the deck first, or drag this action onto one.", tone: "info" }); return; }
                    if (!compat(a, ck())) { toast({ msg: `${a.label} does not fit ${S.control}.`, tone: "info" }); return; }
                    row.classList.remove("flash"); void row.offsetWidth; row.classList.add("flash");
                    ctx.assignTo(S.control, a.id);
                });
                row.addEventListener("keydown", (e) => {
                    const rows = Array.from(list.querySelectorAll(".lib-row")), i = rows.indexOf(row);
                    if (e.key === "ArrowDown") { e.preventDefault(); rows[Math.min(rows.length - 1, i + 1)].focus(); }
                    else if (e.key === "ArrowUp") { e.preventDefault(); if (i === 0) search.focus(); else rows[i - 1].focus(); }
                });
            }
            search.addEventListener("input", () => { q = search.value; build(); });
            search.addEventListener("keydown", (e) => { if (e.key === "ArrowDown") { e.preventDefault(); list.querySelector(".lib-row")?.focus(); } else if (e.key === "Escape" && q) { e.stopPropagation(); search.value = ""; q = ""; build(); } });
            shell.addEventListener("keydown", (e) => { if (e.key === "/" && cur?.name === "library" && document.activeElement !== search) { e.preventDefault(); search.focus(); } });
            build(); captionFill();
            return { icon: "library", title: "Action library", subtitle: `${appName(S.app)} · ${visiblePacks().length} pack${visiblePacks().length === 1 ? "" : "s"}`, body: bodyBox, footer: footCap, focus: () => { search.focus({ preventScroll: true }); return true; } };
        },
        sens() {
            const e = eff(); const a = e && actionDef(e.action); if (!a) return null;
            const g = groupFields(a); if (!g.sens.length) return null;
            const dctx = { ...ctx, rows: ctx.drows }; ctx.drows.length = 0;
            const card = curveCard(ctx); ctx.curve = card;
            const wrap = h("div", { class: "dr-stack" }, card.el);
            const ro = e.source === "global" && !isGlobalApp();
            // one readout: what one raw click does, in the action's real unit
            const num = h("b", {}), unitEl = h("span", { class: "u" }), fineEl = h("small", {});
            const readout = h("div", { class: "perclick", role: "status" }, h("span", {}, "per click ="), h("span", {}, num, " ", unitEl));
            const valsNow = () => Object.fromEntries(Object.entries(eff()?.fields || {}).map(([k, x]) => [k, S.draft[k] ?? x.value]));
            const paintReadout = () => { const pc = perClick(a, valsNow()); readout.hidden = !pc; if (!pc) return; num.textContent = (pc.val > 0 ? "" : pc.val < 0 ? "\u2212" : "") + fmt(Math.abs(pc.val), 3); unitEl.textContent = pc.unit + (pc.note ? " (" + pc.note + ")" : ""); paintPresets(); };
            wrap.append(readout);
            // Coarse / Normal / Fine: multiples of the shipped default, one history step each
            const pr = sensPresets(a, {}); let chips = null;
            const setNow = async (key, v) => { try { if (v == null) await ctx.resetValue(key); else await ctx.saveNow(key, v); } catch (er) { ctx.fail(er); } };
            const paintPresets = () => { if (!chips || !pr) return; const cur = valsNow()[pr.key]; chips.forEach(([k, b]) => { const tv = pr[k]; b.setAttribute("aria-pressed", String(cur != null && Math.abs(cur - tv) < 1e-6)); }); };
            if (pr && !ro) {
                const mk = (k, label) => [k, h("button", { type: "button", class: "btn", "aria-pressed": "false", "data-tip": k === "normal" ? "The shipped default" : k === "coarse" ? "Twice the default" : "Half the default", onclick: () => setNow(pr.key, k === "normal" ? null : pr[k]) }, label)];
                chips = [mk("coarse", "Coarse"), mk("normal", "Normal"), mk("fine", "Fine")];
                wrap.append(h("div", { class: "presets", role: "group", "aria-label": "Sensitivity preset" }, ...chips.map((c) => c[1])));
            }
            card.onUpdate = paintReadout;
            for (const f of g.sens) wrap.append(fieldRow(f, e.fields[f.key] || { value: f.default, source: "default" }, dctx, "full", ro));
            // Resolve: how much fine mode scales the response
            if (isResolve() && S.ep.settings !== false) {
                const cur = S.pr?.raw?.settings?.fineFactor, v0 = cur != null ? cur : 0.5; let tm = 0;
                const errBox = h("div", { class: "fr-err", role: "alert", hidden: true });
                const st = stepper({ value: v0, min: 0.05, max: 1, step: 0.05, label: "Fine mode multiplier", onChange: (v) => { clearTimeout(tm); tm = setTimeout(async () => { try { await api.setting(S.app, "fineFactor", v); S.ep.settings = true; errBox.hidden = true; await ctx.reloadAll(); toast({ msg: `Fine mode \u00d7 ${v}`, undo: () => ctx.doUndo(), key: "fine" }); } catch (er) { if (er.status === 404) { S.ep.settings = false; rebuild(); } else if (er.status === 400) { errBox.replaceChildren(icon("triangle-alert"), h("span", {}, String(er.message).replace(/\bfineFactor\b/g, "Fine mode multiplier"))); errBox.hidden = false; st.input.blur(); st.set(cur != null ? cur : v0); st.setError(true); } else ctx.fail(er); } }, 300); } });
                wrap.append(h("div", { class: "setrow" }, h("div", { class: "lab" }, h("span", {}, "Fine mode"), h("span", { class: "unit" }, "\u00d7 while fine mode is held")), st.el, errBox, h("p", { class: "muted" }, cur != null ? "Set for this app." : "Using the engine default.")));
            }
            paintReadout(); card.update();
            return { icon: "spline", title: "Response curve", subtitle: sub1(), body: wrap, onClose: () => { card.destroy(); ctx.curve = null; } };
        },
        adv() {
            const e = eff(); const a = e && actionDef(e.action); if (!a) return null;
            const g = groupFields(a); if (!g.adv.length) return null;
            const dctx = { ...ctx, rows: ctx.drows }; ctx.drows.length = 0;
            const wrap = h("div", { class: "dr-stack" });
            const ro = e.source === "global" && !isGlobalApp();
            const rows = g.adv.map((f) => ({ f, el: fieldRow(f, e.fields[f.key] || { value: f.default, source: "default" }, dctx, "full", ro) }));
            let sInput = null;
            if (rows.length > 8) {
                sInput = h("input", { type: "text", class: "m-search-in", placeholder: "Search values", "aria-label": "Search values", autocomplete: "off" });
                wrap.append(h("div", { class: "m-search flat" }, icon("search"), sInput));
                sInput.addEventListener("input", () => { const q = sInput.value.toLowerCase(); rows.forEach((r) => { r.el.hidden = !!q && !(r.f.label + r.f.key).toLowerCase().includes(q); }); });
            }
            rows.forEach((r) => wrap.append(r.el));
            return { icon: "settings-2", title: "Advanced", subtitle: sub1(), body: wrap };
        },
        history() {
            if (S.ep.history === false) return null;
            const wrap = h("div", { class: "hist" });
            const undoB = h("button", { type: "button", class: "btn", onclick: () => ctx.doUndo() }, icon("undo-2"), "Undo");
            const redoB = h("button", { type: "button", class: "btn", onclick: () => ctx.doRedo() }, icon("redo-2"), "Redo");
            const load = async () => {
                try {
                    const r = await api.history(); S.ep.history = true;
                    const cur = r.current ?? r.pos ?? 0, es = (r.entries || []).slice().sort((a, b) => b.index - a.index);
                    wrap.replaceChildren();
                    if (es.length <= 1) { wrap.append(h("div", { class: "empty sm" }, icon("history", 32, "em-ic"), h("h2", {}, "Nothing to undo yet"), h("p", {}, "Changes you make will appear here."))); }
                    else for (const en of es) {
                        const isCur = en.index === cur, future = en.index > cur;
                        const row = h("button", { type: "button", class: "hist-row" + (isCur ? " cur" : "") + (future ? " future" : ""), disabled: isCur, "aria-current": isCur ? "true" : null, onclick: () => goto(en, cur) },
                            h("i", { class: "hist-dot" }), h("span", { class: "hist-l" }, plainChange(en.label, (id) => plainControl(ctlMeta(id)?.label || id), /^Restore defaults/.test(en.label) ? restoreApp(en.t) && appName(restoreApp(en.t)) : undefined)), h("span", { class: "hist-t" }, en.index === 0 ? "" : rel(en.t)));
                        wrap.append(row);
                    }
                    undoB.disabled = cur <= 0; redoB.disabled = !(r.entries || []).some((x) => x.index > cur);
                } catch (er) { if (er instanceof ApiError && er.status === 404) { S.ep.history = false; ctx.epChanged(); close(); } else wrap.replaceChildren(h("div", { class: "fr-err" }, icon("triangle-alert"), er.message)); }
            };
            async function goto(en, cur) {
                try { await api.jump(en.index); const n = Math.abs(cur - en.index); toast({ msg: `Went back ${n} change${n === 1 ? "" : "s"}`.replace("back", en.index > cur ? "forward" : "back"), undo: () => api.jump(cur).then(() => ctx.reloadAll()) }); await ctx.reloadAll(); load(); }
                catch (er) { toast({ msg: er.message, tone: "bad" }); }
            }
            load();
            return { icon: "history", title: "History", subtitle: "Newest first", body: wrap, footer: [undoB, redoB], reload: load };
        },
        profile() {
            const app = appInfo(S.app); if (!app) return null;
            const wrap = h("div", { class: "dr-stack gap24" });
            // 1 packs
            const DESC = { generic: "Keys, scroll, shell commands and layers.", resolve: "Scrubbing, markers, colour wheels, clip selection.", browser: "Scroll, tabs, zoom and history.", finder: "Finder tags.", global: "Everyday computer actions." };
            const packs = h("section", { class: "pf-sec" }, h("h3", {}, "Action packs"));
            const have = new Set(app.packs || []);
            for (const pk of S.packs.filter((p) => !p.always)) { // an always-on pack (Global) has no switch
                const sw = switchCtl({ checked: have.has(pk.id), label: pk.label, onChange: async (on) => {
                    const next = S.packs.map((p) => p.id).filter((id) => (id === pk.id ? on : have.has(id)));
                    try { await api.packs(S.app, next); await ctx.reloadAll(); toast({ msg: `${pk.label} ${on ? "enabled" : "disabled"} for ${appName(S.app)}`, undo: () => ctx.doUndo() }); if (cur?.name === "profile") rebuild(); }
                    catch (er) { sw.set(!on); ctx.fail(er); }
                } });
                packs.append(h("div", { class: "pf-row" }, icon(PACK_ICON[pk.id] || "shapes", 16, "pf-ic"), h("div", { class: "pf-txt" }, h("b", {}, pk.label), h("span", {}, DESC[pk.id] || "")), sw.el));
            }
            wrap.append(packs);
            // 2 copy from
            if (S.ep.copy !== false && isResolve()) {
                // the engine always refuses a copy into or out of Resolve (its banks are its own): say so, calmly, instead of offering it
                wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "Copy from another app"), h("p", { class: "muted pf-na" }, "Not available for DaVinci Resolve: it uses its own banks, so assignments can\u2019t be copied between Resolve and other apps.")));
            } else if (S.ep.copy !== false) {
                const isRes = (a) => String(a.id).includes("DaVinciResolve") || (a.packs || []).includes("resolve");
                const others = S.apps.filter((a) => a.id !== S.app && !isRes(a));
                let from = others[0]?.id, scope = "bank";
                const sel = selectField({ options: others.map((a) => ({ value: a.id, label: a.isGlobal ? "Global" : a.name })), value: from, ariaLabel: "Copy from", onChange: (v) => { from = v; } });
                const seg = segmented({ options: [{ value: "bank", label: "This bank" }, { value: "all", label: "All banks" }], value: scope, label: "Scope", onChange: (v) => { scope = v; } });
                const prev = h("div", { class: "cp-prev", hidden: true });
                const apply = async () => {
                    try { const r = await api.copyFrom(S.app, { from, scope, bank: S.bank, mode: "merge" }); S.ep.copy = true; prev.hidden = true; await ctx.reloadAll(); const sk = (r.skipped || []).length; toast({ msg: `Copied ${r.copied} assignment${r.copied === 1 ? "" : "s"} from ${appName(from)}${sk ? `, ${sk} skipped` : ""}`, undo: () => ctx.doUndo() }); }
                    catch (er) { if (er.status === 404) { S.ep.copy = false; rebuild(); } else toast({ msg: er.message, tone: "bad" }); }
                };
                const go = h("button", { type: "button", class: "btn", onclick: async () => {
                    if (!from) return;
                    try {
                        const r = await api.copyFrom(S.app, { from, scope, bank: S.bank, mode: "merge", dryRun: true });
                        if (!r.dryRun) { await apply(); return; } // an older engine without a dry run: apply as before
                        const sk = r.skipped || [], reasons = Object.entries(r.skippedByReason || {}).map(([k, n]) => `${n} ${plain(k)}`).join(", ");
                        prev.hidden = false;
                        prev.replaceChildren(h("p", {}, r.copied ? `Copies ${r.copied} assignment${r.copied === 1 ? "" : "s"} from ${appName(from)}${r.removed ? `, replaces ${r.removed}` : ""}${sk.length ? `, skips ${sk.length}${reasons ? ` (${reasons})` : ""}` : ""}.` : `Nothing to copy${sk.length ? `: ${sk.length} skipped${reasons ? ` (${reasons})` : ""}` : ""}.`),
                            h("div", { class: "cp-act" }, h("button", { type: "button", class: "btn text", onclick: () => { prev.hidden = true; } }, "Cancel"), r.copied ? h("button", { type: "button", class: "btn neutral-primary", onclick: apply }, `Copy ${r.copied}`) : null));
                    } catch (er) { if (er.status === 404) { S.ep.copy = false; rebuild(); } else toast({ msg: er.message, tone: "bad" }); }
                } }, icon("copy"), "Review copy");
                wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "Copy from another app"), others.length ? [h("p", { class: "muted" }, "Merges another app\u2019s assignments into this one. You review the result before anything changes."), sel.el, seg.el, go, prev] : h("p", { class: "muted" }, "No other apps yet.")));
            }
            // 2b standard preset: the app started from a shipped template. Compare shows what differs; Reset to standard is the restore below.
            const std = S.std && S.std.app === app.id ? S.std : null;
            if (std) {
                const out = h("div", { class: "std-diff", hidden: true, role: "region", "aria-label": "Differences from the standard" });
                const FL = { key: "Shortcut", label: "Label", holdMs: "Hold", sens: "Sensitivity", maxDrag: "Max drag", action: "Action", modifier: "Held key", layer: "Layer", wheel: "Wheel", property: "Property", param: "Parameter", cmd: "Command" };
                const val = (v) => (v == null || v === "" ? "empty" : typeof v === "object" ? JSON.stringify(v) : String(v));
                const paintDiff = (r) => {
                    const c = r.counts || {}, rows = (r.controls || []);
                    const head = h("p", { class: "muted" }, rows.length ? `${c.changed || 0} changed \u00b7 ${c.added || 0} added \u00b7 ${c.removed || 0} removed \u00b7 ${c.unchanged || 0} same as the standard` : "Identical to the standard. Nothing differs.");
                    const list = h("div", { class: "std-rows" }, ...rows.map((x) => {
                        const nm = plainControl(ctlMeta(x.control)?.label || x.control), pg = x.bank && x.bank !== "base" ? bankName(x.bank) : "";
                        const lines = x.change === "removed" ? [h("span", { class: "std-l" }, "Not assigned here. The standard has ", h("b", {}, (x.fields || []).find((f) => f.key === "label")?.seed || x.seedAction || "an action"))]
                            : x.change === "added" ? [h("span", { class: "std-l" }, "Added by you. The standard leaves it empty.")]
                            : (x.fields || []).filter((f) => f.key !== "type" && !(f.key === "action" && f.change === "added")).map((f) => h("span", { class: "std-l" }, `${FL[f.key] || f.key}: `, h("span", { class: "std-was" }, val(f.seed)), " \u2192 ", h("b", {}, val(f.current))));
                        return h("div", { class: "std-row" }, h("div", { class: "std-c" }, h("kbd", { class: "cap sm word" }, nm), pg ? h("span", { class: "std-pg" }, pg) : null), h("div", { class: "std-ls" }, ...lines));
                    }));
                    out.replaceChildren(head, ...(rows.length ? [list] : []));
                };
                const cmp = h("button", { type: "button", class: "btn", "aria-expanded": "false", onclick: async () => {
                    const on = out.hidden; out.hidden = !on; cmp.setAttribute("aria-expanded", String(on)); cmp.lastChild.textContent = on ? "Hide differences" : "Compare";
                    if (on) { out.replaceChildren(h("p", { class: "muted" }, "Comparing\u2026")); try { paintDiff(await api.diff(app.id)); } catch (er) { out.replaceChildren(h("p", { class: "muted" }, "Could not read the standard right now.")); } }
                } }, icon("git-compare"), h("span", {}, "Compare"));
                wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "Standard preset"), h("p", { class: "muted" }, `This app started from the ${std.name} standard.`), cmp, out));
                if (S.autoCompare) { S.autoCompare = false; setTimeout(() => cmp.click(), 0); }
            }
            // 3 restore defaults. Resolve restores from the live file and a template-made app carries its templateId; for any other app the engine tells (a 400) once, and the button then stays off for that app for the session.
            const canRestore = () => isResolve() || !!(S.pr && S.pr.raw && S.pr.raw.templateId) || S.restored.has(S.app) || !S.noRestore.has(S.app);
            const noRestore = h("p", { class: "muted", role: "status", hidden: true }, "Nothing to restore to: this app was not made from a standard preset.");
            const rbtn = confirmButton({ label: S.std && S.std.app === S.app ? "Reset to standard" : "Restore defaults", confirmLabel: S.std && S.std.app === S.app ? "Confirm reset" : "Confirm restore", ico: "rotate-ccw", cls: "btn", onConfirm: async () => {
                    try {
                        const r = await api.resetDefaults(S.app); noteRestore(S.app); S.restored.add(S.app); S.noRestore.delete(S.app); S.ep.reset = true; await ctx.reloadAll(); noRestore.hidden = true; paintRestore();
                        const rt = r && r.restoredTo, tn = rt && rt.templateName;
                        toast({ msg: tn && rt.source === "template-guess" ? `Restored ${tn} (best match)` : tn && rt.source === "template" ? `Restored ${tn} template` : `Restored ${appName(S.app)} defaults`, undo: () => ctx.doUndo() });
                    }
                    catch (er) {
                        if (er.status === 404) { S.ep.reset = false; rebuild(); }
                        else if (er.status === 400 && ((er.data && er.data.reason) === "no-shipped-preset" || /no shipped preset/i.test(er.message || ""))) { S.noRestore.add(S.app); paintRestore(); } // a calm line, not an error toast
                        else toast({ msg: er.message, tone: "bad" });
                    }
                } });
            const paintRestore = () => { const ok = canRestore(); rbtn.setAttribute("aria-disabled", String(!ok)); rbtn.classList.toggle("is-off", !ok); noRestore.hidden = ok; };
            paintRestore();
            { const app0 = S.app; // no template on the preset and not Resolve: the defaults diff says whether the engine ships a seed for this app (one read, remembered)
              if (!isResolve() && !(S.pr && S.pr.raw && S.pr.raw.templateId) && !S.noRestore.has(app0) && !S.seeded.has(app0)) api.diff(app0).then((d) => { if (d && d.hasSeed === false) S.noRestore.add(app0); else S.seeded.add(app0); if (S.app === app0) paintRestore(); }).catch(() => {}); }
            if (S.ep.reset !== false && !isGlobalApp()) wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, std ? "Reset to standard" : "Restore defaults"), h("p", { class: "muted" }, std ? `Returns every control to the ${std.name} standard. One undo step brings your version back.` : "Returns every control of this app to the shipped preset."), noRestore, rbtn));
            // remove app (two-step, never a browser dialog)
            if (!isGlobalApp() && S.ep.remove !== false) wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "Remove app"), h("p", { class: "muted" }, `Deletes ${app.name}\u2019s preset from the deck. Undo brings it back.`),
                confirmButton({ label: "Remove app", confirmLabel: "Confirm remove", ico: "trash-2", cls: "btn danger-text", onConfirm: async () => {
                    try { const id = app.id, nm = app.name; await api.removeApp(id); S.ep.remove = true; close({ noFocus: true }); await ctx.loadApps(); ctx.renderAppPicker(); await ctx.selectApp(S.apps.find((a) => a.isGlobal)?.id || S.apps[0].id); toast({ msg: `Removed ${nm}`, undo: () => ctx.doUndo() }); }
                    catch (er) { if (er.status === 404 && /route|not found/i.test(er.message || "")) { S.ep.remove = false; rebuild(); } else toast({ msg: er.message, tone: "bad" }); }
                } })));
            // 4 info
            const copyId = h("button", { type: "button", class: "icon-btn", "data-tip": "Copy bundle id", "aria-label": "Copy bundle id", onclick: async () => { try { await navigator.clipboard.writeText(app.id); toast({ msg: "Bundle id copied", tone: "info" }); } catch (e) { toast({ msg: "Copy is not available here.", tone: "info" }); } } }, icon("copy"));
            wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "App info"), h("div", { class: "pf-lab" }, "App ID (advanced)"), h("div", { class: "pf-id" }, h("code", {}, app.id), copyId), h("p", { class: "muted wrap" }, `Preset file: presets/${app.id}.json`)));
            return { icon: "app-window", iconNode: appTile(app, { size: 28, url: iconUrl(app.id) }), title: app.isGlobal ? "Global" : app.name, subtitle: "App profile", modal: true, body: wrap };
        },
        external() {
            const list = S.pending || [];
            const after = async (msg) => { await ctx.loadPending(); await ctx.reloadAll(); toast({ msg, tone: "info", undo: /Applied/.test(msg) ? () => ctx.doUndo() : undefined }); };
            return { icon: "triangle-alert", title: "Outside changes", subtitle: list.length ? `${list.length} waiting for review` : "Nothing waiting", modal: true, body: pendingBody(list, { done: after, fail: (er) => { if (er.status === 404) { ctx.loadPending(); toast({ msg: "That change was already settled.", tone: "info" }); } else toast({ msg: er.message, tone: "bad" }); } }) };
        },
        calibration() { return ctx.calibration.def(); },
        /* Requirements: not modal (nothing is inert, no focus trap): it can stay open beside the deck while you fix things in System Settings */
        setup() {
            if (S.ep.deps === false) return null;
            const st = ctx.depsState();
            return { icon: "plug", title: "Requirements", subtitle: "What this Mac needs for the deck", body: depsBody({ items: st.items, error: st.error, busy: st.busy, onFix: ctx.depsFix, onRecheck: () => ctx.depsLoad(true) }) };
        },
        /* About 2T5 DECK+: modal (focus trap, Esc closes, focus returns to the header mark or the ... menu). The text is built in about.js; a host without /api/about shows the built-in text. */
        about() {
            if (!aboutSt) api.about().then((r) => { aboutSt = { info: r }; ctx.drawers.refreshNamed("about"); }).catch(() => { aboutSt = { info: null }; ctx.drawers.refreshNamed("about"); });
            return { icon: "info", title: "About 2T5 DECK+", subtitle: "2T5 Film", modal: true, body: aboutBody({ info: aboutSt && aboutSt.info, loading: !aboutSt, openLink: (id) => api.openLink(id) }) };
        },
        ai() {
            if (S.ep.ai === false) return null;
            const st = aiSt;
            const load = () => api.aiAccess().then((r) => { aiSt = r; S.ep.ai = true; ctx.drawers.refreshNamed("ai"); }).catch((e) => { if (e.status === 404) S.ep.ai = false; });
            if (!st) { load(); return { icon: "terminal", title: "AI access", subtitle: "Let an AI tool read and edit your deck", body: h("p", { class: "muted" }, "Loading…") }; }
            const copy = (text, what) => async () => { try { await navigator.clipboard.writeText(text); toast({ msg: `${what} copied`, tone: "info", key: "ai-copy" }); } catch (e) { toast({ msg: "Copy isn’t available here. Select the text instead.", tone: "info", key: "ai-copy" }); } };
            const wrap = h("div", { class: "dr-stack gap24" });
            const sw = switchCtl({ checked: st.enabled, label: "AI access", text: st.enabled ? "On" : "Off", onChange: async (on) => {
                try { aiSt = await api.setAiAccess(on); toast({ msg: on ? "AI access is on" : "AI access is off. Nothing is listening.", tone: "info", key: "ai" }); }
                catch (e) { sw.set(!on); toast({ msg: e.message || "Could not change AI access", tone: "bad", key: "ai" }); return; }
                ctx.drawers.refreshNamed("ai");
            } });
            wrap.append(h("section", { class: "pf-sec" },
                h("p", { class: "muted" }, "Lets an AI tool (Claude Code, Codex, Gemini CLI, Cursor and others) read your deck setup and change assignments and settings. It is off until you switch it on, and it can never arm the deck, press or turn a control, or send keyboard or mouse input."),
                h("div", { class: "m-row sw-row" }, h("span", { class: "m-name" }, "AI access"), sw.el)));
            if (st.enabled) {
                const line = (text, what, label) => h("div", { class: "copyline" }, h("code", { style: "white-space:pre-wrap" }, text), h("button", { type: "button", class: "icon-btn xs", "aria-label": `Copy ${label}`, "data-tip": "Copy", onclick: copy(text, what) }, icon("copy")));
                wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "Add it to your AI tool"),
                    h("p", { class: "muted" }, "Paste this line into a terminal to give Claude Code the deck:"), line(st.setupLine, "Setup line", "the setup line"),
                    h("p", { class: "muted" }, "Other tools use a settings file. Add this server to it:"), line(st.mcpConfig, "Config", "the config block")));
                wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "Status"),
                    h("p", { class: "muted" }, `Listening on ${st.address} only (this Mac). A private token in ${st.tokenFile} guards it; it changes every time you switch AI access on.`),
                    h("p", { class: "muted" }, st.changes ? `${st.changes} change${st.changes === 1 ? "" : "s"} made by AI tools since you switched it on. Each one is one step in History: Undo takes it back.` : "No changes by AI tools yet. What they change appears here at once, and Undo takes it back.")));
            } else {
                wrap.append(h("section", { class: "pf-sec" }, h("p", { class: "muted" }, "Off: nothing is listening and the AI tool cannot reach the app.")));
            }
            wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "System commands"),
                h("p", { class: "muted" }, "An AI tool may assign actions that run a program (a system command). That is an assignment like any other: the command runs only when you press that control while the deck is armed, and each assignment is one step in History, so Undo takes it back.")));
            wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "What an AI tool can do"),
                h("p", { class: "muted" }, `Read: apps, presets, layers and assignments, the action catalogue, calibration and engine status, history. Edit: assignments and preset settings, plus Undo and Redo. ${st.tools} tools in all.`)));
            return { icon: "terminal", title: "AI access", subtitle: "Let an AI tool read and edit your deck", body: wrap, reload: () => ctx.drawers.refreshNamed("ai") };
        },
        hud() {
            ctx.hudInvalidate = () => { hudSt = null; };
            // two settings live on the DaVinci Resolve preset but act globally: hudEnabled (the HUD on/off, default on) and hudWithoutResolve (default on); this panel reads and writes them from whichever app is open
            const rid = (S.apps.find((a) => String(a.id).includes("DaVinciResolve") || (a.packs || []).includes("resolve")) || {}).id;
            if (!rid) return null;
            const head = { icon: "panel-top", title: "HUD", subtitle: "The readout on your screen" };
            const load = () => api.preset(rid).then((r) => { const raw = r.preset || r.raw || r, st = (raw && raw.settings) || {}; hudSt = { rid, en: st.hudEnabled == null ? true : !!st.hudEnabled, on: st.hudWithoutResolve == null ? true : !!st.hudWithoutResolve }; ctx.drawers.refreshNamed("hud"); }).catch((er) => { hudSt = { rid, en: true, on: true, err: er.message || "Could not read the HUD setting" }; ctx.drawers.refreshNamed("hud"); });
            if (!hudSt || hudSt.rid !== rid) { hudSt = null; load(); return { ...head, body: h("p", { class: "muted" }, "Loading\u2026"), reload: load }; }
            const wrap = h("div", { class: "dr-stack gap24" });
            const errBox = h("div", { class: "fr-err", role: "alert", hidden: !hudSt.err }, hudSt.err || "");
            const bind = (key, field, label, msgOn, msgOff, toastKey) => { let sw; sw = switchCtl({ checked: hudSt[field], label, text: hudSt[field] ? "On" : "Off", onChange: async (on) => {
                try { await api.setting(rid, key, on); hudSt = { ...hudSt, [field]: on, err: undefined }; errBox.hidden = true; await ctx.reloadAll(); toast({ msg: on ? msgOn : msgOff, tone: "info", key: toastKey, undo: () => ctx.doUndo() }); }
                catch (er) { sw.set(!on); errBox.textContent = er.message || "Could not change the HUD setting"; errBox.hidden = false; return; }
                ctx.drawers.refreshNamed("hud");
            } }); return sw; };
            const sw1 = (bind("hudEnabled", "en", "Show the HUD", "The HUD is on", "The HUD is off", "hud-en"));
            wrap.append(h("section", { class: "pf-sec" },
                h("p", { class: "muted" }, "Turns the HUD on or off for every app, with or without Resolve. It stays off after a restart. Takes effect at once."),
                h("div", { class: "m-row sw-row" }, h("span", { class: "m-name" }, "Show the HUD"), sw1.el), errBox));
            const sw2 = (bind("hudWithoutResolve", "on", "Show the HUD without Resolve", "The HUD can show without Resolve", "The HUD follows Resolve: hidden while it is closed", "hud"));
            wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "Without Resolve"),
                h("p", { class: "muted" }, "When on, the HUD can show while DaVinci Resolve is not running. When off, it is hidden whenever Resolve is closed and comes back with it. Only matters while the HUD above is on."),
                h("div", { class: "m-row sw-row" }, h("span", { class: "m-name" }, "Show the HUD without Resolve"), sw2.el)));
            return { ...head, body: wrap, reload: load };
        },
        io() {
            if (S.ep.io === false) return null;
            const wrap = h("div", { class: "dr-stack gap24" });
            let applyFn = null, lastR = null; const importBtn = h("button", { type: "button", class: "btn neutral-primary", hidden: true }, "Import");
            // export
            wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "Export"), h("p", { class: "muted" }, "Saves this app’s preset as a bundle you can keep or import elsewhere."),
                h("button", { type: "button", class: "btn", onclick: async () => {
                    const native = window.go && window.go.main && window.go.main.App;
                    try {
                        if (native && native.ExportPresets) {
                            // the app window (WKWebView) can't download blobs: the engine writes the file and tells us where
                            const r = await native.ExportPresets(S.app);
                            const path = typeof r === "string" ? r : r && (r.path || r.file || r.saved);
                            if (!path) return; // an empty path means the save dialog was cancelled: say nothing
                            toast({ msg: `Saved to ${path}`, tone: "info", ms: 10000 });
                        } else {
                            const { blob, name } = await api.exportBundle(S.app);
                            if (!blob.size) throw new Error("The export was empty.");
                            const a = h("a", { href: URL.createObjectURL(blob), download: name }); document.body.append(a); a.click(); setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
                            toast({ msg: `Download started: ${name}. Your browser decides where it is saved.`, tone: "info", ms: 8000 });
                        }
                    } catch (er) { if (er.status === 404) { S.ep.io = false; ctx.epChanged(); close(); } else toast({ msg: `Export failed. ${er.message || er}`, tone: "bad" }); }
                } }, icon("archive"), "Export preset")));
            // import
            const fileIn = h("input", { type: "file", accept: ".zip,application/zip", hidden: true, "aria-label": "Choose a preset bundle" });
            const report = h("div", { class: "imp-report", "aria-live": "polite" });
            const nativeImp = window.go && window.go.main && window.go.main.App && window.go.main.App.ImportPresetsFromDialog;
            const dropzone = h("div", { class: "dropzone", tabindex: "0", role: "button", "aria-label": "Drop a preset file or choose one" }, icon("file-up", 32, "em-ic"), h("div", {}, "Drop a preset file or choose one"));
            const riskBox = h("div", { class: "rp-wrap" });
            const errLine = (msg) => report.append(h("div", { class: "imp-row bad", role: "alert" }, icon("triangle-alert", 16, "c-ic"), h("span", {}, msg)));
            let busy = false;
            const finishImport = async (confirm, skip) => {
                if (!applyFn || busy) return; busy = true; importBtn.disabled = true;
                try {
                    const res = await applyFn(confirm, skip);
                    const fl = (res && res.files) || (lastR && lastR.files) || [], n = fl.filter((f) => f.status !== "unchanged").length || fl.length;
                    await ctx.reloadAll();
                    const dr = (res && res.dropped) || [], skipped = Array.isArray(dr) ? dr.length : +dr || 0;
                    let top = null; try { const hr = await api.history(); top = (hr.entries || []).find((x) => x.index === (hr.current ?? hr.pos)); } catch (e) { /* no history endpoint */ }
                    const isImport = !top || /import/i.test(String(top.label || ""));
                    toast({ msg: `Imported ${n} preset${n === 1 ? "" : "s"}${skipped ? `, skipped ${skipped} program${skipped === 1 ? "" : "s"}` : ""}`, undo: isImport ? () => ctx.doUndo() : undefined });
                    report.replaceChildren(); riskBox.replaceChildren(); importBtn.hidden = true; applyFn = null; lastR = null;
                } catch (er) {
                    const msg = (er && er.message) || String(er);
                    if (er && er.status === 409 && er.report) { lastR = er.report; showRisk(er.report, er.report.riskChanged || (er.data && er.data.riskChanged)); }
                    else if ((er && er.status === 409) || (!confirm && /confirm|risk|run programs|destructive/i.test(msg) && lastR)) showRisk((er && er.report) || lastR || { files: [], risk: { shell: 1 } });
                    else { if (/no longer pending|expired|unknown token/i.test(msg)) { applyFn = null; importBtn.hidden = true; riskBox.replaceChildren(); } errLine(er && er.status === 400 && er.field && !msg.includes(er.field) ? `${plain(String(er.field))}: ${msg}` : msg); }
                } finally { busy = false; importBtn.disabled = false; }
            };
            function showRisk(r, changed) {
                const k = riskOf(r); importBtn.hidden = true;
                const items = k.progs.map((p) => ({ ...plainCommand(p.cmd), file: p.file }));
                const progN = k.sum.shell + k.sum.destructive, otherN = k.sum.unknown + k.sum.panic + k.sum.settings, dropN = progN + otherN;
                const shown = progN || items.length; // counts come from the report; the listed lines only fill in when it sent none
                const onlyProgs = otherN === 0 && k.sum.destructive === 0;
                const skipLabel = dropN === 0 ? "Import, skip risky items" : onlyProgs ? `Import, skip ${shown} program${shown === 1 ? "" : "s"}` : `Import, skip ${dropN} risky item${dropN === 1 ? "" : "s"}`;
                const canSkip = items.length > 0 || dropN > 0;
                const notes = [];
                if (changed) notes.push("What this bundle would install changed since you looked at it. This is the new list: read it again before you go on.");
                if (k.sum.unknown) notes.push("Contains actions the editor cannot identify: they are treated as risky.");
                for (const x of k.pan) notes.push(`Would make ${x.text || "a control"} disarm the deck instead of its current action.`);
                for (const x of k.weak) notes.push(`${x.file ? x.file + ": " : ""}${x.why ? String(x.why) : `would weaken the safety setting ${plain(String(x.key))}`}.`);
                if (!items.length && !dropN) notes.push("The engine asked for confirmation but did not list the command lines.");
                riskBox.replaceChildren(riskSheet({
                    head: changed ? "This bundle now needs a fresh look" : onlyProgs ? "This bundle can run programs on your Mac" : "This bundle contains actions that need your attention",
                    lead: onlyProgs ? "Some of its actions start programs or change files. Read each line. Nothing has been imported yet." : "Some of its actions run programs, delete things, or cannot be identified. Nothing has been imported yet.",
                    chips: [riskChip(k.sum.shell, "run a command", "warn"), riskChip(k.sum.destructive, "destructive", "danger"), riskChip(k.sum.unknown, "unidentified", "warn"), riskChip(k.sum.panic, "panic key", "warn"), riskChip(k.sum.settings, "weaker safety", "warn"), riskChip(k.keys, "key actions", "keys")],
                    notes, items, itemsLabel: "Programs this bundle would run",
                    safe: canSkip ? { label: skipLabel, onClick: () => finishImport(false, true) } : null,
                    confirm: { word: "import", label: "Import everything", intro: "To import the programs too, type ", onClick: () => finishImport(true, false) },
                    cancel: { label: "Cancel", caption: "Nothing is imported.", onClick: () => { riskBox.replaceChildren(); importBtn.hidden = false; } },
                }));
                riskBox.scrollIntoView({ block: "nearest" });
            }
            const renderPreview = (r) => {
                lastR = r;
                report.append(...(r.files || []).map((f) => { const fk = riskOf({ files: [f] }); return h("div", { class: "imp-row" }, icon(f.status === "unchanged" ? "check" : fk.any ? "triangle-alert" : "info", 16, "c-ic"), h("span", { class: "imp-n" }, f.name || f.id), fk.any ? h("span", { class: "chip risk-tag st-set" }, (fk.sum.unknown || fk.sum.panic || fk.sum.destructive) ? "needs attention" : `${fk.sum.shell || fk.progs.length} can run programs`) : null, h("span", { class: "chip" }, f.status === "new" ? `new \u00b7 ${f.added} controls` : f.status === "changed" ? h("span", { "data-tip": `${f.added} added, ${f.changed} changed, ${f.removed} removed` }, [[f.added, "added"], [f.changed, "changed"], [f.removed, "removed"]].filter(([n]) => n).map(([n, w]) => `${n} ${w}`).join(", ") || "changed") : "unchanged")); }));
                const changed = (r.files || []).some((f) => f.status !== "unchanged");
                importBtn.hidden = !changed;
                if (!changed) report.append(h("p", { class: "muted" }, "Nothing to import: the bundle matches what you have."));
                if (changed && riskOf(r).any) showRisk(r);
            };
            const readFile = async (file) => {
                report.replaceChildren(); riskBox.replaceChildren(); importBtn.hidden = true; applyFn = null;
                try { const r = await api.importBundle(file, true); applyFn = (c, skip) => api.importBundle(file, false, c, skip, r.token); renderPreview(r); }
                catch (er) { report.append(h("div", { class: "imp-row bad" }, icon("triangle-alert", 16, "c-ic"), h("span", {}, er.problems && er.problems.length && /^import refused/i.test(er.message) ? "This file can\u2019t be imported:" : er.message)), ...(er.problems || []).map((p) => h("div", { class: "imp-row bad sub" }, /not a zip file/i.test(p) ? "It is not an exported preset. Choose a .zip made with Export preset." : p))); }
            };
            /* the packaged app: the dialog only PREVIEWS (a one-use token); nothing is imported until ApplyImport / ApplyImportConfirmed */
            const nativeImport = async () => {
                report.replaceChildren(); riskBox.replaceChildren(); importBtn.hidden = true; applyFn = null;
                try {
                    const App = window.go.main.App, r = await App.ImportPresetsFromDialog();
                    if (!r || !r.token || !(r.files || []).length) return; // cancelled dialog: no toast
                    applyFn = (c, skip) => (skip && App.ApplyImportSkippingRisky ? App.ApplyImportSkippingRisky(r.token) : c ? App.ApplyImportConfirmed(r.token) : App.ApplyImport(r.token));
                    renderPreview(r);
                } catch (er) { errLine(`Import failed. ${(er && er.message) || er}`); }
            };
            fileIn.addEventListener("change", () => fileIn.files[0] && readFile(fileIn.files[0]));
            dropzone.addEventListener("click", () => { if (nativeImp) nativeImport(); else fileIn.click(); });
            dropzone.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); if (nativeImp) nativeImport(); else fileIn.click(); } });
            dropzone.addEventListener("dragover", (e) => { e.preventDefault(); dropzone.classList.add("over"); });
            dropzone.addEventListener("dragleave", () => dropzone.classList.remove("over"));
            dropzone.addEventListener("drop", (e) => { e.preventDefault(); dropzone.classList.remove("over"); if (nativeImp) return; const f = e.dataTransfer.files[0]; if (f) readFile(f); });
            importBtn.addEventListener("click", () => finishImport(false));
            wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "Import"), dropzone, fileIn, report, riskBox, importBtn));
            // backups
            const bl = h("div", { class: "bk" }), skipBox = h("div", { class: "imp-report" });
            const REASON = { "pre-update": "Before update", daily: "Daily", manual: "Manual", restore: "Before a restore", import: "Before an import", snapshot: "Snapshot" };
            const when = (t) => { const d = new Date(t); return Number.isNaN(+d) ? "" : d.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }); };
            const sendRestore = async (b, slot, confirmRisk, skipRisky) => {
                try {
                    const rr = await api.restoreBackup(b.name, confirmRisk, skipRisky); await ctx.reloadAll();
                    const sk = (rr && rr.skipped) || [], dr = rr && Array.isArray(rr.dropped) ? rr.dropped.length : +(rr && rr.dropped) || 0;
                    toast({ msg: sk.length ? `Backup restored. ${sk.length} unreadable file${sk.length === 1 ? " was" : "s were"} skipped and kept as they are.` : dr ? `Backup restored. ${dr} risky item${dr === 1 ? " was" : "s were"} left out.` : "Backup restored", undo: () => ctx.doUndo(), ms: sk.length || dr ? 9000 : 6000 });
                    if (sk.length) skipBox.replaceChildren(h("div", { class: "imp-row" }, icon("info", 16, "c-ic"), h("span", {}, "Skipped: "), h("span", { class: "mono" }, sk.map((x) => x.file || x).join(", "))));
                    S.hiBackup = null; loadB();
                } catch (er) {
                    const rep = er.status === 409 && (er.report || (er.data && er.data.needsConfirm ? er.data : null));
                    if (rep) restoreRisk(b, slot, rep, !!(rep.riskChanged || (er.data && er.data.riskChanged)));
                    else toast({ msg: er.message, tone: "bad" });
                }
            };
            const doRestore = (b, slot) => sendRestore(b, slot, false, false);
            /* a zip this app did not make (planted in the shared backups folder) is judged like an import before anything is restored */
            const restoreRisk = (b, slot, rep, changed) => {
                const k = riskOf(rep), items = k.progs.map((p) => ({ ...plainCommand(p.cmd), file: p.file }));
                const progN = k.sum.shell + k.sum.destructive, dropN = progN + k.sum.unknown + k.sum.panic + k.sum.settings, shown = progN || items.length;
                const notes = [];
                if (changed) notes.push("What this backup would restore changed since you looked at it. This is the new list: read it again before you go on.");
                if (k.sum.unknown) notes.push("Contains actions the editor cannot identify: they are treated as risky.");
                for (const x of k.pan) notes.push(`Would make ${x.text || "a control"} disarm the deck instead of its current action.`);
                for (const x of k.weak) notes.push(`${x.file ? x.file + ": " : ""}${x.why ? String(x.why) : `would weaken the safety setting ${plain(String(x.key))}`}.`);
                slot.replaceChildren(riskSheet({
                    head: "This backup was not made by this app", lead: "It holds risky actions. Nothing has been restored yet.",
                    chips: [riskChip(k.sum.shell, "run a command", "warn"), riskChip(k.sum.destructive, "destructive", "danger"), riskChip(k.sum.unknown, "unidentified", "warn"), riskChip(k.sum.panic, "panic key", "warn"), riskChip(k.sum.settings, "weaker safety", "warn"), riskChip(k.keys, "key actions", "keys")],
                    notes, items, itemsLabel: "Programs this backup would restore",
                    safe: { label: dropN ? `Restore, skip ${dropN} risky item${dropN === 1 ? "" : "s"}` : shown ? `Restore, skip ${shown} program${shown === 1 ? "" : "s"}` : "Restore, skip risky items", onClick: () => sendRestore(b, slot, false, true) },
                    confirm: { word: "restore", label: "Restore everything", intro: "To restore the risky items too, type ", onClick: () => sendRestore(b, slot, true, false) },
                    cancel: { label: "Cancel", caption: "Nothing is restored.", onClick: () => slot.replaceChildren() },
                }));
                slot.scrollIntoView({ block: "nearest" });
            };
            /* review before restore: what it would add, remove and change, per preset (inline, two steps, no dialog) */
            const review = async (b, slot, btn) => {
                btn.disabled = true;
                try {
                    const r = await api.backupDiff(b.name), d = r.diff || r, files = (d.files || []).filter((f) => f.status !== "unchanged");
                    // a preset the engine lists as changed whose counts and add / remove / change totals are all equal changes nothing in its assignments: say "No change", not "91 -> 91"
                    const same = (f) => f.status === "changed" && f.current === f.backup && !(f.added || 0) && !(f.removed || 0) && !(f.changed || 0);
                    const real = files.filter((f) => !same(f));
                    slot.replaceChildren(h("div", { class: "bk-diff" },
                        h("p", { class: "bk-sum" }, real.length ? `Restoring changes ${real.length} preset${real.length === 1 ? "" : "s"}: +${d.added || 0} / \u2212${d.removed || 0} assignments` : "Restoring would change nothing: your presets already match this backup."),
                        ...files.map((f) => h("div", { class: "bk-df" }, h("span", {}, f.name || f.id), same(f) ? h("span", { class: "muted", "data-tip": `${f.current} assignments, identical to the backup` }, "No change") : h("span", { class: "mono" }, f.status === "new" ? `adds ${f.backup}` : f.status === "removed" ? `removes ${f.current}` : `${f.current} \u2192 ${f.backup}`))),
                        h("div", { class: "cp-act" }, h("button", { type: "button", class: "btn text", onclick: () => { slot.replaceChildren(); btn.disabled = false; } }, "Cancel"),
                            confirmButton({ label: "Restore", confirmLabel: "Press again to confirm restore", ico: "rotate-ccw", cls: "btn neutral-primary", onConfirm: () => doRestore(b, slot) }))));
                } catch (er) { btn.disabled = false; if (er.status === 404) { slot.replaceChildren(); doRestoreDirect(b, slot); } else toast({ msg: er.message, tone: "bad" }); }
            };
            const doRestoreDirect = (b, slot) => slot.replaceChildren(h("div", { class: "bk-diff" }, h("p", { class: "bk-sum" }, "This engine cannot preview a restore."), confirmButton({ label: "Restore", confirmLabel: "Press again to confirm restore", ico: "rotate-ccw", cls: "btn neutral-primary", onConfirm: () => doRestore(b, slot) })));
            const loadB = async () => {
                try {
                    const r = await api.backups(), bs = r.backups || [];
                    bl.replaceChildren(...(bs.length ? bs.map((b) => {
                        const slot = h("div", { class: "bk-slot" });
                        const btn = h("button", { type: "button", class: "btn", onclick: () => review(b, slot, btn) }, icon("rotate-ccw"), "Review");
                        const tag = b.reason === "pre-update" || b.label ? null : null;
                        return h("div", { class: "bk-row" + (S.hiBackup === b.name ? " hi" : ""), "data-name": b.name },
                            h("div", { class: "bk-main" }, h("div", { class: "bk-t" }, h("span", { class: b.label ? "bk-label" : "mono" }, b.label || b.name.replace(/^presets-|\.zip$/g, "")),
                                h("small", {}, [when(b.time), `${fmt((b.size || 0) / 1024, 1)} KB`].filter(Boolean).join(" \u00b7 ")), h("small", { class: "bk-tags" }, b.reason ? h("span", { class: "chip bk-chip" }, REASON[b.reason] || b.reason) : null, S.hiBackup === b.name ? h("span", { class: "chip bk-chip" }, "Suggested") : null, b.trusted === false ? h("span", { class: "chip bk-chip rk-warn", "data-tip": "This zip was not made by this app. Restoring it asks first if it holds risky actions." }, icon("triangle-alert", 12), "Not made by this app") : b.trusted === true ? h("span", { class: "chip bk-chip trusted", "data-tip": "Made by this app", "aria-label": "Made by this app" }, icon("shield-check", 12)) : null)), btn), slot);
                    }) : [h("p", { class: "muted" }, "No backups yet. They are made automatically before an import or restore.")]));
                } catch (er) { if (er.status === 404) bl.replaceChildren(); else bl.replaceChildren(h("div", { class: "fr-err" }, icon("triangle-alert"), er.message)); }
            };
            loadB();
            const snapName = textField({ value: "", placeholder: "Snapshot name", label: "Snapshot name", onInput: () => {} });
            const snapBtn = h("button", { type: "button", class: "btn", onclick: async () => {
                try { const r = await api.backupNow(snapName.input.value.trim()); snapName.input.value = ""; snapName.input.dispatchEvent(new Event("input")); toast({ msg: r && r.made === false ? "Nothing changed since the last backup." : "Snapshot saved", tone: "info" }); loadB(); }
                catch (er) { toast({ msg: er.message, tone: "bad" }); }
            } }, icon("archive"), "Save snapshot");
            snapName.input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); snapBtn.click(); } });
            wrap.append(h("section", { class: "pf-sec" }, h("h3", {}, "Backups"), h("p", { class: "muted" }, "Restore replaces your current presets. A safety copy of them is saved first, and the restore is one undo step."), h("div", { class: "snap-row" }, snapName.el, snapBtn), (S.canaryHint ? h("p", { class: "muted" }, S.canaryHint) : null), skipBox, bl));
            return { icon: "archive", title: "Import, export, backup", subtitle: appName(S.app), modal: true, body: wrap };
        },
    };
    document.addEventListener("keydown", (e) => {
        if (e.key !== "Escape" || e.defaultPrevented) return;
        const inLive = live.contains(document.activeElement);
        if (inLive && liveOpen) { e.preventDefault(); closeLive(); }
        else if (cur) { e.preventDefault(); close(); if (ctx.deselect) ctx.deselect(); } // Esc closes the drawer and clears the selection
        else if (liveOpen) { e.preventDefault(); closeLive(); }
    });
    return {
        open, close, rebuild, refreshNamed: (n) => { if (cur && cur.name === n) rebuild(); }, openLive, closeLive, DEFS,
        /** a preview (import / copy dry run) is on screen: a background reload must not rebuild the drawer under it */
        get midFlow() { return !!(cur && [...bodyEl.querySelectorAll(".imp-row, .cp-act")].some((n) => !n.closest("[hidden]"))); },
        get current() { return cur && cur.name; }, get closing() { return closing; }, get liveOpen() { return liveOpen; },
        refresh() { if (cur && cur.def.reload) cur.def.reload(); },
    };
}
