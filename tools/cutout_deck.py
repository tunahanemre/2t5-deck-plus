#!/usr/bin/env python3
"""Rebuild the transparent Loupedeck+ cut-out from the ORIGINAL download (white background).

    python3 tools/cutout_deck.py            # writes the editor assets
    python3 tools/cutout_deck.py --crops D  # also writes 400% edge/corner crops (before = files on disk, after = new) into D

Source (never modified): assets/deck-photo-amazon/deck.jpg  (1500x1500, case on pure white, no shadow)
Outputs:
  app/frontend/public/editor/assets/deck-cutout.png      1500x1500 RGBA
  app/frontend/public/editor/assets/deck-cutout-2x.png   3000x3000 RGBA
  app/frontend/public/editor/assets/deck.jpg             source + the Custom Mode LED painted out
  assets/deck-photo-amazon/deck-cutout.png, deck-cutout@2x.png   copies of the two PNGs

Method (why it looks clean on a dark stage):
  1. The Custom Mode LED is baked LIT in the photo: it is painted out first (the dark, unlit Hue LED's relief is copied onto
     the local surface colour, estimated from the left half of a ring so the glow does not skew it).
  2. Exterior = near-white pixels connected to the image border. Only the largest object survives (specks removed).
  3. Soft alpha at the rim by matting against the known white: P = a*F + (1-a)*W, F = the colour of the nearest definite
     interior pixel. a = (W-P)/(W-F), taken on the channel with the most contrast; hairline values (< 0.05) are dropped.
  4. Colour decontamination: C = (P-(1-a)*W)/a, so no white matte stays in the edge pixels; where a is too small to
     divide, the nearest interior colour is used and also extended 3 px outward, so the feather blends dark into dark.
  5. Alpha feather: a 0.6 px gaussian on the band only (smooth, anti-aliased edge, no stairs), interior stays 1.
  6. 2x: premultiplied Lanczos upscale of the cleaned 1x (colour and alpha), so the edge stays consistent at both sizes.
"""
import argparse, os, sys
import numpy as np
from PIL import Image
from scipy import ndimage as ndi

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "assets/deck-photo-amazon/deck.jpg")
ED = os.path.join(ROOT, "app/frontend/public/editor/assets")
AM = os.path.join(ROOT, "assets/deck-photo-amazon")
LED_LIT, LED_REF = (230, 545), (507, 548)   # photo px: the lit Custom Mode LED, the unlit Hue LED used as the reference relief


def paint_out_led(rgb):
    a = rgb.astype(float)
    R, Rd = 24, 12
    yy, xx = np.mgrid[-R:R + 1, -R:R + 1]
    d = np.hypot(xx, yy)
    x, y = LED_LIT
    blk = a[y - R:y + R + 1, x - R:x + R + 1].copy()
    ring = (d >= R - 3) & (d <= R) & (xx <= 0)
    bg = blk[ring].mean(0)
    sx, sy = LED_REF
    sb = a[sy - Rd:sy + Rd + 1, sx - Rd:sx + Rd + 1]
    d2 = np.hypot(*np.mgrid[-Rd:Rd + 1, -Rd:Rd + 1])
    delta = sb - sb[d2 >= Rd - 2].mean(0)
    w = np.clip((R - 2 - d) / 6, 0, 1)[..., None]
    new = np.tile(bg, (2 * R + 1, 2 * R + 1, 1))
    new[R - Rd:R + Rd + 1, R - Rd:R + Rd + 1] += delta * (d2 <= Rd - 1)[..., None]
    a[y - R:y + R + 1, x - R:x + R + 1] = blk * (1 - w) + new * w
    return np.clip(a, 0, 255)


