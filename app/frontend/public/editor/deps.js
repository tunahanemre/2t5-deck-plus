// deps.js: the Requirements panel body (a non-modal right drawer, see drawers.js "setup").
// Rows come from GET /api/dependencies, which is docs/dependencies.json plus a live status.
// The rules (order, when the panel opens by itself, what the fix button does) live in depsLogic.js.
import { h, icon } from "./util.js";
import { groupItems, summarize, fixKind } from "./depsLogic.js";
import { brandLockup } from "./about.js";

const WORD = { ok: "Ready", missing: "Missing", unknown: "Not checked", info: "Note" };
const GLYPH = { ok: ["check", "pf-ok"], missing: ["triangle-alert", "pf-bad"], unknown: ["circle-dashed", "pf-un"], info: ["info", "pf-un"] };

function row(item, onFix) {
    const st = item.status in WORD ? item.status : "unknown";
    const fix = st === "ok" || st === "info" ? null : fixKind(item);
    const open = st === "missing";
    const why = [
        item.detail && st !== "ok" ? h("p", { class: "dep-detail" }, item.detail) : null,
        h("p", {}, h("b", {}, "How to get it: "), item.howToGet),
        h("p", {}, h("b", {}, "Without it: "), item.without),

    ];
    const line = fix && fix.type === "copy" ? h("div", { class: "copyline" }, h("code", {}, fix.text)) : null;
    const fixBtn = fix ? h("button", { type: "button", class: "btn sm dep-fix", onclick: (e) => onFix(item, fix, e.currentTarget) }, icon(fix.type === "copy" ? "copy" : "external-link", 16), fix.label) : null;
    return h("div", { class: "dep-row " + st, role: "listitem", "data-dep": item.id },
        icon(GLYPH[st][0], 16, GLYPH[st][1]),
        h("div", { class: "pf-txt" },
            h("div", { class: "dep-h" }, h("b", {}, item.name), h("span", { class: "dep-st " + st }, WORD[st])),
            h("span", {}, "For: " + item.feature),
            st === "ok" && item.detail ? h("span", { class: "dep-ok-detail" }, item.detail) : null,
            ...(open ? why : [h("details", { class: "dep-more" }, h("summary", {}, st === "ok" ? "Details" : "What this means"), ...why)]),
            h("details", { class: "dep-more" }, h("summary", {}, "How it is checked"), h("p", {}, item.howToCheck)),
            line, fixBtn));
}

/**
 * @param {{items: object[]|null, error?: string, onFix: Function, onRecheck: Function, busy?: boolean}} o
 */
export function depsBody({ items, error, onFix, onRecheck, busy }) {
    const wrap = h("div", { class: "dr-stack gap24 dep-panel" });
    if (!items) {
        wrap.append(h("p", { class: "muted" }, error || "Checking this Mac…"));
        return wrap;
    }
    const sum = summarize(items), g = groupItems(items);
    wrap.append(brandLockup());
    wrap.append(h("section", { class: "pf-sec" },
        h("div", { class: "dep-sum " + sum.tone, role: "status", "aria-live": "polite" }, h("i", { class: "pdot" + (sum.tone === "ok" ? " ok" : "") }), h("b", {}, sum.text)),
        h("p", { class: "muted" }, "2T5 DECK+ checks these by itself and tells you when something goes missing. Required ones first."),
        h("button", { type: "button", class: "btn sm", disabled: !!busy, onclick: () => onRecheck() }, icon("rotate-ccw", 16), busy ? "Checking…" : "Check again")));
    const group = (title, sub, list) => list.length ? h("section", { class: "pf-sec" }, h("h3", {}, title), sub ? h("p", { class: "muted" }, sub) : null,
        h("div", { class: "dep-list", role: "list", "aria-label": title }, ...list.map((x) => row(x, onFix)))) : null;
    wrap.append(...[
        group("Required", "The deck does not work properly without these.", g.required),
        group("Optional", "Each one switches on a feature. The deck still works without it.", g.optional),
        group("Good to know", null, g.notes),
    ].filter(Boolean));
    return wrap;
}
