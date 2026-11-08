#!/usr/bin/env python3
"""Fix the calibration-key legend on the rendered deck art: "--/Cal" -> "--/Col".

The real Loupedeck+ prints "--/Col" (letter o). The rendered art printed an "a".
Only that one letter changes. Run from anywhere:

    python3 tools/fix_cal_legend.py            # patch both PNGs (no-op if already fixed)
    python3 tools/fix_cal_legend.py --force    # patch even if the "o" is detected already
    python3 tools/fix_cal_legend.py --check    # report state only, write nothing
    python3 tools/fix_cal_legend.py --preview DIR   # also save before/after 6x crops

What it does (Pillow + numpy only):
  1. deck-cutout-2x.png (3000x3000): the legend is fitted to Helvetica Neue Regular at
     23.75 px, origin x=241.75, baseline y=1684.5 (fit against the existing "C" and "l"
     glyphs; same Lanczos-downsampled antialiasing, so strokes and ringing match).
  2. The old "a" is removed by rebuilding the key surface in its slot (x 259..271,
     y 1664..1687): row-wise linear interpolation between clean strips left and right
     of the word, plus the key's own dither texture copied from the clean rows above.
  3. A rendered "o" (same face, size, baseline, colour) is composited into the slot.
     Spacing to "C" and "l" is the font's own advance (o and a share the 0.556 em advance,
     so "l" does not move).
  4. deck-cutout.png (1500x1500) is NOT an exact Lanczos downscale of the 2x (it differs
     by up to ~23 levels elsewhere), so it is patched directly: the change of the 2x
     patch, Lanczos-downscaled, is added to the existing 1x pixels. Pixels outside the
     word are untouched, alpha is never written (the key is fully opaque).
Idempotent: the slot is scored against the modelled "a" and "o"; if "o" fits better the
art is already fixed and nothing is written (unless --force).
"""
import argparse
import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw, ImageFont

ROOT = Path(__file__).resolve().parent.parent
ASSETS = ROOT / "app/frontend/public/editor/assets"
F2 = ASSETS / "deck-cutout-2x.png"
F1 = ASSETS / "deck-cutout.png"

FONT = ("/System/Library/Fonts/HelveticaNeue.ttc", 0)  # Helvetica Neue Regular
SIZE = 23.75
# word crop (2x px) the fit was done in; origin/baseline are relative to its top-left
CX0, CX1, CY0, CY1 = 236, 282, 1660, 1692
OX, OY = 5.75, 24.5
SS = 8  # supersampling for glyph rendering
# slot of the old "a" (inclusive x, inclusive y) in absolute 2x pixels
SLOT_X0, SLOT_X1 = 259, 271
SLOT_Y0, SLOT_Y1 = 1664, 1687


def cov_of(text):
    """Coverage map (0..1, may ring slightly negative) of `text` over the word crop."""
    w, h = CX1 - CX0, CY1 - CY0
    f = ImageFont.truetype(FONT[0], SIZE * SS, index=FONT[1])
    im = Image.new("L", (w * SS, h * SS), 0)
    ImageDraw.Draw(im).text((OX * SS, OY * SS), text, font=f, fill=255, anchor="ls")
    im = im.convert("F").resize((w, h), Image.LANCZOS)
    return np.asarray(im, dtype=np.float64) / 255.0


def surface(rgb):
    """Smooth key surface (no glyphs) over the word crop, from clean strips left/right."""
    crop = rgb[CY0:CY1, CX0:CX1, :].astype(np.float64)
    w = CX1 - CX0
    left = crop[:, 0:5, :].mean(1)
    right = crop[:, 42:46, :].mean(1)
    t = ((np.arange(w) - 2) / (43.5 - 2))[None, :, None]
    return left[:, None, :] + (right - left)[:, None, :] * t


def dither_texture(rgb):
    """Row-demeaned dither texture from clean key surface just above the word (slot columns)."""
    h = SLOT_Y1 - SLOT_Y0 + 1
    blk = rgb[SLOT_Y0 - 30:SLOT_Y0 - 30 + h, SLOT_X0:SLOT_X1 + 1, :].astype(np.float64)
    return blk - blk.mean(1, keepdims=True)


def state(rgb):
    """Return (looks_fixed, err_a, err_o, gains)."""
    crop = rgb[CY0:CY1, CX0:CX1, :].astype(np.float64)
    d = crop - surface(rgb)
    cov_c = cov_of("Cal")
    mask = np.ones(cov_c.shape, bool)
    mask[:, 23:36] = False
    mask[:, 0:6] = False
    mask[:, 41:] = False
    gains = []
    for ch in range(3):
        c, o = cov_c[mask], d[..., ch][mask]
        gains.append(float((c * o).sum() / (c * c).sum()))
    gains = np.array(gains)
    sx0, sx1 = SLOT_X0 - CX0, SLOT_X1 - CX0 + 1
    sy0, sy1 = SLOT_Y0 - CY0, SLOT_Y1 - CY0 + 1
    ea = eo = 0.0
    for ch in range(3):
        obs = d[sy0:sy1, sx0:sx1, ch]
        ea += ((obs - gains[ch] * cov_of("Cal")[sy0:sy1, sx0:sx1]) ** 2).mean()
        eo += ((obs - gains[ch] * cov_of("Col")[sy0:sy1, sx0:sx1]) ** 2).mean()
    return eo < ea, ea, eo, gains


