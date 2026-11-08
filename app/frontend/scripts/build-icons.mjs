// build-icons.mjs: dev-time only (the app never runs this).
// Builds public/editor/assets/icons/icons.svg (one <symbol id="i-NAME"> per icon in icons.json) from the pinned
// lucide-static package and inlines the same sprite into public/editor/index.html between the icons markers.
// FAILS (exit 1) when a listed icon is missing from the pinned version, so a renamed icon is caught, not shipped blank.
// Usage: LUCIDE_DIR=/path/to/node_modules/lucide-static node scripts/build-icons.mjs
import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const editor = resolve(here, "../public/editor");
const dir = process.env.LUCIDE_DIR || resolve(here, "../node_modules/lucide-static");
if (!existsSync(join(dir, "icons"))) { console.error("lucide-static not found at " + dir + " (set LUCIDE_DIR)"); process.exit(1); }
const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
const names = JSON.parse(readFileSync(join(editor, "assets/icons/icons.json"), "utf8"));
const missing = [];
const symbols = [];
for (const n of names) {
    const f = join(dir, "icons", n + ".svg");
    if (!existsSync(f)) { missing.push(n); continue; }
    const svg = readFileSync(f, "utf8");
    const inner = svg.replace(/^[\s\S]*?<svg[^>]*>/, "").replace(/<\/svg>\s*$/, "").replace(/\s*\n\s*/g, "").trim();
    symbols.push(`<symbol id="i-${n}" viewBox="0 0 24 24">${inner}</symbol>`);
}
if (missing.length) { console.error("MISSING ICONS in lucide-static@" + pkg.version + ": " + missing.join(", ")); process.exit(1); }
const sprite = `<svg xmlns="http://www.w3.org/2000/svg" hidden aria-hidden="true">\n${symbols.join("\n")}\n</svg>\n`;
writeFileSync(join(editor, "assets/icons/icons.svg"), `<!-- lucide-static@${pkg.version} (ISC) -->\n` + sprite);
const idx = join(editor, "index.html");
let html = readFileSync(idx, "utf8");
const re = /<!--icons:start-->[\s\S]*?<!--icons:end-->/;
if (!re.test(html)) { console.error("index.html has no icons markers"); process.exit(1); }
html = html.replace(re, `<!--icons:start-->\n${sprite}<!--icons:end-->`);
writeFileSync(idx, html);
console.log(`icons: ${symbols.length} symbols from lucide-static@${pkg.version}`);
