#!/usr/bin/env python3
"""Repair the printed legends the 2x upscale hallucinated in the deck cut-out, then rebuild the 1x.

    python3 tools/fix_legends.py            # patch the four repo PNGs in place
    python3 tools/fix_legends.py --out DIR  # write deck-cutout-2x.png / deck-cutout.png into DIR instead

What was wrong (audit of every legend against assets/deck-photo-amazon/deck.jpg, 2026-11-08): the 2x showed
"Controst", "Clority", "Control Dial" as "Cantral Dial", "--/Cal" for "--/Col", and "C5" as "CS". Every other legend matched.

Method: each wrong glyph is replaced by a clean glyph of the SAME font, size and baseline that the 2x already draws
correctly elsewhere (an 'a' and an 'o' from "Shadows", the 'o' of "Copy", the '5' of "P5").
Only the glyph's additive light (pixel minus its smooth local background) is moved; the background under the wrong
glyph is rebuilt from its surroundings, so the surface, shading, shadows and neighbouring letters stay as they were.
Every change is confined to the boxes below (+2 px). The 1x is the same premultiplied Lanczos downscale as before,
applied only around the patched boxes, so every other 1x pixel stays byte-identical.
Idempotent: re-running on a patched file yields the same result. Boxes are inclusive 2x pixel coordinates.
"""
import argparse, os
import numpy as np
from PIL import Image
from scipy import ndimage as ndi

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ED = os.path.join(ROOT, "app/frontend/public/editor/assets")
AM = os.path.join(ROOT, "assets/deck-photo-amazon")

# name: (src x0, x1, y0, y1)  ->  (dst x0, x1, y0, y1)   the glyph to copy, and the wrong glyph it replaces
FIXES = {
    "Contrast  o->a": ((1802, 1817, 1724, 1743), (1186, 1201, 1724, 1743)),   # 'a' of Shadows
    "Clarity   o->a": ((1802, 1817, 1724, 1743), (1414, 1429, 1724, 1743)),
    "Control#2  a->o": ((1838, 1853, 1724, 1743), (516, 533, 1594, 1613)),    # 'o' of Shadows (same size, baseline 130 px higher)
    "Control#6  a->o": ((1838, 1853, 1724, 1743), (572, 588, 1594, 1613)),
    "Col       a->o": ((224, 239, 1897, 1916), (265, 280, 1671, 1690)),       # 'o' of Copy
    "C5        S->5": ((1707, 1721, 1054, 1079), (2436, 2450, 1769, 1794)),   # '5' of P5
}
PAD = 2


def bg_est(img, x0, x1, y0, y1):
    """Smooth background of the box (padded) from the dark pixels around it, per channel."""
    X0, X1, Y0, Y1 = x0 - PAD, x1 + PAD, y0 - PAD, y1 + PAD
    m = 5
    sub = img[Y0 - m:Y1 + m + 1, X0 - m:X1 + m + 1]
    valid = np.ones(sub.shape[:2], bool)
    valid[m:-m, m:-m] = False
    valid &= sub.mean(2) < 80
    w = valid.astype(float)
    num = np.stack([ndi.gaussian_filter(sub[..., c] * w, 3) for c in range(3)], -1)
    den = np.maximum(ndi.gaussian_filter(w, 3), 1e-6)[..., None]
    return (num / den)[m:-m, m:-m]


