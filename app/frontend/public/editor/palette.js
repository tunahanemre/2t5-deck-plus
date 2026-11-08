// palette.js: Cmd+K jump-to-control. Keyboard-first, recent items, Esc closes. A custom modal (never a browser dialog).
import { h, icon, reduced, anim, EASE_OUT, EASE_IN, store, debounce } from "./util.js";
import { keycaps } from "./ui.js";

const hl = (text, q) => {
    if (!q) return text;
    const i = String(text).toLowerCase().indexOf(q.toLowerCase());
    return i < 0 ? text : [text.slice(0, i), h("mark", {}, text.slice(i, i + q.length)), text.slice(i + q.length)];
};
/** source: { query(q) -> Promise<item[]>, idle() -> item[], pick(item) }  item = {key,title,sub,icon,app,bank,control} */
export function openPalette(source, opener) {
    if (document.getElementById("pal")) return;
    const input = h("input", { type: "text", placeholder: "Jump to a control, an action or a command", "aria-label": "Jump to a control, an action or a command", role: "combobox", "aria-expanded": "true", "aria-controls": "pal-list", autocomplete: "off", spellcheck: "false" });
    const list = h("div", { class: "pal-list", id: "pal-list", role: "listbox" });
    const foot = h("div", { class: "pal-foot" }, h("span", {}, h("kbd", { class: "cap sm" }, "↑"), h("kbd", { class: "cap sm" }, "↓"), "move"), h("span", {}, h("kbd", { class: "cap sm" }, "⏎"), "go"), h("span", {}, h("kbd", { class: "cap sm" }, "Esc"), "close"));
    const box = h("div", { class: "palette", role: "dialog", "aria-modal": "true", "aria-label": "Command palette" }, h("div", { class: "pal-in" }, icon("search", 20), input), list, foot);
    const scrim = h("div", { class: "pal-scrim", id: "pal", onpointerdown: (e) => { if (e.target === scrim) close(); } }, box);
    document.body.append(scrim);
    if (!reduced()) { anim(scrim, [{ opacity: 0 }, { opacity: 1 }], { duration: 140 }); anim(box, [{ opacity: 0, transform: "translateY(-8px) scale(.98)" }, { opacity: 1, transform: "none" }], { duration: 200, easing: EASE_OUT }); }
    let items = [], act = 0, seq = 0, closed = false;
    function paint(q, head) {
        list.replaceChildren();
        let prev = null;
        items.forEach((it, i) => {
            const sec = it.sec || (i === 0 ? head : null);
            if (sec && sec !== prev) { list.append(h("div", { class: "pal-h", role: "presentation" }, sec)); prev = sec; }
            const row = h("div", { class: "pal-row" + (i === act ? " act" : ""), role: "option", id: "pal-o" + i, "aria-selected": String(i === act) }, icon(it.icon || "circle-dashed", 16, "m-ic"),
                h("span", { class: "m-txt" }, h("span", { class: "m-name" }, hl(it.title, q)), it.sub ? h("span", { class: "m-sub" }, it.sub) : null),
                it.hint ? h("span", { class: "chip" }, it.hint) : null,
                it.kbd ? keycaps(it.kbd, "pal-kbd sm") : null);
            row.addEventListener("pointermove", () => { if (act !== i) { act = i; mark(); } });
            row.addEventListener("click", () => choose(i));
            list.append(row);
        });
        if (!items.length) list.append(h("div", { class: "m-empty" }, icon("search-x", 24), h("div", {}, q ? "Nothing matches " : "Nothing to show yet", q ? h("b", {}, `“${q}”`) : "")));
        mark();
    }
    function mark() { Array.from(list.querySelectorAll(".pal-row")).forEach((r, i) => { r.classList.toggle("act", i === act); r.setAttribute("aria-selected", String(i === act)); if (i === act) { input.setAttribute("aria-activedescendant", r.id); r.scrollIntoView({ block: "nearest" }); } }); }
    async function run(q) {
        const my = ++seq;
        if (!q.trim()) { items = source.idle(); act = 0; paint("", ""); return; }
        let r = []; try { r = await source.query(q.trim()); } catch (e) { r = []; }
        if (my !== seq || closed) return;
        items = r.slice(0, 60); act = 0; paint(q.trim(), "");
    }
    const runD = debounce(run, 90);
    input.addEventListener("input", () => runD(input.value));
    function choose(i) { const it = items[i]; if (!it) return; close(true); source.pick(it); }
    function close(noFocus) {
        if (closed) return; closed = true; document.removeEventListener("keydown", onKey, true);
        const done = () => scrim.remove();
        const a = reduced() ? null : anim(scrim, [{ opacity: 1 }, { opacity: 0 }], { duration: 110, easing: EASE_IN, fill: "forwards" });
        if (a) a.onfinish = done; else done();
        if (!noFocus && opener && document.contains(opener)) opener.focus({ preventScroll: true });
    }
    function onKey(e) {
        if (e.key === "Escape" || ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k")) { e.preventDefault(); e.stopPropagation(); close(); }
        else if (e.key === "ArrowDown") { e.preventDefault(); if (items.length) { act = (act + 1) % items.length; mark(); } }
        else if (e.key === "ArrowUp") { e.preventDefault(); if (items.length) { act = (act - 1 + items.length) % items.length; mark(); } }
        else if (e.key === "Enter") { e.preventDefault(); choose(act); }
        else if (e.key === "Tab") { e.preventDefault(); }
    }
    document.addEventListener("keydown", onKey, true);
    run(""); input.focus();
    return { close };
}
