// editorBridge.js: the PARENT side of the editor's write bridge. The editor runs in an iframe, and Wails binds
// window.go only in the top page (its promise callbacks are resolved there too), so the iframe asks the top page,
// by postMessage, to make the bound Go call and send the answer back. Only messages from the editor frame itself
// are served. Every call is answered within TIMEOUT_MS, even when the bound call never settles.
const TIMEOUT_MS = 3500;

export function installEditorBridge(frame, getApp = () => window.go && window.go.main && window.go.main.App) {
    const reply = (src, origin, msg) => {
        try { src.postMessage({ ldEditorRes: msg }, origin && origin !== "null" ? origin : "*"); } catch (e) { /* the frame is gone */ }
    };
    window.addEventListener("message", async (ev) => {
        const m = ev.data && ev.data.ldEditorReq;
        if (!m || !frame.contentWindow || ev.source !== frame.contentWindow) return;
        const { id, method, path, body } = m;
        const app = getApp();
        if (!app || typeof app.EditorRequest !== "function") {
            reply(ev.source, ev.origin, { id, status: 0, error: "parent has no EditorRequest binding" });
            return;
        }
        let done = false;
        const timer = setTimeout(() => { if (!done) { done = true; reply(ev.source, ev.origin, { id, status: 0, error: `bound call timed out after ${TIMEOUT_MS} ms` }); } }, TIMEOUT_MS);
        try {
            const r = await app.EditorRequest(String(method), String(path), String(body || ""));
            if (!done) { done = true; clearTimeout(timer); reply(ev.source, ev.origin, { id, status: r.status, body: r.body }); }
        } catch (e) {
            if (!done) { done = true; clearTimeout(timer); reply(ev.source, ev.origin, { id, status: 0, error: String((e && e.message) || e) }); }
        }
    });
    // the editor asks once whether a bridge exists (no answer = no bridge, it falls back)
    window.addEventListener("message", (ev) => {
        if (ev.data && ev.data.ldEditorPing && frame.contentWindow && ev.source === frame.contentWindow) {
            reply(ev.source, ev.origin, { pong: true });
        }
    });
}