def patch_2x(rgb):
    """Return a copy of the 2x RGB with the 'a' replaced by 'o'."""
    ok, ea, eo, gains = state(rgb)
    out = rgb.astype(np.float64).copy()
    sx0, sx1 = SLOT_X0 - CX0, SLOT_X1 - CX0 + 1
    sy0, sy1 = SLOT_Y0 - CY0, SLOT_Y1 - CY0 + 1
    bg = surface(rgb)[sy0:sy1, sx0:sx1, :] + dither_texture(rgb)
    # isolate the "o": "Co" minus "C" (same origin, so the o sits at the font's own advance)
    cov_o_only = cov_of("Co") - cov_of("C")
    # the new glyph must stay inside its slot (otherwise it would touch C or l)
    leak = np.abs(cov_o_only[:, :sx0]).max()
    leak_r = np.abs(cov_o_only[:, sx1:]).max()
    if max(leak, leak_r) > 0.10:
        sys.exit("new 'o' leaks outside its slot (%.3f / %.3f): refit needed" % (leak, leak_r))
    seg = cov_o_only[sy0:sy1, sx0:sx1]
    for ch in range(3):
        out[SLOT_Y0:SLOT_Y1 + 1, SLOT_X0:SLOT_X1 + 1, ch] = bg[..., ch] + gains[ch] * seg
    return np.clip(np.rint(out), 0, 255).astype(np.uint8)


def to_1x_delta(old2, new2, rgb1):
    """Add the Lanczos-downscaled change of the 2x patch to the 1x pixels (alpha untouched)."""
    x0, x1, y0, y1 = 248, 284, 1654, 1698  # even, covers the slot + Lanczos support (6 px)
    res = []
    for arr in (old2, new2):
        c = Image.fromarray(arr[y0:y1, x0:x1, :])
        res.append(np.asarray(c.resize(((x1 - x0) // 2, (y1 - y0) // 2), Image.LANCZOS)).astype(np.int32))
    # Lanczos on a crop is identical to the full-image result for pixels > 3 dst px from the edge;
    # outside the patch the two crops are identical, so the delta is exactly 0 there.
    delta = res[1] - res[0]
    out = rgb1.astype(np.int32).copy()
    X, Y = x0 // 2, y0 // 2
    out[Y:Y + delta.shape[0], X:X + delta.shape[1], :] += delta
    return np.clip(out, 0, 255).astype(np.uint8)


def crop6(img, box):
    x0, y0, x1, y1 = box
    return img.crop(box).resize(((x1 - x0) * 6, (y1 - y0) * 6), Image.NEAREST)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--force", action="store_true", help="patch even if already detected as fixed")
    ap.add_argument("--check", action="store_true", help="report only, write nothing")
    ap.add_argument("--preview", metavar="DIR", help="save before/after 6x crops of the key")
    ap.add_argument("--assets", metavar="DIR", help="assets folder to work on (default: the repo's)")
    args = ap.parse_args()
    global F1, F2
    if args.assets:
        F2, F1 = Path(args.assets) / F2.name, Path(args.assets) / F1.name

    im2 = Image.open(F2)
    im1 = Image.open(F1)
    assert im2.mode == "RGBA" and im2.size == (3000, 3000), im2
    assert im1.mode == "RGBA" and im1.size == (1500, 1500), im1
    a2 = np.asarray(im2)
    a1 = np.asarray(im1)
    fixed, ea, eo, gains = state(a2[..., :3])
    print("slot fit error: 'a'=%.1f  'o'=%.1f  -> %s" % (ea, eo, "already 'Col'" if fixed else "reads 'Cal'"))
    if fixed and not args.force:
        print("nothing to do (use --force to re-patch)")
        return
    if args.check:
        return

    new2_rgb = patch_2x(a2[..., :3])
    new1_rgb = to_1x_delta(a2[..., :3], new2_rgb, a1[..., :3])
    n2 = np.concatenate([new2_rgb, a2[..., 3:]], axis=2)
    n1 = np.concatenate([new1_rgb, a1[..., 3:]], axis=2)
    assert (n2[..., 3] == a2[..., 3]).all() and (n1[..., 3] == a1[..., 3]).all()

    if args.preview:
        pd = Path(args.preview)
        pd.mkdir(parents=True, exist_ok=True)
        crop6(im2, (150, 1590, 420, 1720)).save(pd / "key2x_before.png")
        crop6(Image.fromarray(n2), (150, 1590, 420, 1720)).save(pd / "key2x_after.png")
        crop6(im1, (75, 795, 210, 860)).save(pd / "key1x_before.png")
        crop6(Image.fromarray(n1), (75, 795, 210, 860)).save(pd / "key1x_after.png")

    Image.fromarray(n2, "RGBA").save(F2, optimize=True)
    Image.fromarray(n1, "RGBA").save(F1, optimize=True)
    print("patched", F2.name, "and", F1.name)


if __name__ == "__main__":
    main()
