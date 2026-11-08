#!/usr/bin/env python3
"""Re-runnable: build the Dock / app icon from the 2T5 mark.

  make_appicon.py [mark.png] [out.png]

Defaults: app/frontend/public/editor/assets/brand/2t5-mark-white.png -> app/build/appicon.png.
1024x1024 RGBA: the white mark centred on an ink #0a0908 rounded-square tile (macOS icon shape:
824 px tile, 100 px margin = ~10 %, corner radius 22.4 % of the tile, drawn 4x and scaled down
for a clean edge). Wails turns appicon.png into the .icns at build time.
"""
import pathlib
import sys

from PIL import Image, ImageDraw

SIZE, TILE, SS = 1024, 824, 4
RADIUS = round(TILE * 0.224)
INK = (10, 9, 8, 255)
MARK_W = 500  # mark width inside the tile (~61 %)


def build(mark_path, out_path):
    mark = Image.open(mark_path).convert("RGBA")
    big = Image.new("L", (SIZE * SS, SIZE * SS), 0)
    m = (SIZE - TILE) // 2 * SS
    ImageDraw.Draw(big).rounded_rectangle([m, m, m + TILE * SS - 1, m + TILE * SS - 1], radius=RADIUS * SS, fill=255)
    mask = big.resize((SIZE, SIZE), Image.LANCZOS)
    icon = Image.new("RGBA", (SIZE, SIZE), (0, 0, 0, 0))
    icon.paste(Image.new("RGBA", (SIZE, SIZE), INK), (0, 0), mask)
    h = round(mark.height * MARK_W / mark.width)
    mk = mark.resize((MARK_W, h), Image.LANCZOS)
    icon.alpha_composite(mk, ((SIZE - MARK_W) // 2, (SIZE - h) // 2))
    out_path.parent.mkdir(parents=True, exist_ok=True)
    icon.save(out_path, optimize=True)
    return icon


def main(argv):
    root = pathlib.Path(__file__).resolve().parent.parent
    mark = pathlib.Path(argv[1]) if len(argv) > 1 else root / "app/frontend/public/editor/assets/brand/2t5-mark-white.png"
    out = pathlib.Path(argv[2]) if len(argv) > 2 else root / "app/build/appicon.png"
    icon = build(mark, out)
    print(f"wrote {out} {icon.size}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
