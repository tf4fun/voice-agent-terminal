#!/usr/bin/env python3
"""Convert preview .rgb frames to PNG and build a labelled contact sheet."""
import struct, sys, zlib
from pathlib import Path

W, H = 240, 320

def write_png(path, width, height, rgb):
    raw = b"".join(b"\0" + rgb[y * width * 3:(y + 1) * width * 3] for y in range(height))
    chunk = lambda tag, data: struct.pack(">I", len(data)) + tag + data + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)
    png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(raw, 9)) + chunk(b"IEND", b"")
    Path(path).write_bytes(png)

def sheet(frames, path, columns, gap=16, background=(40, 40, 44)):
    rows = (len(frames) + columns - 1) // columns
    sw, sh = columns * W + (columns + 1) * gap, rows * H + (rows + 1) * gap
    canvas = bytearray(bytes(background) * (sw * sh))
    for index, frame in enumerate(frames):
        ox = gap + (index % columns) * (W + gap)
        oy = gap + (index // columns) * (H + gap)
        for y in range(H):
            start = ((oy + y) * sw + ox) * 3
            canvas[start:start + W * 3] = frame[y * W * 3:(y + 1) * W * 3]
    write_png(path, sw, sh, bytes(canvas))

def main():
    directory = Path(sys.argv[1])
    frames = {}
    for raw in sorted(directory.glob("*.rgb")):
        data = raw.read_bytes()
        write_png(raw.with_suffix(".png"), W, H, data)
        frames[raw.stem] = data
        raw.unlink()
    for theme in ("dark", "light"):
        selected = [frames[name] for name in sorted(frames) if name.endswith("-" + theme)]
        if selected:
            sheet(selected, directory / f"sheet-{theme}.png", 6)
    print(f"{len(frames)} frames -> {directory}")

main()
