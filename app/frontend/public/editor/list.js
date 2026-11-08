// list.js: "List" view, the accessible and printable twin of the photo. Every assignment of the current app, across banks,
// sortable and filterable (action, source, bank), keyboard-first (roving rows), with a copy-as-Markdown cheat sheet.
import { h, icon, bankLabel } from "./util.js";
import { S, eff, ctlMeta, actionDef, appName, isGlobalApp, banks, allBanks, smodeLabel, actionIcon, MODS, modGlyph, parseBank, bankName, pages } from "./state.js";
import { HOT } from "./hotspots.js";
import { plain, plainControl } from "./plain.js";
import { KIND, keycaps, isChord, segmented, selectField, textField, stateChip, toast } from "./ui.js";

const COLS = [["control", "Control"], ["action", "Action"], ["value", "Value"], ["source", "Source"], ["bank", "Press"]];
const SRC = { app: ["set", "App"], global: ["inh", "Global"] };
const srcOf = (e) => (isGlobalApp() && e.source === "app" ? ["set", "Global"] : SRC[e.source] || ["default", "Default"]);

export function createList(ctx) {
    const L = { q: "", bank: "all", source: "all", sort: "order", dir: 1, rows: [], shown: [], rove: null };
    const q = textField({ value: "", placeholder: "Filter by control or action", label: "Filter assignments", onInput: (v) => { L.q = v; paint(); } });
    q.el.classList.add("lv-q");
    let bankSel = null;
    const srcSeg = segmented({ options: [{ value: "all", label: "All" }, { value: "app", label: "App" }, { value: "global", label: "Global" }, { value: "default", label: "Default" }], value: "all", label: "Source", onChange: (v) => { L.source = v; paint(); } });
    srcSeg.el.classList.add("lv-src");
    const count = h("span", { class: "lv-count", role: "status", "aria-live": "polite" });
    const copyBtn = h("button", { type: "button", class: "btn sm", onclick: () => copyMarkdown() }, icon("copy"), "Markdown");
    copyBtn.setAttribute("data-tip", "Copy these rows as a Markdown cheat sheet"); copyBtn.setAttribute("aria-label", "Copy as Markdown");
    const bankSlot = h("span", { class: "lv-bank" });
    const bar = h("div", { class: "lv-bar" }, q.el, bankSlot, srcSeg.el, h("span", { class: "lv-sp" }), count, copyBtn);
    const heads = COLS.map(([k, label]) => {
        const b = h("button", { type: "button", class: "lt-sort", onclick: () => sortBy(k) }, h("span", {}, label), icon("chevron-up", 16, "lt-chev"));
        return h("th", { scope: "col", "aria-sort": "none", "data-col": k, class: "c-" + k }, b);
    });
    const tbody = h("tbody");
    const table = h("table", { class: "lt", "aria-label": "Every assignment" }, h("thead", {}, h("tr", {}, ...heads)), tbody);
    const scroll = h("div", { class: "lv-scroll" }, table);
    const empty = h("div", { class: "lv-empty", hidden: true });
    const el = h("section", { class: "listview", id: "listView", "aria-label": "Every assignment", hidden: true }, bar, scroll, empty);

    function collect() {
        const rows = [], order = new Map((S.profile?.controls || []).map((c, i) => [c.id, i])), bl = allBanks(), bo = new Map(bl.map((b, i) => [b, i]));
        for (const bank of Object.keys(S.pr?.eff || {})) for (const id of Object.keys(S.pr.eff[bank])) {
            if (!ctx.stage.has(id)) continue;
            const e = S.pr.eff[bank][id], a = actionDef(e.action), m = ctlMeta(id);
            let sm = String(e.summary || ""); const al = a ? a.label : "";
            if (al && sm.toLowerCase().startsWith(al.toLowerCase())) sm = sm.slice(al.length).replace(/^[:\s–-]+/, "");
            if (sm.toLowerCase() === al.toLowerCase()) sm = "";
            /* one plain phrase: a key action reads "Play/Pause" + its keycaps; a held key reads "Holds Shift"; no repeated chord or "chord" wording */
            let chord = "";
            const k = e.fields?.key?.value, lab = String(e.fields?.label?.value || "");
            if (e.action === "generic.modifierHold") { const md = MODS.find((x) => x.id === e.fields?.modifier?.value); sm = md ? `Holds ${md.name || md.label}` : "Holds a key"; }
            else if (k) { chord = String(k); sm = lab && !/chord/i.test(lab) && !lab.toLowerCase().includes(chord.toLowerCase()) ? lab : ""; }
            else sm = sm.replace(/\s*\((?:[^()]*chord[^()]*)\)/gi, "").replace(/\s+chord\b/gi, "").trim();
            rows.push({ chord, bank, id, label: plainControl(m?.label || id), kind: HOT[id]?.kind || "key", actionId: e.action, action: al || String(e.action || ""), value: plain([sm, chord].filter(Boolean).join(" ")), vtext: plain(sm), src: srcOf(e), srcKey: e.source === "app" ? (isGlobalApp() ? "global" : "app") : e.source === "global" ? "global" : "default", o: order.get(id) ?? 999, bo: bo.get(bank) ?? 99 });
        }
        return rows;
    }
    const cmp = { order: (a, b) => a.bo - b.bo || a.o - b.o, control: (a, b) => a.label.localeCompare(b.label) || a.bo - b.bo, action: (a, b) => a.action.localeCompare(b.action) || a.bo - b.bo || a.o - b.o, value: (a, b) => a.value.localeCompare(b.value) || a.bo - b.bo, source: (a, b) => a.src[1].localeCompare(b.src[1]) || a.bo - b.bo || a.o - b.o, bank: (a, b) => a.bo - b.bo || a.o - b.o };
    function sortBy(k) { if (L.sort === k) { if (L.dir === 1) L.dir = -1; else { L.sort = "order"; L.dir = 1; } } else { L.sort = k; L.dir = 1; } paint(); }
    function filtered() {
        const ql = L.q.trim().toLowerCase();
        const rows = L.rows.filter((r) => (L.bank === "all" || r.bank === L.bank) && (L.source === "all" || r.srcKey === L.source) && (!ql || (r.label + " " + r.id + " " + r.action + " " + r.value).toLowerCase().includes(ql)));
        rows.sort((a, b) => (cmp[L.sort] || cmp.order)(a, b) * L.dir);
        return rows;
    }
    function paintBankSel() {
        const bl = allBanks().filter((b) => L.rows.some((r) => r.bank === b));
        if (L.bank !== "all" && !bl.includes(L.bank)) L.bank = "all";
        const sig = bl.join(",") + "|" + L.bank;
        if (bankSlot.dataset.sig === sig) return; bankSlot.dataset.sig = sig;
        bankSel = selectField({ options: [{ value: "all", label: "All layers" }, ...bl.map((b) => ({ value: b, label: bankName(b) }))], value: L.bank, ariaLabel: "Layer", onChange: (v) => { L.bank = v; paint(); } });
        bankSlot.replaceChildren(bankSel.el);
    }
    /** the exact combination to press: held key + control as keycaps, with the page when the app has more than one */
    function pressNode(r) {
        const { page, mod, pm } = parseBank(r.bank), multi = pages().length > 1;
        return h("span", { class: "lt-press" }, h("span", { class: "tc-keys" }, ...(mod ? [h("kbd", { class: "cap sm" + (modGlyph[mod].length > 1 ? " word" : "") }, modGlyph[mod]), h("span", { class: "tc-plus" }, "+")] : pm ? [h("kbd", { class: "cap sm word" }, smodeLabel(pm)), h("span", { class: "tc-plus" }, "+")] : []), h("kbd", { class: "cap sm" + (r.label.length > 2 ? " word" : "") }, r.label)), multi ? h("span", { class: "lt-page" }, bankLabel(page)) : null);
    }
    function valueNode(r) { if (!r.value) return h("span", { class: "muted" }, "\u2014"); if (!r.chord) return isChord(r.value) ? keycaps(r.value, "sm") : h("span", { class: "lt-val" }, r.value); return h("span", { class: "lt-vals" }, r.vtext ? h("span", { class: "lt-val" }, r.vtext) : null, keycaps(r.chord, "sm")); }
    function paint() {
        const had = tbody.contains(document.activeElement);
        paintBankSel();
        L.shown = filtered();
        heads.forEach((th) => { const k = th.dataset.col, on = L.sort === k; th.setAttribute("aria-sort", on ? (L.dir === 1 ? "ascending" : "descending") : "none"); th.classList.toggle("sorted", on); th.querySelector(".lt-chev").style.transform = on && L.dir === -1 ? "rotate(180deg)" : ""; });
        const keep = L.rove && L.shown.find((r) => r.bank === L.rove.bank && r.id === L.rove.id) ? L.rove : null;
        const cur = L.shown.find((r) => r.bank === S.bank && r.id === S.control);
        const rove = keep || (cur ? { bank: cur.bank, id: cur.id } : L.shown[0] ? { bank: L.shown[0].bank, id: L.shown[0].id } : null);
        L.rove = rove;
        tbody.replaceChildren(...L.shown.map((r) => {
            const isSel = r.bank === S.bank && r.id === S.control, isRove = rove && rove.bank === r.bank && rove.id === r.id;
            const tr = h("tr", { class: "lt-row" + (isSel ? " is-sel" : ""), tabindex: isRove ? "0" : "-1", "aria-selected": String(isSel), "data-bank": r.bank, "data-id": r.id,
                "aria-label": `${r.label}, ${r.action}${r.value ? ", " + r.value : ""}, ${r.src[1]}, ${bankName(r.bank)}` },
                h("td", { class: "c-control" }, h("span", { class: "lt-ctl" }, icon((KIND[r.kind] || KIND.key)[0], 16, "lt-ic"), h("span", {}, r.label))),
                h("td", { class: "c-action" }, h("span", { class: "lt-act" }, icon(actionIcon(r.actionId), 16, "lt-ic"), h("span", {}, r.action))),
                h("td", { class: "c-value" }, valueNode(r)),
                h("td", { class: "c-source" }, h("span", { class: "lt-src" }, h("i", { class: "lt-sdot " + (r.srcKey || "default") }), h("span", {}, r.src[1]))),
                h("td", { class: "c-bank" }, pressNode(r)));
            tr.addEventListener("click", () => { L.rove = { bank: r.bank, id: r.id }; ctx.selectRow(r.bank, r.id); });
            tr.addEventListener("keydown", (e) => rowKey(e, tr, r));
            tr.addEventListener("focus", () => { if (!L.rove || L.rove.bank !== r.bank || L.rove.id !== r.id) { tbody.querySelectorAll("tr[tabindex='0']").forEach((x) => x.setAttribute("tabindex", "-1")); tr.setAttribute("tabindex", "0"); L.rove = { bank: r.bank, id: r.id }; } });
            return tr;
        }));
        if (had) tbody.querySelector("tr[tabindex='0']")?.focus({ preventScroll: true });
        const total = L.rows.length;
        count.textContent = L.shown.length === total ? `${total} assignment${total === 1 ? "" : "s"}` : `${L.shown.length} of ${total}`;
        scroll.hidden = !L.shown.length;
        empty.hidden = !!L.shown.length;
        if (!L.shown.length) empty.replaceChildren(icon("search-x", 32, "em-ic"), h("div", {}, total ? "No assignments match these filters." : "Nothing is assigned in this app yet."), total ? h("button", { type: "button", class: "btn ghost sm", onclick: () => reset() }, "Clear filters") : null);
        copyBtn.disabled = !L.shown.length;
    }
    function reset() { L.q = ""; L.bank = "all"; L.source = "all"; q.input.value = ""; q.input.dispatchEvent(new Event("input")); srcSeg.set("all"); bankSlot.dataset.sig = ""; paint(); }
    function rowKey(e, tr, r) {
        const rows = Array.from(tbody.querySelectorAll("tr")), i = rows.indexOf(tr);
        const go = (j) => { const t = rows[Math.max(0, Math.min(rows.length - 1, j))]; if (t) { e.preventDefault(); t.focus(); t.scrollIntoView({ block: "nearest" }); } };
        if (e.key === "ArrowDown") go(i + 1); else if (e.key === "ArrowUp") { if (i === 0) { e.preventDefault(); q.input.focus(); } else go(i - 1); }
        else if (e.key === "Home") go(0); else if (e.key === "End") go(rows.length - 1);
        else if (e.key === "PageDown") go(i + 8); else if (e.key === "PageUp") go(i - 8);
        else if (e.key === "Enter" || e.key === " ") { e.preventDefault(); ctx.selectRow(r.bank, r.id); }
        else if ((e.key === "Delete" || e.key === "Backspace") && !e.metaKey && !e.ctrlKey) { e.preventDefault(); ctx.unassignRow(r.bank, r.id); }
    }
    q.input.addEventListener("keydown", (e) => { if (e.key === "ArrowDown") { e.preventDefault(); tbody.querySelector("tr[tabindex='0']")?.focus(); } });
    function markdown() {
        const rows = L.shown.length ? L.shown : filtered(), esc = (t) => String(t || "").replace(/\|/g, "\\|").replace(/\n/g, " ");
        const head = `# ${appName(S.app)}: deck assignments\n\n| Control | Action | Value | Source | Press |\n| --- | --- | --- | --- | --- |\n`;
        return head + rows.map((r) => `| ${esc(r.label)} | ${esc(r.action)} | ${esc(r.value)} | ${r.src[1]} | ${esc(bankName(r.bank))} |`).join("\n") + "\n";
    }
    async function copyMarkdown() {
        const text = markdown();
        try { await navigator.clipboard.writeText(text); toast({ msg: `Copied ${L.shown.length} assignment${L.shown.length === 1 ? "" : "s"} as Markdown`, tone: "info", key: "md" }); }
        catch (e) {
            // clipboard blocked: show the text selected so it can be copied by hand
            const ta = h("textarea", { class: "lv-md", readonly: true, "aria-label": "Markdown cheat sheet" }); ta.value = text; empty.hidden = false; scroll.hidden = true; empty.replaceChildren(h("div", {}, "Copy isn’t available here. Select all and copy:"), ta, h("button", { type: "button", class: "btn sm", onclick: () => paint() }, "Back to the list")); ta.focus(); ta.select();
        }
    }
    return {
        el, refresh() { L.rows = collect(); paint(); }, copyMarkdown, markdown, rowsData: () => (L.shown.length ? L.shown : filtered()),
        focus() { const t = tbody.querySelector("tr.is-sel") || tbody.querySelector("tr[tabindex='0']"); (t || q.input).focus(); },
        get count() { return L.shown.length; },
    };
}
