# Vendored icons
Lucide (https://lucide.dev), package `lucide-static` pinned at **1.67.0**, licence ISC (see ICONS-LICENSE.txt).
`icons.json` lists the icons in use; `node scripts/build-icons.mjs` (dev time, `LUCIDE_DIR` = path of the package) rebuilds
`icons.svg` and re-inlines the sprite into `index.html` between the icons markers. The build exits 1 if a name is missing.
Offline: nothing is fetched at runtime.