def matte(rgb):
    """rgb float HxWx3 (LED already painted out) -> RGBA float, straight alpha."""
    W = 255.0
    L = rgb.min(2)                                   # a pixel is background only if every channel is near white
    bgish = L >= 246
    lab, n = ndi.label(bgish)
    border = set(np.unique(np.concatenate([lab[0], lab[-1], lab[:, 0], lab[:, -1]]))) - {0}
    ext = np.isin(lab, list(border))
    solid = ~ext
    # keep only the largest object (specks, dust and leftover white-ish islands go)
    lab2, n2 = ndi.label(solid)
    sizes = ndi.sum(solid, lab2, range(1, n2 + 1))
    solid = lab2 == (1 + int(np.argmax(sizes)))
    solid = ndi.binary_fill_holes(solid)
    ext = ~solid
    # definite interior: more than 3 px from the exterior
    dist = ndi.distance_transform_edt(solid)
    inner = dist > 3
    idx = ndi.distance_transform_edt(~inner, return_distances=False, return_indices=True)
    F = rgb[idx[0], idx[1]]                           # nearest definite-interior colour for every pixel
    band = ndi.binary_dilation(ext, iterations=3) & ~inner   # rim: last 3 px of the object and 3 px of the outside
    # alpha from the contrast channel
    contrast = W - F
    ch = np.argmax(contrast, axis=2)
    ii, jj = np.indices(ch.shape)
    Pc, Fc = rgb[ii, jj, ch], F[ii, jj, ch]
    a = np.clip((W - Pc) / np.maximum(W - Fc, 1.0), 0, 1)
    a[a < 0.05] = 0
    a[inner] = 1
    a[ext & ~band] = 0
    # decontaminate colour
    C = (rgb - (1 - a[..., None]) * W) / np.maximum(a[..., None], 1e-3)
    use = (a > 0.2)[..., None]
    out = np.where(use, np.clip(C, 0, 255), F)
    out[inner] = rgb[inner]
    # feather the band
    a_s = ndi.gaussian_filter(a, 0.6)
    a2 = np.where(inner, 1.0, a_s)
    a2[a2 < 0.02] = 0
    a2 = np.clip(a2, 0, 1)
    # the object ends where alpha ends: no stray alpha farther than 3 px from the object
    a2[dist <= 0] = np.where(ndi.binary_dilation(solid, iterations=2)[dist <= 0], a2[dist <= 0], 0)
    return np.dstack([out, a2 * 255.0])


def upscale2(rgba):
    a = rgba[..., 3:4] / 255.0
    pm = Image.fromarray(np.clip(rgba[..., :3] * a, 0, 255).astype("uint8"))
    pm2 = np.array(pm.resize((pm.width * 2, pm.height * 2), Image.LANCZOS)).astype(float)
    a2 = np.array(Image.fromarray(np.clip(rgba[..., 3], 0, 255).astype("uint8")).resize((pm.width * 2, pm.height * 2), Image.LANCZOS)).astype(float) / 255.0
    rgb2 = np.where(a2[..., None] > 0.003, pm2 / np.maximum(a2[..., None], 1e-3), pm2)
    return np.dstack([np.clip(rgb2, 0, 255), a2 * 255.0])


def save_png(arr, path):
    Image.fromarray(np.clip(np.rint(arr), 0, 255).astype("uint8"), "RGBA").save(path, optimize=True)


def crops(dirpath, before):
    os.makedirs(dirpath, exist_ok=True)
    def over(img, bgc):
        a = np.array(img.convert("RGBA")).astype(float); al = a[..., 3:4] / 255
        return Image.fromarray(np.clip(a[..., :3] * al + np.array(bgc) * (1 - al), 0, 255).astype("uint8"))
    new = Image.open(os.path.join(ED, "deck-cutout.png"))
    regions = {"corner-tl": (24, 462, 104, 522), "corner-tr": (1396, 462, 1476, 522), "corner-bl": (24, 976, 104, 1036), "corner-br": (1396, 976, 1476, 1036),
               "edge-left": (24, 730, 84, 790), "edge-bottom": (700, 986, 780, 1036), "edge-top": (700, 462, 780, 512), "edge-right": (1416, 730, 1476, 790)}
    for name, b in regions.items():
        z = 4; w, h = (b[2] - b[0]) * z, (b[3] - b[1]) * z
        sheet = Image.new("RGB", (w * 3 + 8, h), (40, 40, 40))
        for k, (img, bgc) in enumerate([(before, (14, 14, 16)), (new, (14, 14, 16)), (new, (120, 120, 130))]):
            sheet.paste(over(img.crop(b), bgc).resize((w, h), Image.NEAREST), (k * (w + 4), 0))
        sheet.save(os.path.join(dirpath, f"cutout-{name}.png"))


def main():
    ap = argparse.ArgumentParser(); ap.add_argument("--crops"); args = ap.parse_args()
    before = Image.open(os.path.join(ED, "deck-cutout.png")).convert("RGBA").copy() if args.crops else None
    src = np.array(Image.open(SRC).convert("RGB")).astype(float)
    src = paint_out_led(src)
    Image.fromarray(src.astype("uint8")).save(os.path.join(ED, "deck.jpg"), quality=95)
    rgba = matte(src)
    save_png(rgba, os.path.join(ED, "deck-cutout.png"))
    rgba2 = upscale2(rgba)
    save_png(rgba2, os.path.join(ED, "deck-cutout-2x.png"))
    import shutil
    shutil.copyfile(os.path.join(ED, "deck-cutout.png"), os.path.join(AM, "deck-cutout.png"))
    shutil.copyfile(os.path.join(ED, "deck-cutout-2x.png"), os.path.join(AM, "deck-cutout@2x.png"))
    if args.crops: crops(args.crops, before)
    print("ok")


if __name__ == "__main__":
    sys.exit(main())
