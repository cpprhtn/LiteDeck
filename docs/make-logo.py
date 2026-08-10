#!/usr/bin/env python3
"""Turn a logo export into the two README images.

    python3 docs/make-logo.py logo.png

Writes docs/logo.png and docs/logo-dark.png.

Three things have to happen to a logo before a README can use it, and an icon
generator does none of them:

1. **Crop.** Exports arrive on a square canvas with the artwork floating in the
   middle. Left alone, the README reserves a tall block of empty space and the
   logo looks small inside it.

2. **Transparency.** The background comes back as opaque white. On GitHub's dark
   theme that renders as a white slab around the mark.

3. **A dark-theme variant.** Dark artwork on a transparent background disappears
   against a dark page. GitHub picks between two files with

       <picture>
         <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.png">
         <img src="docs/logo.png" alt="LiteDeck">
       </picture>

The background is removed by flooding inwards from the edges rather than by
testing every light pixel, because the mark encloses white areas of its own — the
window inside the key head — and a plain threshold would punch a hole through
them.
"""

import collections
import importlib.util
import os
import sys

# The PNG codec lives with the icon tool; there is no reason to carry a second
# copy of it here.
_spec = importlib.util.spec_from_file_location(
    "mkicons",
    os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "build", "make-icons.py"),
)
_mk = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_mk)

# The ink colour the artwork is drawn in. Edge pixels are recoloured to this and
# given partial alpha, so the antialiasing survives the background removal instead
# of leaving a pale halo.
INK = (0x16, 0x1A, 0x20)


def content_bounds(rgba, w, h, threshold=235):
    """The box that actually contains artwork."""
    xs, ys = [], []
    for y in range(h):
        row = y * w
        for x in range(w):
            o = (row + x) * 4
            if rgba[o + 3] > 16 and (
                rgba[o] < threshold or rgba[o + 1] < threshold or rgba[o + 2] < threshold
            ):
                xs.append(x)
                ys.append(y)
    if not xs:
        raise SystemExit("no artwork found — the image looks blank")
    return min(xs), min(ys), max(xs), max(ys)


def crop(rgba, w, h, x0, y0, x1, y1):
    cw, ch = x1 - x0 + 1, y1 - y0 + 1
    out = bytearray(cw * ch * 4)
    for y in range(ch):
        src = ((y0 + y) * w + x0) * 4
        dst = y * cw * 4
        out[dst : dst + cw * 4] = rgba[src : src + cw * 4]
    return out, cw, ch


def drop_background(rgba, w, h):
    """Flood from the border and clear whatever light region connects to it."""
    seen = bytearray(w * h)
    queue = collections.deque()

    def light(k):
        o = k * 4
        return rgba[o] > 200 and rgba[o + 1] > 200 and rgba[o + 2] > 200

    for x in range(w):
        for y in (0, h - 1):
            k = y * w + x
            if not seen[k] and light(k):
                seen[k] = 1
                queue.append((x, y))
    for y in range(h):
        for x in (0, w - 1):
            k = y * w + x
            if not seen[k] and light(k):
                seen[k] = 1
                queue.append((x, y))

    while queue:
        x, y = queue.popleft()
        for dx, dy in ((1, 0), (-1, 0), (0, 1), (0, -1)):
            nx, ny = x + dx, y + dy
            if 0 <= nx < w and 0 <= ny < h:
                k = ny * w + nx
                if not seen[k] and light(k):
                    seen[k] = 1
                    queue.append((nx, ny))

    out = bytearray(rgba)
    for k in range(w * h):
        if not seen[k]:
            continue
        o = k * 4
        # Alpha from luminance, so a half-covered edge pixel stays half-covered.
        lum = (out[o] * 299 + out[o + 1] * 587 + out[o + 2] * 114) // 1000
        out[o], out[o + 1], out[o + 2] = INK
        out[o + 3] = max(0, 255 - lum)
    return out


def invert(rgba, w, h):
    """Flip the ink for the dark theme: dark strokes go light, the window goes dark."""
    out = bytearray(rgba)
    for k in range(w * h):
        o = k * 4
        if out[o + 3] == 0:
            continue
        out[o] = 255 - out[o]
        out[o + 1] = 255 - out[o + 1]
        out[o + 2] = 255 - out[o + 2]
    return out


def main():
    args = sys.argv[1:]
    margin_pct = 3.0
    if "--margin" in args:
        i = args.index("--margin")
        args.pop(i)
        margin_pct = float(args.pop(i))
    if not args:
        raise SystemExit("usage: make-logo.py <source.png> [--margin PCT]")

    src = args[0]
    here = os.path.dirname(os.path.abspath(__file__))

    w, h, rgba = _mk.read_png(src)
    x0, y0, x1, y1 = content_bounds(rgba, w, h)
    print(f"source: {src} ({w}x{h})")
    print(f"        artwork at x {x0}..{x1}, y {y0}..{y1}")

    m = int(max(x1 - x0, y1 - y0) * margin_pct / 100)
    x0, y0 = max(0, x0 - m), max(0, y0 - m)
    x1, y1 = min(w - 1, x1 + m), min(h - 1, y1 + m)

    rgba, cw, ch = crop(rgba, w, h, x0, y0, x1, y1)
    print(f"        cropped to {cw}x{ch} ({cw / ch:.2f}:1) with a {margin_pct:g}% margin")

    rgba = drop_background(rgba, cw, ch)

    light_path = os.path.join(here, "logo.png")
    dark_path = os.path.join(here, "logo-dark.png")
    with open(light_path, "wb") as f:
        f.write(_mk.png_bytes(rgba, cw, ch))
    with open(dark_path, "wb") as f:
        f.write(_mk.png_bytes(invert(rgba, cw, ch), cw, ch))

    print(f"wrote {light_path} ({os.path.getsize(light_path)} bytes)")
    print(f"wrote {dark_path} ({os.path.getsize(dark_path)} bytes)")


if __name__ == "__main__":
    main()
