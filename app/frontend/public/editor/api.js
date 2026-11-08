// api.js — thin client for the 2T5 DECK+ editor HTTP/JSON API.
// ?mock=1 swaps the network for mock.js (same endpoints). ?api=http://host:port targets a dev server.
const params = new URLSearchParams(location.search);
export const MOCK = params.has("mock");
/* ?api=<url> points the page at a dev server; it is honoured only on a loopback host and never inside the packaged app */
const DEV_HOST = /^(127\.0\.0\.1|localhost|\[::1\])$/.test(location.hostname) && !window.go && !window.wails && !window.runtime;
const BASE = DEV_HOST ? (params.get("api") || "").replace(/\/$/, "") : "";

export class ApiError extends Error {
    constructor(message, field, status, network) {
        super(message);
        this.field = field || null;
        this.status = status || 0;
        this.network = !!network;
    }
}

/* per-launch token from the packaged app (when it offers one): sent with X-LD-Editor on every mutation */
let TOKEN = "";
/* The packaged app shows this editor in an IFRAME (app/frontend/index.html). Wails binds window.go in the TOP page only,
   so inside the frame window.go.main.App is undefined: look in the parent and top windows too (same origin, so readable).
   Before this, every write (Apply the change, Arm, any edit) was refused 403 "missing or wrong X-LD-Token". */
const tokenFn = () => {
    for (const w of [window, window.parent, window.top]) {
        try { const f = w && w.go && w.go.main && w.go.main.App && w.go.main.App.EditorToken; if (typeof f === "function") return f.bind(w.go.main.App); } catch (e) { /* cross-origin parent: skip */ }
    }
    return null;
};
const tokenReady = (async () => {
    const t0 = Date.now();
    while (Date.now() - t0 < 3000) { // the Wails bindings can arrive after the module loads: retry briefly
        const f = tokenFn();
        if (f) { try { TOKEN = String((await Promise.race([f(), new Promise((_, rj) => setTimeout(() => rj(new Error("EditorToken timed out")), 2000))])) || ""); } catch (e) { clog("token lookup failed: " + (e && e.message)); } return; }
        await new Promise((r) => setTimeout(r, 100));
    }
})();
/* In-app diagnostics: the editor's own errors and the outcome of its writes go to the app log through GET /api/client-log
   (a GET on purpose: when writes are what fails inside the packaged app, a POST could not report it). Capped per session. */
let clogN = 0;
export function clog(msg) {
    if (MOCK || clogN >= 300) return;
    clogN++;
    try { fetch(BASE + "/api/client-log?m=" + encodeURIComponent(String(msg).slice(0, 400)), { cache: "no-store" }).catch(() => {}); } catch (e) { /* never break the page for a log line */ }
}
window.addEventListener("error", (e) => clog(`window error: ${e.message} @ ${String(e.filename || "").split("/").pop()}:${e.lineno}`));
window.addEventListener("unhandledrejection", (e) => clog(`unhandled rejection: ${e.reason && (e.reason.stack || e.reason.message || e.reason)}`));
/* Parent bridge: Wails binds window.go (and resolves the promises of bound calls) in the TOP page only, so from the editor's
   iframe a bound call can hang. The parent page (app/frontend/src/editorBridge.js) makes the call and answers by postMessage. */
const bridgePending = new Map();
let bridgeSeq = 0, bridgeDead = false, boundDead = false;
window.addEventListener("message", (ev) => {
    if (ev.source !== window.parent) return;
    const r = ev.data && ev.data.ldEditorRes;
    if (!r || !bridgePending.has(r.id)) return;
    const p = bridgePending.get(r.id); bridgePending.delete(r.id); clearTimeout(p.timer); p.resolve(r);
});
function bridgeCall(method, path, bodyStr, ms) {
    return new Promise((resolve, reject) => {
        const id = ++bridgeSeq;
        const timer = setTimeout(() => { bridgePending.delete(id); reject(new Error(`no answer from the parent bridge in ${ms} ms`)); }, ms);
        bridgePending.set(id, { resolve, timer });
        try { window.parent.postMessage({ ldEditorReq: { id, method, path, body: bodyStr } }, "*"); } catch (e) { clearTimeout(timer); bridgePending.delete(id); reject(e); }
    });
}
const withTimeout = (p, ms, what) => Promise.race([p, new Promise((_, rj) => setTimeout(() => rj(new Error(`${what} timed out after ${ms} ms`)), ms))]);
/* Wails v2 on macOS can lose a fetch() POST body (its NSInputStream read reports "end" before the bytes arrive), so inside the
   packaged app every write goes through the bound Go method EditorRequest (same handler, guards and token, no URL scheme in between).
   The bound object lives in the top page: look in the parent windows like the token does. */
