#!/usr/bin/env python3
"""Regenerate build/windows/icon.ico from build/appicon.png.

    python3 build/make-icons.py

Run this whenever appicon.png changes.

Wails generates a platform icon only when it is *missing*. build/windows/icon.ico
is committed, so it is never refreshed: replace appicon.png and macOS picks up the
new mark on the next build while every Windows build keeps the old one. Both
halves stay internally consistent and the product is wrong — the failure this
project keeps running into. Hence a tool rather than a note in a README.

macOS needs nothing here. Wails builds the .icns from appicon.png on every build.

No image library is available on the build machine, so this reads and writes PNG
by hand. It accepts what a web icon generator actually emits: 8-bit greyscale,
palette, RGB or RGBA, with or without an alpha channel.
"""

import os
import struct
import sys
import zlib

# What Windows asks for: 16 in the title bar and tray, 32 in the taskbar, 48 in
# Explorer's medium view, 256 for the large views.
ICO_SIZES = (16, 24, 32, 48, 64, 128, 256)


def read_png(path):
    """Decode a PNG to (size, RGBA bytes). Square images only."""
    data = open(path, "rb").read()
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        raise SystemExit(f"{path}: not a PNG")

    idat = b""
    plte = None
    trns = None
    width = height = depth = ctype = interlace = 0

    i = 8
    while i < len(data):
        (length,) = struct.unpack(">I", data[i : i + 4])
        tag = data[i + 4 : i + 8]
        chunk = data[i + 8 : i + 8 + length]
        if tag == b"IHDR":
            width, height, depth, ctype, _, _, interlace = struct.unpack(">IIBBBBB", chunk)
        elif tag == b"PLTE":
            plte = chunk
        elif tag == b"tRNS":
            trns = chunk
        elif tag == b"IDAT":
            idat += chunk
        elif tag == b"IEND":
            break
        i += 12 + length

    if width != height:
        raise SystemExit(f"{path}: {width}x{height} — the icon must be square")
    if depth != 8:
        raise SystemExit(
            f"{path}: {depth}-bit — re-export as 8 bits per channel"
        )
    if interlace:
        raise SystemExit(f"{path}: interlaced — re-export without interlacing")

    channels = {0: 1, 2: 3, 3: 1, 4: 2, 6: 4}.get(ctype)
    if channels is None:
        raise SystemExit(f"{path}: unsupported colour type {ctype}")
    if ctype == 3 and plte is None:
        raise SystemExit(f"{path}: palette image with no PLTE chunk")

    raw = zlib.decompress(idat)
    stride = width * channels
    rows = []
    prev = bytearray(stride)
    pos = 0
    for _ in range(height):
        filt = raw[pos]
        pos += 1
        line = bytearray(raw[pos : pos + stride])
        pos += stride
        # Undo the per-scanline filter. bpp is the byte offset to the pixel to
        # the left, which for 8-bit data is just the channel count.
        bpp = channels
        for x in range(stride):
            a = line[x - bpp] if x >= bpp else 0
            b = prev[x]
            c = prev[x - bpp] if x >= bpp else 0
            if filt == 1:
                line[x] = (line[x] + a) & 0xFF
            elif filt == 2:
                line[x] = (line[x] + b) & 0xFF
            elif filt == 3:
                line[x] = (line[x] + (a + b) // 2) & 0xFF
            elif filt == 4:
                p = a + b - c
                pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                pred = a if (pa <= pb and pa <= pc) else (b if pb <= pc else c)
                line[x] = (line[x] + pred) & 0xFF
        rows.append(bytes(line))
        prev = line

    rgba = bytearray(width * width * 4)
    for y, line in enumerate(rows):
        for x in range(width):
            o = (y * width + x) * 4
            if ctype == 0:  # greyscale
                v = line[x]
                rgba[o] = rgba[o + 1] = rgba[o + 2] = v
                rgba[o + 3] = 255
            elif ctype == 4:  # greyscale + alpha
                v = line[x * 2]
                rgba[o] = rgba[o + 1] = rgba[o + 2] = v
                rgba[o + 3] = line[x * 2 + 1]
            elif ctype == 2:  # RGB
                i3 = x * 3
                rgba[o : o + 3] = line[i3 : i3 + 3]
                rgba[o + 3] = 255
            elif ctype == 3:  # palette
                idx = line[x]
                rgba[o : o + 3] = plte[idx * 3 : idx * 3 + 3]
                rgba[o + 3] = trns[idx] if trns and idx < len(trns) else 255
            else:  # RGBA
                i4 = x * 4
                rgba[o : o + 4] = line[i4 : i4 + 4]

    return width, rgba


def resize(rgba, src, dst):
    """Area-average resample, alpha included. Handles non-integer ratios."""
    if src == dst:
        return rgba
    out = bytearray(dst * dst * 4)
    scale = src / dst
    for y in range(dst):
        y0 = int(y * scale)
        y1 = max(y0 + 1, int((y + 1) * scale))
        for x in range(dst):
            x0 = int(x * scale)
            x1 = max(x0 + 1, int((x + 1) * scale))
            r = g = b = a = n = 0
            for sy in range(y0, min(y1, src)):
                base = (sy * src + x0) * 4
                for sx in range(min(x1, src) - x0):
                    i = base + sx * 4
                    r += rgba[i]
                    g += rgba[i + 1]
                    b += rgba[i + 2]
                    a += rgba[i + 3]
                    n += 1
            o = (y * dst + x) * 4
            out[o] = r // n
            out[o + 1] = g // n
            out[o + 2] = b // n
            out[o + 3] = a // n
    return out


def png_bytes(rgba, size):
    """A complete PNG file: signature, IHDR, IDAT, IEND."""

    def chunk(tag, data):
        return (
            struct.pack(">I", len(data))
            + tag
            + data
            + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)
        )

    raw = bytearray()
    stride = size * 4
    for y in range(size):
        raw.append(0)  # filter type 0 (None)
        raw += rgba[y * stride : (y + 1) * stride]

    return (
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0))
        + chunk(b"IDAT", zlib.compress(bytes(raw), 9))
        + chunk(b"IEND", b"")
    )