def patch(rgb):
    out = rgb.copy()
    changed = np.zeros(rgb.shape[:2], bool)
    for name, ((sx0, sx1, sy0, sy1), (dx0, dx1, dy0, dy1)) in FIXES.items():
        sw, dw = sx1 - sx0 + 1, dx1 - dx0 + 1
        sbg = bg_est(rgb, sx0, sx1, sy0, sy1)
        delta = np.maximum(rgb[sy0 - PAD:sy1 + PAD + 1, sx0 - PAD:sx1 + PAD + 1] - sbg, 0)   # a legend only ever adds light
        # keep only the glyph's own columns (half weight on the 1 px skirt, nothing beyond)
        cw = np.zeros(delta.shape[1]); cw[PAD:PAD + sw] = 1; cw[PAD - 1] = cw[PAD + sw] = 0.5
        delta = delta * cw[None, :, None]
        # centre the glyph in the destination box when the widths differ
        off = (dw - sw) // 2
        place = np.zeros((dy1 - dy0 + 1 + 2 * PAD, dx1 - dx0 + 1 + 2 * PAD, 3))
        h, w = delta.shape[:2]
        place[:h, off:off + w] = delta[:place.shape[0], :place.shape[1] - off]
        dbg = bg_est(rgb, dx0, dx1, dy0, dy1)
        new = dbg + place
        ww = np.zeros(new.shape[:2]); ww[:] = 1
        ww[:, :PAD - 1] = 0; ww[:, -(PAD - 1):] = 0
        ww[:, PAD - 1] = ww[:, -PAD] = 0.5
        Y0, X0 = dy0 - PAD, dx0 - PAD
        sl = (slice(Y0, Y0 + new.shape[0]), slice(X0, X0 + new.shape[1]))
        out[sl] = out[sl] * (1 - ww[..., None]) + new * ww[..., None]
        changed[sl] |= ww > 0
    return np.clip(np.rint(out), 0, 255).astype("uint8"), changed


def downscale(rgba2):
    """Premultiplied Lanczos 3000 -> 1500 (the step the original 1x used)."""
    a = rgba2[..., 3:4].astype(float) / 255.0
    pm = Image.fromarray(np.clip(np.rint(rgba2[..., :3] * a), 0, 255).astype("uint8"))
    pm1 = np.array(pm.resize((1500, 1500), Image.LANCZOS)).astype(float)
    a1 = np.array(Image.fromarray(rgba2[..., 3]).resize((1500, 1500), Image.LANCZOS)).astype(float) / 255.0
    rgb1 = np.where(a1[..., None] > 0.003, pm1 / np.maximum(a1[..., None], 1e-3), pm1)
    return np.dstack([np.clip(np.rint(rgb1), 0, 255), np.rint(a1 * 255)]).astype("uint8")


def main():
    ap = argparse.ArgumentParser(); ap.add_argument("--out"); args = ap.parse_args()
    p2 = os.path.join(ED, "deck-cutout-2x.png"); p1 = os.path.join(ED, "deck-cutout.png")
    im2 = np.array(Image.open(p2).convert("RGBA"))
    rgb, changed = patch(im2[..., :3].astype(float))
    new2 = np.dstack([rgb, im2[..., 3]])
    # 1x: recompute only within 4 px (1x) of the patched boxes, keep every other pixel as it is
    old1 = np.array(Image.open(p1).convert("RGBA"))
    near = ndi.binary_dilation(changed, iterations=8)
    near1 = np.array(Image.fromarray((near * 255).astype("uint8")).resize((1500, 1500), Image.BOX)) > 0
    new1 = old1.copy(); full1 = downscale(new2)
    new1[near1] = full1[near1]
    if args.out:
        os.makedirs(args.out, exist_ok=True)
        Image.fromarray(new2, "RGBA").save(os.path.join(args.out, "deck-cutout-2x.png"), optimize=True)
        Image.fromarray(new1, "RGBA").save(os.path.join(args.out, "deck-cutout.png"), optimize=True)
    else:
        for d, n1, n2 in ((ED, "deck-cutout.png", "deck-cutout-2x.png"), (AM, "deck-cutout.png", "deck-cutout@2x.png")):
            Image.fromarray(new2, "RGBA").save(os.path.join(d, n2), optimize=True)
            Image.fromarray(new1, "RGBA").save(os.path.join(d, n1), optimize=True)
    print("patched 2x px:", int(changed.sum()), "| 1x px recomputed:", int(near1.sum()))


if __name__ == "__main__":
    main()
