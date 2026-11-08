// appPicker.js: the one app picker (replaces the old tab strip). A single header button shows the app being edited;
// it opens a filterable listbox: Global pinned on top, then every app by name, "Add app" as the last row.
// No sliding highlight anywhere: the active row is a plain background swap.
import { h, icon } from "./util.js";
import { popover, appTile, nextId } from "./ui.js";

/** the one app order used everywhere (picker rows, Cmd+1..9, palette): Global first, then by name */
export const orderedApps = (apps) => [
    ...apps.filter((a) => a.isGlobal),
    ...apps.filter((a) => !a.isGlobal).sort((a, b) => String(a.name || a.id).localeCompare(String(b.name || b.id), undefined, { sensitivity: "base", numeric: true })),
];
/** the app's real icon, bare at 20px; Global gets the matching neutral link glyph */
const appIcon = (a) => (a.isGlobal ? h("span", { class: "ap-ic ap-glob" }, icon("link", 16)) : appTile(a, { size: 20, url: a.iconUrl }));
/* no calibration warning on the app: the status lives only in Mouse calibration */
const calNeeds = () => false;
const label = (a) => (a.isGlobal ? "Global" : a.name || a.id);
/** plain words for the calibration warning: what it means for him and what to do */
const calText = (a) => { const c = a.cal || {}, n = (c.needs || 0) + (c.stale || 0) + (c.unknown || 0), total = c.targets || n, name = label(a);
    const imp = (c.imported || 0) ? `; ${c.imported} imported from ld-resolve and not re-checked` : "";
    const why = (c.needs || 0) ? `${c.needs} of ${total} mouse targets have never been calibrated${imp}` : (c.stale || 0) ? `${c.stale} of ${total} mouse targets are out of date` : `${c.unknown || n} of ${total} mouse targets could not be checked`;
    return `${name}: ${why}. Deck keys that click on the screen can miss until you calibrate them. Click to calibrate.`; };

function mark(text, q) {
    if (!q) return text;
    const i = text.toLowerCase().indexOf(q.toLowerCase());
    return i < 0 ? text : [text.slice(0, i), h("mark", {}, text.slice(i, i + q.length)), text.slice(i + q.length)];
}

