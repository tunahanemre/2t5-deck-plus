#!/usr/bin/env python3
"""Re-runnable: derive the in-app brand mark files from the company logo source.

  make_brand_assets.py <2t5-white-transparent.png> [outdir]

Source (read-only, 02-BRANDINGs/00-LOGOS/00-2T5-LOGOS/):
  mark      2t5-white-transparent.png   472x388, white on transparent
Outputs (default outdir app/frontend/public/editor/assets/brand/):
  2t5-mark-white.png   the mark as is (dark surfaces, the app icon)
  2t5-mark-ink.png     the mark recoloured #0a0908, alpha kept (light surfaces)
"FILM" and "2t5.co" under the mark are live text in the editor, not images.
Only the alpha channel of the source carries shape; colour is set explicitly.
"""
import hashlib
import pathlib
import sys

from PIL import Image

INK = (10, 9, 8)
WHITE = (255, 255, 255)


def recolour(img, rgb):
    a = img.convert("RGBA").getchannel("A")
    out = Image.new("RGBA", img.size, rgb + (0,))
    out.putalpha(a)
    return out


def main(argv):
    if len(argv) < 2:
        print(__doc__)
        return 2
    mark_src = pathlib.Path(argv[1])
    here = pathlib.Path(__file__).resolve().parent.parent
    out = pathlib.Path(argv[2]) if len(argv) > 2 else here / "app/frontend/public/editor/assets/brand"
    out.mkdir(parents=True, exist_ok=True)
    mark = Image.open(mark_src).convert("RGBA")
    files = {"2t5-mark-white.png": recolour(mark, WHITE), "2t5-mark-ink.png": recolour(mark, INK)}
    for name, im in files.items():
        p = out / name
        im.save(p, optimize=True)
        print(f"{name}  {im.size[0]}x{im.size[1]}  sha256 {hashlib.sha256(p.read_bytes()).hexdigest()}")
    print(f"source {mark_src.name}  sha256 {hashlib.sha256(mark_src.read_bytes()).hexdigest()}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