def write_ico(path, master, src_size):
    """Write a PNG-compressed .ico.

    Vista and later accept PNG data directly inside an ICO, which is why this can
    reuse the PNG writer rather than emit BMP with its bottom-up rows and separate
    AND mask.
    """
    images = [(n, png_bytes(resize(master, src_size, n), n)) for n in ICO_SIZES]

    header = struct.pack("<HHH", 0, 1, len(images))  # reserved, type=icon, count
    offset = len(header) + 16 * len(images)

    entries = b""
    for n, data in images:
        # A directory entry stores the dimension in one byte, so 256 is written
        # as 0.
        entries += struct.pack(
            "<BBBBHHII", n & 0xFF, n & 0xFF, 0, 0, 1, 32, len(data), offset
        )
        offset += len(data)

    with open(path, "wb") as f:
        f.write(header + entries + b"".join(d for _, d in images))


def round_corners(rgba, size, radius_pct):
    """Cut the canvas down to a rounded square, making everything outside it
    transparent.

    Icon generators — the AI ones especially — hand back an opaque square with the
    rounded shape *painted on* in white. macOS does not mask an app icon for you:
    it draws exactly the bitmap it is given, so a painted corner ships as a white
    square sitting among the system's rounded ones.

    The edge is antialiased from a signed distance rather than a hard test, or the
    curve comes out as visible steps at 32px and below.
    """
    r = size * radius_pct / 100.0
    half = size / 2.0
    inner = half - r
    out = bytearray(rgba)

    for y in range(size):
        dy = abs(y + 0.5 - half) - inner
        for x in range(size):
            dx = abs(x + 0.5 - half) - inner
            if dx <= 0 and dy <= 0:
                continue  # well inside; the common case, and the cheap one
            qx, qy = max(dx, 0.0), max(dy, 0.0)
            dist = (qx * qx + qy * qy) ** 0.5 - r
            if dist <= -0.5:
                continue
            coverage = 0.0 if dist >= 0.5 else 0.5 - dist
            i = (y * size + x) * 4 + 3
            out[i] = int(out[i] * coverage)
    return out


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    appicon = os.path.join(here, "appicon.png")

    args = [a for a in sys.argv[1:]]
    radius_pct = None
    if "--round" in args:
        i = args.index("--round")
        args.pop(i)
        radius_pct = float(args.pop(i)) if i < len(args) else 24.0

    source = args[0] if args else appicon
    if not os.path.exists(source):
        raise SystemExit(f"{source} not found")

    size, rgba = read_png(source)
    if size < 256:
        print(
            f"warning: {source} is {size}x{size}; 1024x1024 is what Wails expects",
            file=sys.stderr,
        )
    print(f"source: {source} ({size}x{size})")

    if radius_pct is not None:
        rgba = round_corners(rgba, size, radius_pct)
        print(f"        corners cut to a {radius_pct:g}% rounded square")

    # Writing appicon.png is skipped when it *is* the source and nothing changed,
    # so the plain form of this command stays a read-only conversion.
    if source != appicon or radius_pct is not None:
        with open(appicon, "wb") as f:
            f.write(png_bytes(rgba, size))
        print(f"wrote {appicon} ({os.path.getsize(appicon)} bytes)")

    out = os.path.join(here, "windows", "icon.ico")
    write_ico(out, rgba, size)
    print(f"wrote {out} ({os.path.getsize(out)} bytes, sizes {', '.join(map(str, ICO_SIZES))})")


if __name__ == "__main__":
    main()
