# Brand files

Copies (never links) of the 2T5 company mark, the same source the DIT+ app uses.
Rebuild with `tools/make_brand_assets.py <mark>`; the app icon (`app/build/appicon.png`)
comes from `tools/make_appicon.py`.

The lockup in the app is the mark plus "FILM" and "2t5.co" set as live text (see `brandLockup()` in `about.js`); there is no wordmark image.

Source (read-only), `02-BRANDINGs/00-LOGOS/00-2T5-LOGOS/` on the company Google Drive:

| Source file | What | sha256 |
|---|---|---|
| `2t5-white-transparent.png` | the 2T5 mark, white on transparent, 472x388 (byte-identical to the site's `assets/2t5-logo.png` and to `../2t5-logo.png`) | `68f541e1e8ce1d4747d640e711c3bc4e7ea7aa543ecfef4601a69b4dde9d0748` |

Files here:

| File | Made from | sha256 |
|---|---|---|
| `2t5-mark-white.png` | the mark (colour set to white, alpha kept) for dark surfaces and the app icon | `3825c5415aa503c408460a4582f956e07d66263ea03df44aeffa1f4b2d4e7010` |
| `2t5-mark-ink.png` | the mark recoloured `#0a0908`, alpha kept (light surfaces; no source file exists) | `7aac84eebb318796c9c70530564997d1ac3434554d624630329c544215d28986` |

Brand colours: ink `#0a0908`, white, accent red `#c0392b` (the editor's own UI accent stays its orange).