const boundWrite = () => {
    for (const w of [window, window.parent, window.top]) {
        try { const f = w && w.go && w.go.main && w.go.main.App && w.go.main.App.EditorRequest; if (typeof f === "function") return f.bind(w.go.main.App); } catch (e) { /* cross-origin parent: skip */ }
    }
    return null;
};
/** fires after every successful write, so the chrome (undo / redo availability) can re-read the history once */
export const events = new EventTarget();
let mock = null;
async function request(method, path, body) {
    if (MOCK) {
        mock = mock || (await import("./mock.js"));
        return mock.handle(method, path, body);
    }
    let res;
    const t0 = performance.now(), ms = () => Math.round(performance.now() - t0);
    const bodyStr = body !== undefined ? JSON.stringify(body) : "";
    const asRes = (r) => ({ ok: r.status >= 200 && r.status < 300, status: r.status, statusText: "", json: async () => JSON.parse(r.body) });
    let bound = false;
    if (method !== "GET") {
        // 1) the parent page makes the bound call (works where an iframe's own call hangs); 2) the bound call from here; 3) fetch
        if (window.parent !== window && !bridgeDead) {
            try {
                const r = await bridgeCall(method, path, bodyStr, 4000);
                if (r.status) { res = asRes(r); bound = true; clog(`write ${method} ${path} via parent bridge -> ${r.status} in ${ms()} ms`); }
                else { bridgeDead = true; clog(`write ${method} ${path}: parent bridge failed (${r.error}); skipped from now on`); }
            } catch (e) { bridgeDead = true; clog(`write ${method} ${path}: ${e.message}; the bridge is skipped from now on`); }
        }
        if (!res) {
            const bw = boundDead ? null : boundWrite();
            if (bw) {
                try { const r = await withTimeout(bw(method, path, bodyStr), 4000, "direct bound call"); res = asRes(r); bound = true; clog(`write ${method} ${path} via direct bound call -> ${r.status} in ${ms()} ms`); }
                catch (e) { boundDead = true; clog(`write ${method} ${path}: direct bound call failed (${e.message}); skipped from now on`); }
            }
        }
    }
    if (!res) {
        if (method !== "GET") await Promise.race([tokenReady, new Promise((r) => setTimeout(r, 1500))]);
        const send = (keepalive) => fetch(BASE + path, {
            method,
            headers: method === "GET" ? {} : { "X-LD-Editor": "1", ...(TOKEN ? { "X-LD-Token": TOKEN } : {}), "Content-Type": "application/json" },
            body: body !== undefined ? JSON.stringify(body) : undefined,
            keepalive,
        });
        try {
            try { res = await (method === "GET" ? send(false) : withTimeout(send(true), 6000, "fetch write")); }
            catch (e) { if (method === "GET") throw e; clog(`${method} ${path} fetch failed (${e && e.message}); retrying once without keepalive`); res = await withTimeout(send(false), 6000, "fetch write"); }
        } catch (e) {
            if (method !== "GET") clog(`write ${method} ${path} failed: ${e && e.message} after ${ms()} ms`);
            throw new ApiError("The 2T5 DECK+ engine did not answer. Try again.", null, 0, true);
        }
        if (method !== "GET") clog(`write ${method} ${path} via fetch -> ${res.status} in ${ms()} ms`);
    }
    let data = null;
    try { data = await res.json(); } catch (e) { /* empty body */ }
    if (res.status === 403 && !TOKEN && !bound && (window.go || window.wails || window.parent !== window)) throw new ApiError("The editor has no security token from the app, so changes are refused. Quit and reopen 2T5 DECK+.", null, 403);
    if (!res.ok) { const er = new ApiError((data && data.error) || res.statusText, data && data.field, res.status); er.data = data; er.report = data && (data.report || (data.files ? data : null)); throw er; }
    if (method !== "GET" && !(body && body.dryRun)) events.dispatchEvent(new Event("mutated")); // a dry run only previews: it changes nothing
    return data;
}

