// about.js: the brand lockup (mark + wordmark) and the body of the "About 2T5 DECK+" drawer (see drawers.js "about").
// Rules live in aboutLogic.js. Images: assets/brand/ (README.md there says where each file comes from).
import { h, icon } from "./util.js";
import { aboutModel, interceptClick, openAction, DEFAULT_LINKS } from "./aboutLogic.js";
import { api } from "./api.js";

const BRAND = "assets/brand/";

/** the one company lockup: the 2T5 mark, "FILM" as tracked text under it, a small muted "2t5.co" link under that.
 *  Used by the About view and the first-run Requirements panel (CSS: .brandlock in editor.css).
 *  The link asks the app to open it (id "site"); a refusal or a host without the route falls back to a new tab. */
export function brandLockup() {
    const site = DEFAULT_LINKS[0];
    return h("div", { class: "brandlock" },
        h("img", { class: "bl-mark", src: BRAND + "2t5-mark-white.png", alt: "2T5", width: 472, height: 388, decoding: "async", draggable: "false" }),
        h("div", { class: "bl-film" }, "FILM"),
        h("a", {
            class: "bl-url", href: site.url, target: "_blank", rel: "noopener noreferrer", "data-link": site.id,
            onclick: (e) => { if (!interceptClick(e, true)) return; e.preventDefault(); openAction(site, { open: (id) => api.openLink(id), win: (u) => window.open(u, "_blank", "noopener,noreferrer") }); },
        }, site.label));
}

/**
 * @param {{info: object|null, openLink: (id: string) => Promise<any>, openWindow?: (url: string) => void}} o
 * info is GET /api/about (null when this host has none: the built-in text is used)
 */
export function aboutBody({ info, openLink, openWindow, loading = false }) {
    const m = aboutModel(info);
    const win = openWindow || ((u) => window.open(u, "_blank", "noopener,noreferrer"));
    const links = m.links.map((l) => h("li", {}, h("a", {
        class: "about-link", href: l.url, target: "_blank", rel: "noopener noreferrer", "data-link": l.id,
        onclick: (e) => { if (!interceptClick(e, m.canOpen)) return; e.preventDefault(); openAction(l, { open: openLink, win }); },
    }, icon("external-link", 16), h("span", {}, l.label))));
    return h("div", { class: "dr-stack gap24 about" },
        brandLockup(),
        h("section", { class: "pf-sec", style: { textAlign: "center" } },
            h("h3", { class: "about-name" }, m.name),
            h("p", { class: "about-by" }, "by " + m.by),
            h("p", { class: "about-ver mono", "aria-live": "polite" }, loading ? " " : m.versionText)),
        h("section", { class: "pf-sec" }, h("h3", {}, "Links"), h("ul", { class: "about-links", "aria-label": "Links" }, ...links)),
        h("p", { class: "about-copy" }, "© 2026 2T5 Film"));
}
