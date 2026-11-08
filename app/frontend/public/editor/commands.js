// commands.js: the one complete command index behind Cmd+K. Every command the editor has, with its shortcut beside it.
// Commands never disappear: ones that do not apply right now are demoted (ranked last), not hidden.
import { S, banks, eff, parseBank, pages, modsFor, MODS } from "./state.js";
import { orderedApps } from "./appPicker.js";
import { reduced, bankLabel } from "./util.js";

/** fuzzy score: 0 = no match. Substring > word start > transposed typo > subsequence. */
export function score(q, text) {
    q = String(q).toLowerCase().trim(); const t = String(text).toLowerCase();
    if (!q) return 1;
    const i = t.indexOf(q);
    if (i >= 0) return 100 - Math.min(40, i * 2) + (i === 0 ? 50 : 0) + (i > 0 && /[\s\-/·]/.test(t[i - 1]) ? 25 : 0);
    for (let k = 0; k < q.length - 1; k++) { const sw = q.slice(0, k) + q[k + 1] + q[k] + q.slice(k + 2); if (t.includes(sw)) return 55; } // "opn" -> "open"
    // initials: "ot" -> "Open the library" (needs two or more letters; no loose subsequences, they only add noise)
    const initials = t.split(/[\s\-/·]+/).filter(Boolean).map((w) => w[0]).join("");
    return q.length >= 2 && initials.startsWith(q) ? 40 : 0;
}

/** host = the functions app.js offers. Returns [{key, title, sub, icon, kbd, alias, applies, run}] */
export function commandItems(host) {
    const sel = !!S.control, own = !!(sel && eff() && eff().source === "app");
    const live = host.state();
    const C = (key, title, icon, kbd, run, o = {}) => ({ key: "cmd:" + key, kind: "command", title, icon, kbd, run, sub: o.sub || "", alias: o.alias || "", applies: o.applies !== false });
    const out = [
        C("undo", "Undo", "undo-2", "Cmd+Z", host.undo, { alias: "revert back" }),
        C("redo", "Redo", "redo-2", "Cmd+Shift+Z", host.redo),
        C("history", "Open history", "history", "Cmd+Shift+H", () => host.drawer("history"), { alias: "changes log" }),
        C("library", "Open action library", "library", "Cmd+L", host.library, { alias: "actions browse" }),
        C("test", live.test ? "Turn live test off" : "Turn live test on", "activity", "Cmd+T", host.test, { alias: "test mode live input" }),
        C("lens", live.lens ? "Turn overrides lens off" : "Show overrides lens", "scan-eye", "O", host.lens, { alias: "changed from default overridden" }),
        C("callouts", live.callouts ? "Hide callouts" : "Show callouts", "eye", "L", host.callouts, { alias: "labels lines" }),
        C("view", live.view === "list" ? "Show the deck" : "Show every assignment as a list", live.view === "list" ? "layout-grid" : "list", "V", host.toggleView, { alias: "list table cheat sheet" }),
        C("markdown", "Copy the assignments as Markdown", "copy", "", host.copyMarkdown, { alias: "cheat sheet clipboard" }),
        C("addapp", "Add an app…", "plus", "", host.addApp, { alias: "new app preset" }),
        ...(S.apps.find((a) => a.id === S.app)?.cal ? [C("calibration", "Mouse calibration…", "mouse-pointer-click", "", () => host.drawer("calibration"), { alias: "calibrate mouse clicks drag targets screen layout" })] : []),
        C("profile", "App profile…", "app-window", "", () => host.drawer("profile"), { alias: "packs copy restore remove" }),
        C("export", "Export this app’s preset…", "archive", "", () => host.drawer("io"), { alias: "save bundle" }),
        C("import", "Import a preset bundle…", "file-up", "", () => host.drawer("io"), { alias: "load bundle" }),
        C("backup", "Restore a backup…", "archive", "", () => host.drawer("io"), { alias: "backups restore" }),
        C("shortcuts", "Keyboard shortcuts", "keyboard", "?", host.shortcuts),
        C("reduce", reduced() ? "Allow motion" : "Reduce motion", "zap", "", host.reduce, { alias: "animation" }),
        C("contrast", document.documentElement.getAttribute("data-contrast") === "more" ? "Normal contrast" : "High contrast", "contrast", "", host.contrast, { alias: "accessibility" }),
    ];
    if (new URLSearchParams(location.search).has("labs")) out.push(C("print", "Print cheat sheet (labs)", "printer", "", host.print, { alias: "paper pdf" }));
    if (typeof S.live?.armed === "boolean" && S.ep.arm !== false) out.push(C("arm", S.live.armed ? "Disarm the deck" : "Arm the deck", S.live.armed ? "shield-alert" : "shield-check", "", host.arm, { alias: "pause safety" }));
    out.push(C("unassign", "Unassign the selected control", "trash-2", "Delete", host.unassign, { applies: own, alias: "clear remove", sub: sel ? "" : "Select a control first" }));
    out.push(C("bank-next", "Next page", "layers", "]", () => host.stepBank(1)), C("bank-prev", "Previous page", "layers", "[", () => host.stepBank(-1)));
    orderedApps(S.apps).slice(0, 40).forEach((a, i) => { if (a.id === S.app) return; out.push(C("app:" + a.id, `Switch to ${a.isGlobal ? "Global" : a.name}`, "app-window", i < 9 ? `Cmd+${i + 1}` : "", () => host.selectApp(a.id), { alias: "app " + a.id })); });
    { const cur = parseBank(S.bank);
      pages().forEach((p) => { if (p === cur.page) return; out.push(C("page:" + p, `Go to ${bankLabel(p)} page`, "layers", "", () => host.switchPage(p), { alias: "layer bank" })); });
      modsFor(cur.page).forEach((m) => { if (m === cur.mod) return; const md = MODS.find((x) => x.id === m); out.push(C("hold:" + m, `Hold ${md.name || md.label} layer`, "layers", "", () => host.switchHold(m), { alias: "modifier held key" })); });
      if (cur.mod) out.push(C("hold:none", "Leave the held-key layer", "layers", "Escape", () => host.switchHold(null), { alias: "release modifier" })); }
    return out;
}

/** rank commands for a query (empty query: declared order, applicable first) */
export function rankCommands(items, q) {
    const scored = items.map((c, i) => { const sc = q ? Math.max(score(q, c.title), score(q, c.alias) * 0.8) : 1; return { c, i, sc: sc * (c.applies ? 1 : 0.4) }; });
    return scored.filter((x) => x.sc > 0).sort((a, b) => b.sc - a.sc || a.i - b.i).map((x) => Object.assign(x.c, { _sc: x.sc }));
}