const q = encodeURIComponent;
/** Binary / non-JSON requests (bundle export and import). Throws ApiError like request(). */
async function raw(method, path, body, type) {
    if (MOCK) throw new ApiError("Not available in mock mode.", null, 404);
    let res;
    // every mutating request carries the editor header; the zip import keeps its zip type
    if (method !== "GET") await tokenReady;
    const headers = method === "GET" ? {} : { "X-LD-Editor": "1", ...(TOKEN ? { "X-LD-Token": TOKEN } : {}), ...(type ? { "Content-Type": type } : {}) };
    try { res = await fetch(BASE + path, { method, headers, body }); }
    catch (e) { throw new ApiError("The 2T5 DECK+ engine is not reachable.", null, 0, true); }
    if (!res.ok) { let d = null; try { d = await res.json(); } catch (e) { /* not json */ } const er = new ApiError((d && d.error) || res.statusText, d && d.field, res.status); er.problems = d && d.problems; er.report = d && (d.report || (d.files ? d : null)); throw er; }
    if (method !== "GET" && !/[?&]dryRun=1/.test(path)) events.dispatchEvent(new Event("mutated"));
    return res;
}
export const calibPreviewUrl = (id, cid) => `${BASE}/api/calibration/${encodeURIComponent(id)}/capture/${encodeURIComponent(cid)}/preview.png?t=${Date.now()}`;
export const iconUrl = (id) => (MOCK ? null : `${BASE}/api/appicon/${encodeURIComponent(id)}?size=64`);

