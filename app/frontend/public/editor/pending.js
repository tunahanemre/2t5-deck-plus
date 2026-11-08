// pending.js: risky edits made to a preset file from OUTSIDE the editor (a guest on the share, a script) that the engine
// held back. Each one is a risk sheet: what would change in plain words, the categories, Reject (safe, two taps) or Apply
// (typed word). Nothing here uses a browser dialog.
import { h, icon } from "./util.js";
import { api } from "./api.js";
import { riskSheet, riskChip } from "./risk.js";
import { appName } from "./state.js";
import { plain } from "./plain.js";

export const RISK_WORD = { shell: "runs a program", destructive: "deletes things", unknown: "unidentified action", "guard-settings": "weaker safety setting", "panic-removed": "panic key removed", "panic-override": "panic key overridden" };
const cap = (t) => { t = String(t == null ? "" : t).trim(); return t ? t.charAt(0).toUpperCase() + t.slice(1) : t; };
const ago = (t) => { const d = Math.max(0, (Date.now() - t) / 1000); return !t || Number.isNaN(d) ? "" : d < 10 ? "just now" : d < 60 ? `${Math.round(d)} s ago` : d < 3600 ? `${Math.round(d / 60)} min ago` : d < 86400 ? `${Math.round(d / 3600)} h ago` : `${Math.round(d / 86400)} d ago`; };

/** One held-back outside edit as a risk sheet. opts.done(msg) is called after it was settled. */
export function pendingSheet(p, opts = {}) {
    const risks = [...new Set(p.risks || [])], name = appName(p.preset) || p.preset || p.file || "A preset";
    const removal = p.kind === "removal";
    const chips = risks.map((r) => riskChip(true, cap(RISK_WORD[r] || plain(String(r))), r === "destructive" ? "danger" : "warn"));
    let busy = false;
    const settle = async (fn, msg) => { if (busy) return; busy = true; try { await fn(); await (opts.done && opts.done(msg)); } catch (e) { busy = false; if (opts.fail) opts.fail(e); } };
    return riskSheet({
        head: removal ? `${name}: its preset file was deleted from outside` : `${name}: its preset file was changed from outside`,
        lead: `${removal ? "The deletion" : "The change"} was not applied. The deck keeps using the version you have here.${p.file ? ` File: ${p.file}.` : ""}${p.detectedAt ? ` Found ${ago(p.detectedAt)}.` : ""}`,
        chips: chips.length ? chips : [riskChip(true, "Needs a look", "warn")],
        items: (p.changes || []).map((c) => ({ text: cap(c), destructive: false })),
        itemsLabel: `What the outside change would do to ${name}`,
        emptyNote: "The engine did not list what changed.",
        safe: { label: "Reject: put the file back", onClick: (e) => {
            const b = e.currentTarget;
            if (b.dataset.armed !== "1") { b.dataset.armed = "1"; b.textContent = "Press again to confirm: put the file back"; setTimeout(() => { if (b.isConnected) { delete b.dataset.armed; b.textContent = "Reject: put the file back"; } }, 4000); return; }
            settle(() => api.rejectExternal(p.id), `Rejected. ${p.file || "The file"} was put back as the deck has it.`);
        } },
        confirm: { word: "apply", label: removal ? "Apply the deletion" : "Apply the change", intro: "To apply it anyway, type ", onClick: () => settle(() => api.approveExternal(p.id), `Applied the outside change to ${name}. Undo brings the old version back.`) },
    });
}

/** The drawer body: every pending item, or a calm empty state. */
export function pendingBody(list, opts) {
    if (!list || !list.length) return h("div", { class: "pend-empty" }, icon("check", 24), h("p", {}, "Nothing is waiting for review."), h("p", { class: "muted" }, "Preset files that change from outside the editor and look risky are held back here."));
    return h("div", { class: "dr-stack pend-stack" }, h("p", { class: "muted" }, "These files were changed from outside the editor. They were not applied, so your deck is unchanged."), ...list.map((p) => pendingSheet(p, opts)));
}