export function createAppPicker({ button, getApps, getCurrent, getFront, getHeld = () => false, onSelect, onAdd, onCalibrate }) {
    const name = h("span", { class: "ap-name" });
    let tileHost = h("span", { class: "ap-tile" }), calDot = h("span", { class: "ap-warn", role: "button", tabindex: "-1", hidden: true }, icon("triangle-alert", 16));
    let tileSig = "", calTxt = "";
    calDot.addEventListener("click", (e) => { if (!onCalibrate) return; e.stopPropagation(); e.preventDefault(); const cur = getApps().find((x) => x.id === getCurrent()); if (cur) onCalibrate(cur, calDot); });
    const heldDot = h("span", { class: "ap-held", title: "A change made outside the editor is waiting for review", "aria-hidden": "true", hidden: true });
    button.replaceChildren(tileHost, name, heldDot, calDot, icon("chevron-down", 16, "sel-chev"));
    let pop = null, list = null, head = null, foot = null, root = null, input = null, q = "", active = -1, flat = [];
    let lid = "";

    function paint() {
        const apps = getApps(), a = apps.find((x) => x.id === getCurrent()) || apps[0];
        if (!a) { name.textContent = "Choose an app"; tileHost.replaceChildren(); calDot.hidden = true; return; }
        /* repaint only what changed: the app icon <img> is kept (a new element on every poll flashed it) */
        const sig = a.id + "|" + (a.iconUrl || "") + "|" + (a.name || "");
        if (sig !== tileSig) { tileSig = sig; tileHost.replaceChildren(appIcon(a)); }
        if (name.textContent !== label(a)) name.textContent = label(a);
        const need = calNeeds(a); if (calDot.hidden === need) calDot.hidden = !need;
        if (need) { const t = calText(a); if (t !== calTxt) { calTxt = t; calDot.title = t; calDot.setAttribute("aria-label", t); } }
        { const hv = !getHeld(a.id); if (heldDot.hidden !== hv) heldDot.hidden = hv; }
        button.setAttribute("aria-label", `App: ${label(a)}${a.id === getFront() ? ", in front" : ""}${calNeeds(a) ? ", needs mouse calibration" : ""}${getHeld(a.id) ? ", a change from outside is waiting" : ""}. Change app`);
        button.dataset.app = a.id;
    }

    function build() {
        if (!list) return;
        list.replaceChildren(); foot.replaceChildren(); head.replaceChildren(); flat = [];
        const ql = q.trim().toLowerCase(), cur = getCurrent(), fr = getFront();
        const ok = (a) => !ql || (label(a) + " " + a.id).toLowerCase().includes(ql);
        const all = orderedApps(getApps()).filter(ok);
        const add = (it) => {
            const i = flat.length, on = it.app && it.app.id === cur;
            const row = h("div", { class: "m-row ap-row" + (it.add ? " ap-add" : "") + (on ? " cur" : ""), role: "option", id: it.add ? `${lid}-add` : `${lid}-o${i}`, "aria-selected": on ? "true" : "false", "data-app": it.app ? it.app.id : null },
                it.add ? h("span", { class: "ap-ic ap-plus" }, icon("plus", 16)) : appIcon(it.app),
                h("span", { class: "m-txt" }, h("span", { class: "m-name" }, it.add ? "Add app" : mark(label(it.app), ql))),
                calNeeds(it.app) ? h("span", { class: "ap-warn", title: calText(it.app), "aria-label": calText(it.app) }, icon("triangle-alert", 16)) : null,
                it.app && getHeld(it.app.id) ? h("span", { class: "ap-held", title: "A change made outside the editor is waiting for review", "aria-label": "Change waiting" }) : null,
                it.app && it.app.id === fr ? h("span", { class: "ap-front-tag", title: "This app is in front on the Mac" }, "In front") : null,
                it.app ? h("span", { class: "ap-n", "aria-label": (it.app.assigned ?? 0) > 0 ? `${it.app.assigned} assigned` : null }, (it.app.assigned ?? 0) > 0 ? String(it.app.assigned) : "") : null,
                h("span", { class: "ap-ck" }, on ? icon("check", 16, "m-check") : null));
            row.addEventListener("pointermove", () => setActive(i, false));
            row.addEventListener("click", () => pick(i));
            it.row = row; flat.push(it); (it.add ? foot : it.app.isGlobal ? head : list).append(row);
        };
        const g = all.filter((a) => a.isGlobal), rest = all.filter((a) => !a.isGlobal);
        g.forEach((a) => add({ app: a }));
        rest.forEach((a) => add({ app: a }));
        if (!all.length) list.append(h("div", { class: "m-empty ap-empty", role: "presentation" }, "No app matches ", h("b", {}, `“${q.trim()}”`)));
        add({ add: true });
        const cur2 = flat.findIndex((f) => f.app && f.app.id === cur);
        setActive(ql ? (all.length ? 0 : flat.length - 1) : cur2 >= 0 ? cur2 : 0, true);
    }
    function setActive(i, scroll) {
        root && root.querySelector(".ap-row.act")?.classList.remove("act");
        active = i;
        const row = flat[i] && flat[i].row;
        if (!row) { input && input.removeAttribute("aria-activedescendant"); return; }
        row.classList.add("act");
        if (input) input.setAttribute("aria-activedescendant", row.id); // the list can repaint while the popover is closed (the editor follows the front app): no input then
        if (scroll) row.scrollIntoView({ block: "nearest" });
    }
    function pick(i) {
        const f = flat[i]; if (!f) return;
        const cur = getCurrent();
        if (f.add) { pop.close({ noFocus: true }); onAdd(button); return; }
        pop.close();
        if (f.app.id !== cur) onSelect(f.app.id);
    }
    function onKey(e) {
        const n = flat.length;
        if (e.key === "ArrowDown") { e.preventDefault(); setActive((active + 1) % n, true); }
        else if (e.key === "ArrowUp") { e.preventDefault(); setActive((active - 1 + n) % n, true); }
        else if (e.key === "Home" && e.ctrlKey) { e.preventDefault(); setActive(0, true); }
        else if (e.key === "End" && e.ctrlKey) { e.preventDefault(); setActive(n - 1, true); }
        else if (e.key === "PageDown") { e.preventDefault(); setActive(Math.min(n - 1, active + 6), true); }
        else if (e.key === "PageUp") { e.preventDefault(); setActive(Math.max(0, active - 6), true); }
        else if (e.key === "Enter") { e.preventDefault(); if (active >= 0) pick(active); }
        else if (e.key === "Tab") { pop.close({ noFocus: true }); }
    }

    function open() {
        if (pop && pop.open) { pop.close(); return; }
        q = ""; lid = nextId("ap");
        input = h("input", { type: "text", class: "m-search-in", placeholder: "Find an app", "aria-label": "Find an app", role: "combobox", "aria-controls": lid, "aria-expanded": "true", "aria-autocomplete": "list", autocomplete: "off", spellcheck: "false" });
        const clear = h("button", { type: "button", class: "icon-btn xs", "aria-label": "Clear search", tabindex: "-1", hidden: true, onclick: () => { input.value = ""; q = ""; clear.hidden = true; build(); input.focus(); } }, icon("x"));
        list = h("div", { class: "m-list ap-list", role: "listbox", id: lid, "aria-label": "Apps" });
        foot = h("div", { class: "ap-foot" }); head = h("div", { class: "ap-head" });
        list.setAttribute("aria-owns", `${lid}-add`);
        const el = h("div", { class: "menu apick" }, h("div", { class: "m-search" }, icon("search"), input, clear), head, list, foot); root = el;
        input.addEventListener("keydown", onKey);
        input.addEventListener("input", () => { q = input.value; clear.hidden = !q; build(); });
        build(); // fill before the popover measures
        pop = popover({ anchor: button, el, matchWidth: true, minWidth: 320, maxHeight: 440, placement: "bottom-start", gap: 6,
            onClose: () => { list = null; head = null; foot = null; root = null; input = null; button.removeAttribute("aria-controls"); },
            onEsc: () => { if (q) { input.value = ""; q = ""; clear.hidden = true; build(); return true; } return false; } });
        button.setAttribute("aria-controls", lid);
        input.focus({ preventScroll: true });
        root.querySelector(".ap-row.act")?.scrollIntoView({ block: "nearest" });
    }

    button.addEventListener("click", open);
    button.addEventListener("keydown", (e) => {
        if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Enter" || e.key === " ") { e.preventDefault(); if (!(pop && pop.open)) open(); }
    });

    return {
        render() { paint(); if (pop && pop.open) build(); },
        setFront() { paint(); if (!root) return; const fr = getFront(); root.querySelectorAll(".ap-row[data-app]").forEach((r) => { const d = r.querySelector(".ap-front-tag"), on = r.dataset.app === fr; if (on && !d) r.insertBefore(h("span", { class: "ap-front-tag", title: "This app is in front on the Mac" }, "In front"), r.querySelector(".ap-n")); else if (!on && d) d.remove(); }); },
        flash() { button.classList.remove("flash"); void button.offsetWidth; button.classList.add("flash"); },
        focus() { button.focus({ preventScroll: true }); },
        close() { pop && pop.close({ noFocus: true }); },
    };
}
