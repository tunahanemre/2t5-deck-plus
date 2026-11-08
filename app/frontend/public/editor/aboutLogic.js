// aboutLogic.js: the pure rules behind "About 2T5 DECK+". No DOM, no network.
// Tested by app/frontend/tests/aboutLogic.test.mjs (run: node --test app/frontend/tests/).
//
// The packaged window (Wails) cannot follow a target=_blank link, so a click asks the app to open it
// (POST /api/open-link {id}, which only knows three ids). Where the host cannot (dev server without an
// opener, mock, a refusal) the link is a normal anchor with rel=noopener noreferrer, so it still works.

export const DEFAULT_LINKS = [
    { id: "site", label: "2t5.co", url: "https://2t5.co" },
    { id: "instagram", label: "instagram.com/tunahanemre", url: "https://instagram.com/tunahanemre" },
    { id: "sponsors", label: "Donate", url: "https://github.com/sponsors/tunahanemre" },
];

const safeLink = (l) => l && typeof l === "object" && typeof l.id === "string" && l.id && typeof l.label === "string" && typeof l.url === "string" && /^https:\/\//.test(l.url);

/** what the view shows, from GET /api/about (or null when the host has no such route) */
export function aboutModel(info) {
    const i = info && typeof info === "object" ? info : {};
    const v = typeof i.version === "string" ? i.version.trim() : "";
    const links = Array.isArray(i.links) ? i.links.filter(safeLink) : [];
    return {
        name: typeof i.name === "string" && i.name ? i.name : "2T5 DECK+",
        by: typeof i.by === "string" && i.by ? i.by : "2T5 Film",
        versionText: v ? "Version " + v : "Version unknown",
        canOpen: i.canOpen === true,
        links: links.length ? links : DEFAULT_LINKS.map((l) => ({ ...l })),
    };
}

/** should a click on a link be handled by the app (preventDefault + ask the host) instead of the browser? */
export function interceptClick(ev, canOpen) {
    if (!canOpen || ev.defaultPrevented) return false;
    if (ev.button !== 0) return false;
    return !(ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey);
}

/** open one link: the app first, a new tab if that fails. Returns "app" | "window". */
export async function openAction(link, { open, win }) {
    try { await open(link.id); return "app"; }
    catch (e) { win(link.url); return "window"; }
}
