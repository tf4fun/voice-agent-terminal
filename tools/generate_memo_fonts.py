#!/usr/bin/env python3
"""Reproduce memo font payloads from pinned, licensed Noto source fonts.

Default/--check writes only temporary output and requires the reproduced
payloads to match the manifest. --write regenerates the maintained C files from
the current character inventories and records their new hashes, code point
counts and line heights in the manifest; --fetch downloads missing source files
into the ignored cache.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / "firmware/assets/fonts"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def payload(data):
    # lv_font_conv embeds cache/output paths in its leading CLI comment. Those
    # paths have no effect on glyph bitmap, metrics, kerning or exported names.
    return data.split(b"*/", 1)[1]


def portable(data, out, name, source_dir):
    # Keep machine-local paths out of the committed header comment.
    header, rest = data.split(b"*/", 1)
    header = header.replace(str(out).encode(), name.encode())
    header = header.replace(str(source_dir).encode() + b"/", b"build/font-sources/")
    return header + b"*/" + rest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true")
    mode.add_argument("--write", action="store_true")
    parser.add_argument("--fetch", action="store_true")
    parser.add_argument("--source-dir", type=Path, default=ROOT / "build/font-sources")
    parser.add_argument("--converter", type=Path, default=Path("lv_font_conv"))
    args = parser.parse_args()
    manifest = json.loads((ASSETS / "manifest.json").read_text())
    inventory = (ASSETS / "charset.txt").read_bytes()
    if args.write:
        manifest["charset_sha256"] = digest(inventory)
    elif digest(inventory) != manifest["charset_sha256"]:
        raise SystemExit("Character inventory differs from manifest")
    version = subprocess.check_output([str(args.converter), "--version"], text=True).strip()
    if version != manifest["converter_version"]:
        raise SystemExit("Expected lv_font_conv " + manifest["converter_version"] + ", got " + version)
    for name, source in manifest["sources"].items():
        path = args.source_dir / name
        if not path.exists():
            if not args.fetch:
                raise SystemExit(str(path) + " missing; use --fetch or --source-dir")
            with urllib.request.urlopen(source["url"], timeout=60) as response:
                data = response.read(20 * 1024 * 1024 + 1)
            if digest(data) != source["sha256"]:
                raise SystemExit("Downloaded source hash mismatch: " + name)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        if digest(path.read_bytes()) != source["sha256"]:
            raise SystemExit("Source hash mismatch; existing file preserved: " + str(path))
    with tempfile.TemporaryDirectory(prefix="memo-font-check-") as directory:
        for name, target in manifest["outputs"].items():
            out = Path(directory) / name
            inventory = (ASSETS / target["charset"]).read_bytes()
            if args.write:
                target["charset_sha256"] = digest(inventory)
            elif digest(inventory) != target["charset_sha256"]:
                raise SystemExit("Character inventory differs from manifest: " + name)
            symbols = inventory.decode("utf-8").rstrip("\n")
            subprocess.run([
                str(args.converter), "--font", str(args.source_dir / target["source"]),
                "--symbols", symbols, "--size", str(target["size"]), "--bpp", "4",
                "--format", "lvgl", "--no-compress", "--lv-include", "lvgl.h", "-o", str(out),
            ], cwd=ROOT, check=True)
            data = portable(out.read_bytes(), out, name, args.source_dir)
            if args.write:
                requested = set(symbols)
                emitted = {chr(int(code, 16)) for code in re.findall(rb"/\* U\+([0-9A-F]+) ", data)}
                target["payload_sha256"] = digest(payload(data))
                target["emitted_codepoints"] = len(emitted)
                target["omitted_codepoints"] = sorted("U+%04X" % ord(c) for c in requested - emitted)
                target["line_height"] = int(re.search(rb"\.line_height = (\d+)", data).group(1))
                (ASSETS / name).write_bytes(data)
                print(name + ": regenerated")
                continue
            if digest(payload(data)) != target["payload_sha256"]:
                raise SystemExit("Reproduced payload differs: " + name)
            if payload((ASSETS / name).read_bytes()) != payload(data):
                raise SystemExit("Maintained C payload differs: " + name)
            print(name + ": reproduced bitmap/metrics/cmap payload PASS")
    if args.write:
        chat = manifest["outputs"]["memo_font_16.c"]
        manifest["emitted_codepoints"] = chat["emitted_codepoints"]
        manifest["omitted_unassigned_codepoints"] = chat["omitted_codepoints"]
        manifest["requested_codepoints"] = len(set((ASSETS / "charset.txt").read_text().rstrip("\n")))
        (ASSETS / "manifest.json").write_text(json.dumps(manifest, indent=2, ensure_ascii=False) + "\n")
        print("manifest.json: updated; run tools/check-firmware.py")


if __name__ == "__main__":
    main()
