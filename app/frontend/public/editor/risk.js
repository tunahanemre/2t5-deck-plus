// risk.js: the shared risk sheet (import, restoring a foreign backup, approving a held-back outside edit).
// One card, one grammar: what it is, how many of each kind, the lines it would run (the page scrolls, the card never does),
// a safe default (neutral), a typed-word confirmation for the dangerous option, and a quiet Cancel.
import { h, icon } from "./util.js";
import { plainControl } from "./plain.js";

/** Sum an import / restore report: per-file risk counts, program lines, panic-key overrides. */
export function riskOf(r) {
    const sum = { shell: 0, keys: 0, destructive: 0, unknown: 0, panic: 0, settings: 0 }, progs = [], pan = [];
    const add = (k) => { if (!k) return; for (const x of Object.keys(sum)) sum[x] += +k[x] || 0; };
    const prog = (c, f) => { const cmd = typeof c === "string" ? c : c && (c.commandLine || c.command || c.cmd || c.line); if (cmd) progs.push({ cmd: String(cmd), file: f }); };
    const pl = (f) => { for (const x of f.overrides || f.panicOverrides || f.panicOverridden || []) pan.push({ file: f.name || f.id, text: typeof x === "string" ? x.replace(/^[A-Za-z ]+\s+[\w-]+\./, "") : [x.control && `${plainControl(x.control)}`, x.action || x.was].filter(Boolean).join(": ") }); };
    const weak = [];
    for (const f of r.files || []) { pl(f); add(f.risk); for (const c of f.runsProgram || []) prog(c, f.name || f.id); for (const s of f.settingsChanged || []) if (s && s.weakens) weak.push({ file: f.name || f.id, key: s.key, why: s.why }); }
    if (!(r.files || []).some((f) => f.risk)) add(r.risk);
    if (Array.isArray(r.runsProgram)) for (const c of r.runsProgram) prog(c, "");
    return { sum, progs, pan, weak, any: r.needsConfirm === true || sum.shell > 0 || sum.destructive > 0 || sum.unknown > 0 || sum.panic > 0 || sum.settings > 0 || progs.length > 0, keys: sum.keys };
}

/** A count chip. Destructive wears the danger colour and a trash icon, so it never reads like the amber ones. */
export function riskChip(n, word, kind) {
    if (!n) return null;
    const ic = kind === "danger" ? "trash-2" : kind === "keys" ? "keyboard" : "triangle-alert";
    return h("span", { class: "chip rk-" + (kind || "warn") }, icon(ic, 16, "c-ic"), h("span", {}, n === true ? word : `${n} ${word}`));
}

/**
 * o: { head, lead, chips:[Node|null], notes:[string], items:[{text,raw,file,where,destructive}], itemsLabel, emptyNote,
 *      safe:{label,onClick}|null, confirm:{word,label,intro,onClick}|null, cancel:{label,caption,onClick} }
 * Returns the element. The type-to-confirm button stays disabled until the word matches exactly (case-insensitive).
 */
export function riskSheet(o) {
    const parts = [h("div", { class: "rp-h" }, icon("triangle-alert", 20), h("span", {}, o.head))];
    if (o.lead) parts.push(h("p", {}, o.lead));
    const chips = (o.chips || []).filter(Boolean);
    if (chips.length) parts.push(h("div", { class: "rp-counts" }, ...chips));
    for (const n of o.notes || []) parts.push(h("p", {}, n));
    if ((o.items || []).length) parts.push(h("ul", { class: "rp-progs", "aria-label": o.itemsLabel || "What it would do" }, ...o.items.map((p) =>
        h("li", {}, h("div", { class: "rp-line" }, h("b", {}, p.text), p.destructive ? h("span", { class: "chip rk-danger rp-tag" }, icon("trash-2", 16, "c-ic"), "Destructive") : null),
            p.raw ? h("code", {}, p.raw) : null, (p.file || p.where) ? h("span", { class: "rp-f" }, [p.file ? `In ${p.file}` : "", p.where].filter(Boolean).join(" · ")) : null))));
    else if (o.emptyNote) parts.push(h("p", { class: "muted" }, o.emptyNote));
    const act = [];
    if (o.safe) act.push(h("button", { type: "button", class: "btn sm neutral-primary", onclick: o.safe.onClick }, o.safe.label));
    if (o.confirm) {
        const typed = h("input", { type: "text", class: "tf-in", placeholder: `Type ${o.confirm.word}`, "aria-label": `Type the word ${o.confirm.word} to confirm`, autocomplete: "off", spellcheck: "false" });
        const go = h("button", { type: "button", class: "btn sm danger-solid", disabled: true, onclick: o.confirm.onClick }, icon("triangle-alert"), o.confirm.label);
        typed.addEventListener("input", () => { go.disabled = typed.value.trim().toLowerCase() !== o.confirm.word.toLowerCase(); });
        typed.addEventListener("keydown", (e) => { if (e.key === "Enter" && !go.disabled) { e.preventDefault(); go.click(); } });
        act.push(h("div", { class: "rp-type" }, h("label", {}, o.confirm.intro || "To go ahead anyway, type ", h("b", {}, o.confirm.word)), h("div", { class: "rp-typerow" }, typed, go)));
    }
    if (o.cancel) act.push(h("div", { class: "rp-cancel" }, h("button", { type: "button", class: "btn ghost", onclick: o.cancel.onClick }, o.cancel.label || "Cancel"), o.cancel.caption ? h("span", { class: "rp-cap" }, o.cancel.caption) : null));
    parts.push(h("div", { class: "rp-act" }, ...act));
    return h("div", { class: "risk-panel", role: "alert" }, ...parts);
}