export const api = {
    deck: () => request("GET", "/api/deck"),
    registry: () => request("GET", "/api/registry"),
    apps: () => request("GET", "/api/apps"),
    addApp: (id, name, packs) => request("POST", "/api/apps", { id, name, packs }),
    capture: () => request("POST", "/api/apps/capture"),
    running: () => request("GET", "/api/running"),
    preset: (id) => request("GET", `/api/preset/${q(id)}`),
    assign: (id, bank, control, assignment) => request("PUT", `/api/preset/${q(id)}/assign`, { bank, control, assignment }),
    unassign: (id, bank, control) => request("DELETE", `/api/preset/${q(id)}/assign?bank=${q(bank)}&control=${q(control)}`),
    value: (id, bank, control, key, value) => request("PUT", `/api/preset/${q(id)}/value`, { bank, control, key, value }),
    packs: (id, packs) => request("PUT", `/api/preset/${q(id)}/packs`, { packs }),
    undo: () => request("POST", "/api/undo"),
    redo: () => request("POST", "/api/redo"),
    live: () => request("GET", "/api/live"),
    events: (since, wait) => request("GET", `/api/events?since=${since}${wait ? `&wait=${wait}` : ""}`),
    // wave B (the UI feature-detects: a 404 hides the section)
    history: () => request("GET", "/api/history"),
    jump: (index) => request("POST", "/api/history/jump", { index }),
    copyFrom: (id, body) => request("POST", `/api/preset/${q(id)}/copy`, body),
    resetDefaults: (id) => request("POST", `/api/preset/${q(id)}/reset-defaults`),
    backups: () => request("GET", "/api/backups"),
    backupNow: (label) => request("POST", "/api/backups", label ? { label } : undefined),
    backupDiff: (name) => request("GET", `/api/backups/${q(name)}/diff`),
    restoreBackup: (name, confirmRisk, skipRisky) => request("POST", `/api/backups/${q(name)}/restore${confirmRisk ? "?confirmRisk=1" : skipRisky ? "?skipRisky=1" : ""}`),
    // wave O/P: risky outside edits that were held back
    pendingExternal: () => request("GET", "/api/pending-external"),
    approveExternal: (id) => request("POST", `/api/pending-external/${q(id)}/approve`, { confirmRisk: true }),
    rejectExternal: (id) => request("POST", `/api/pending-external/${q(id)}/reject`, {}),
    exportBundle: async (id) => { const r = await raw("GET", `/api/preset/${q(id)}/export`); const cd = r.headers.get("Content-Disposition") || ""; const m = /filename="?([^";]+)"?/.exec(cd); return { blob: await r.blob(), name: m ? m[1] : "2t5-deck-plus-preset.zip" }; },
    // wave B/C/D (feature-detected; a 404 hides the feature)
    coverage: (id) => request("GET", `/api/coverage?app=${q(id)}`),
    search: (text, limit = 40) => request("GET", `/api/search?q=${q(text)}&limit=${limit}`),
    diff: (id) => request("GET", `/api/defaults/diff?app=${q(id)}`),
    warnings: () => request("GET", "/api/warnings"),
    curve: (action, fields, maxTicksPerSec = 80) => request("POST", "/api/curve", { action, fields, maxTicksPerSec }),
    setting: (id, key, value) => request("PUT", `/api/preset/${q(id)}/settings`, { key, value }),
    getArm: () => request("GET", "/api/arm"),
    /** AI access switch (a 404 = this host has none: the menu entry stays hidden) */
    aiAccess: () => request("GET", "/api/ai-access"),
    setAiAccess: (enabled) => request("PUT", "/api/ai-access", { enabled }),
    arm: (armed) => request("POST", "/api/arm", { armed }),
    /** click on Hue / Sat / Lum / Custom Mode on the deck picture: sets the engine state as a deck press would. {sliderMode?: "hue"|"sat"|"lum", customMode?: bool}; allowed while disarmed; the LEDs then follow the pushed deck-state event */
    setDeckState: (state) => request("POST", "/api/deck-state", state),
    preflight: () => request("GET", "/api/preflight"),
    /** the shared dependency list (docs/dependencies.json) with a live status per row; fresh=true re-runs the slow checks (a 404 = this host has none) */
    dependencies: (fresh) => request("GET", "/api/dependencies" + (fresh ? "?fresh=1" : "")),
    /** open one System Settings pane by name: accessibility | screen-recording | automation | software-update */
    openSettings: (pane) => request("POST", "/api/open-settings", { pane }),
    /** "About 2T5 DECK+": name, maker, version, the three links (a 404 = this host has none: built-in text is used) */
    about: () => request("GET", "/api/about"),
    /** open one of the About links in the default browser by id: site | instagram | sponsors (never a URL) */
    openLink: (id) => request("POST", "/api/open-link", { id }),
    /** this Mac has seen the Requirements screen (writes the per-Mac marker) */
    setupDone: () => request("POST", "/api/setup/done"),
    eventsLog: () => request("GET", "/api/events-log"),
    canaryAck: () => request("POST", "/api/canary/ack"),
    templates: () => request("GET", "/api/templates"),
    fromTemplate: (template, id) => request("POST", "/api/apps/from-template", { template, templateId: template, id }),
    // mouse calibration (a 404 = this host has none: the UI stays hidden)
    calibApps: () => request("GET", "/api/calibration"),
    calib: (id) => request("GET", `/api/calibration/${q(id)}`),
    calibSession: (id, scope) => request("GET", `/api/calibration/${q(id)}/session?scope=${scope}`),
    calibStart: (id, body) => request("POST", `/api/calibration/${q(id)}/capture`, body),
    calibCapture: (id, cid) => request("GET", `/api/calibration/${q(id)}/capture/${q(cid)}`),
    calibConfirm: (id, cid) => request("POST", `/api/calibration/${q(id)}/capture/${q(cid)}/confirm`),
    calibDiscard: (id, cid) => request("POST", `/api/calibration/${q(id)}/capture/${q(cid)}/discard`),
    calibTest: (id, target, mode, confirm) => request("POST", `/api/calibration/${q(id)}/test`, { target, mode, ...(confirm ? { confirm: true } : {}) }),
    calibClear: (id, key, entry) => request("DELETE", `/api/calibration/${q(id)}/target/${q(key)}${entry ? `?entry=${q(entry)}` : ""}`),
    calibOptions: (id, body) => request("PUT", `/api/calibration/${q(id)}/options`, body),
    removeApp: (id) => request("DELETE", `/api/apps/${q(id)}`),
    restoreLastGood: (id) => request("POST", `/api/preset/${q(id)}/restore-last-good`),
    /** HTTP import in two steps: dry=true previews and returns a one-use token; then apply with {token, confirmRisk | skipRisky} (no body). */
    importBundle: async (file, dry, confirmRisk, skipRisky, token) => {
        if (MOCK) { mock = mock || (await import("./mock.js")); return mock.importReport(dry, confirmRisk, skipRisky); }
        if (dry) return (await raw("POST", "/api/import?dryRun=1" + (skipRisky ? "&skipRisky=1" : ""), file, "application/zip")).json();
        const qs = [token ? `token=${q(token)}` : "", confirmRisk ? "confirmRisk=1" : "", skipRisky ? "skipRisky=1" : ""].filter(Boolean).join("&");
        return (await raw("POST", "/api/import" + (qs ? "?" + qs : ""), token ? undefined : file, token ? undefined : "application/zip")).json();
    },
};
