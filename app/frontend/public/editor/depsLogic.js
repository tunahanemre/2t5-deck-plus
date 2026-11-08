// depsLogic.js: the pure rules behind the Requirements panel. No DOM, no network, no storage:
// the editor passes things in and persists what comes back. Tested by app/frontend/tests/depsLogic.test.mjs
// (run: node --test app/frontend/tests/).
//
// An item is a row of GET /api/dependencies: { id, required, status: ok|missing|unknown|info, fixAction, copyText, ... }.
// "dismissed" is { [id]: "missing" }: the panel was closed while that item was missing.

const asList = (items) => (Array.isArray(items) ? items.filter((x) => x && typeof x === "object" && x.id) : []);
const asMap = (m) => (m && typeof m === "object" && !Array.isArray(m) ? m : {});
const isInfo = (x) => x.informational === true || x.status === "info";
const STATUS_RANK = { missing: 0, unknown: 1, ok: 2, info: 3 };
const groupRank = (x) => (isInfo(x) ? 2 : x.required ? 0 : 1);

/** required first, then optional, then notes; inside each: missing, unknown, ok. Stable; the input is untouched. */
export function sortItems(items) {
    return asList(items).map((x, i) => [x, i]).sort((a, b) => groupRank(a[0]) - groupRank(b[0]) || (STATUS_RANK[a[0].status] ?? 1) - (STATUS_RANK[b[0].status] ?? 1) || a[1] - b[1]).map((p) => p[0]);
}

export function groupItems(items) {
    const s = sortItems(items);
    return { required: s.filter((x) => groupRank(x) === 0), optional: s.filter((x) => groupRank(x) === 1), notes: s.filter((x) => groupRank(x) === 2) };
}

/** ids of required items that are missing. "unknown" is not missing: a check that could not run never raises the panel. */
export function requiredMissing(items) {
    return asList(items).filter((x) => x.required && !isInfo(x) && x.status === "missing").map((x) => x.id);
}

/**
 * Should the panel open on its own?
 *  - first run: once, even when everything is fine, so a new user sees the whole list
 *  - a required item is missing and the panel was not already closed on it
 * A dismissal is kept only while that item stays missing. Once it is ok (or unknown, or gone) the record is dropped,
 * so ok -> missing again brings the panel back. "busy" (a drawer or the tour is open) postpones it and records nothing.
 * Returns { open, reason, fresh, dismissed } where dismissed is the pruned map to store.
 */
export function decide({ items, dismissed, firstRun, busy = false }) {
    const list = asList(items);
    const old = asMap(dismissed);
    if (!list.length) return { open: false, reason: null, fresh: [], dismissed: old };
    const missing = new Set(requiredMissing(list));
    const stillMissing = new Set(list.filter((x) => x.status === "missing").map((x) => x.id));
    const kept = {};
    for (const id of Object.keys(old)) if (stillMissing.has(id)) kept[id] = old[id];
    const worked = new Set(list.filter((x) => x.wasOK !== false).map((x) => x.id));
    const fresh = [...missing].filter((id) => !kept[id] && worked.has(id));
    if (busy) return { open: false, reason: null, fresh, dismissed: kept };
    if (fresh.length) return { open: true, reason: "required-missing", fresh, dismissed: kept };
    if (firstRun) return { open: true, reason: "first-run", fresh, dismissed: kept };
    return { open: false, reason: null, fresh, dismissed: kept };
}

/** the map to store when the user closes the panel: every required item that is missing right now stays quiet until it changes. */
export function afterDismiss(items, dismissed) {
    const out = { ...asMap(dismissed) };
    for (const id of requiredMissing(items)) out[id] = "missing";
    return out;
}

const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;

/** one calm line for the top of the panel */
export function summarize(items) {
    const list = asList(items).filter((x) => !isInfo(x));
    if (!list.length) return { tone: "unknown", text: "Could not check this Mac" };
    const req = requiredMissing(list).length;
    if (req) return { tone: "bad", text: `${plural(req, "required thing is", "required things are")} missing` };
    const opt = list.filter((x) => !x.required && x.status === "missing").length;
    if (opt) return { tone: "warn", text: `Everything required is in place. ${plural(opt, "optional thing is", "optional things are")} missing` };
    if (list.some((x) => x.required && x.status === "unknown")) return { tone: "unknown", text: "Some required checks could not run" };
    return { tone: "ok", text: "Everything checks out" };
}

/** inline "Needs X" hints for one optional feature (a tag in docs/dependencies.json: resolve, finder-tags, calibration).
 *  The words are the catalog's own item names, so no string lives in two places. Only "missing" says Needs. */
export function needsFor(items, feature) {
    return asList(items).filter((x) => !isInfo(x) && x.status === "missing" && Array.isArray(x.features) && x.features.includes(feature))
        .map((x) => ({ id: x.id, text: "Needs " + x.name, item: x }));
}

const PANES = { "open-accessibility-settings": "accessibility", "open-screen-recording-settings": "screen-recording", "open-automation-settings": "automation", "open-software-update": "software-update" };
const PANE_LABEL = { accessibility: "Open Accessibility settings", "screen-recording": "Open Screen Recording settings", automation: "Open Automation settings", "software-update": "Open Software Update" };

/** what the fix button does: { type: "open", pane, label } | { type: "copy", text, label } | null */
export function fixKind(item) {
    const a = item && item.fixAction;
    if (a && PANES[a]) return { type: "open", pane: PANES[a], label: PANE_LABEL[PANES[a]] };
    if (a === "copy-text" && item.copyText) return { type: "copy", text: item.copyText, label: "Copy the install line" };
    return null;
}
