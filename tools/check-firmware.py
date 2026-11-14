#!/usr/bin/env python3
"""Check fixed Chinese UI glyphs and the device's bounded transport contract."""

import ast
import hashlib
import json
from pathlib import Path
import re

root = Path(__file__).resolve().parents[1]
firmware = root / "firmware"
charset = set((firmware / "assets/fonts/charset.txt").read_text().rstrip("\n"))
assets = firmware / "assets/fonts"
common = (firmware / "main/memo_ui_common.c").read_text()
manifest = json.loads((assets / "manifest.json").read_text())
coverage = {}
for name, target in manifest["outputs"].items():
    data = (assets / name).read_bytes()
    assert hashlib.sha256(data.split(b"*/", 1)[1]).hexdigest() == target["payload_sha256"], name
    inventory = (assets / target["charset"]).read_bytes()
    assert hashlib.sha256(inventory).hexdigest() == target["charset_sha256"], name
    requested = set(inventory.decode().rstrip("\n"))
    emitted = {chr(int(code, 16)) for code in re.findall(rb"/\* U\+([0-9A-F]+) ", data)}
    omitted = {chr(int(code[2:], 16)) for code in target["omitted_codepoints"]}
    assert emitted == requested - omitted, name
    assert len(emitted) == target["emitted_codepoints"], name
    line_height = int(re.search(rb"\.line_height = (\d+)", data).group(1))
    assert line_height == target["line_height"], name
    coverage[name] = emitted
assert coverage["memo_font_16.c"] == charset - {"\uff00"}
# Cover both GB2312 Chinese levels when regenerating the chat font.
for high in range(0xB0, 0xF8):
    for low in range(0xA1, 0xFF):
        try:
            character = bytes((high, low)).decode("gb2312")
        except UnicodeDecodeError:
            continue
        assert character in coverage["memo_font_16.c"], character
assert "渥" in coverage["memo_font_16.c"]
# Chat content and UI labels retain Chinese quotation marks and common punctuation.
common_punctuation = set("‘’“”‚„–—‐‑‒―•‧‥…‰′″‵※")
assert common_punctuation <= coverage["memo_font_16.c"]
assert common_punctuation <= coverage["memo_font_20.c"]
assert set("0123456789%:- ") <= coverage["memo_font_meta_16.c"]
assert manifest["outputs"]["memo_font_meta_16.c"]["line_height"] <= 23
# ASCII uses contiguous glyph IDs. Check proportional 100% fits its header box.
metadata = (assets / "memo_font_meta_16.c").read_text()
records = metadata[metadata.index("glyph_dsc[]"):metadata.index("cmaps[]")]
advances = [int(value) for value in re.findall(r"\.adv_w = (\d+)", records)]
battery_width = int(re.search(r"lv_obj_set_size\(label, (\d+), memo_font_meta\(\)->line_height\)", common).group(1))
assert sum(advances[ord(character) - 32 + 1] for character in "100%") <= battery_width * 16


def decode_utf8_escape(match):
    data = bytes(int(code, 16) for code in re.findall(r"\\x([0-9a-fA-F]{2})", match.group(0)))
    try:
        return json.dumps(data.decode("utf-8"), ensure_ascii=False)[1:-1]
    except UnicodeDecodeError:
        return match.group(0)


tokens = re.compile(r'("(?:[^"\\]|\\.)*")|//[^\n]*|/\*.*?\*/', re.S)
missing = {}
for path in (firmware / "main").glob("*.c"):
    for match in tokens.finditer(path.read_text()):
        if match.group(1) is None:
            continue
        try:
            literal = re.sub(r"(?:\\x[0-9a-fA-F]{2})+", decode_utf8_escape, match.group(1))
            text = ast.literal_eval(literal)
        except (SyntaxError, ValueError):
            continue
        for character in text:
            if ord(character) >= 0x80 and character not in coverage["memo_font_16.c"]:
                missing.setdefault(character, set()).add(path.name)
if missing:
    raise SystemExit("Missing UI glyphs: " + repr({ch: sorted(paths) for ch, paths in missing.items()}))

settings = (firmware / "main/memo_ui_settings.c").read_text()
assert "ROW_AUTOPLAY" not in settings and "ROW_AUTOSAVE" not in settings
assert '"_voiceagent"' in (firmware / "main/memo_discovery.h").read_text()
assert "#define AGENT_PAGE_SIZE 4" in (firmware / "main/agent_protocol.h").read_text()
assert "#define AGENT_TEXT_BYTES 512" in (firmware / "main/agent_protocol.h").read_text()
assert '"memo_recorder.c"' not in (firmware / "main/CMakeLists.txt").read_text()
assert '"memo_player.c"' not in (firmware / "main/CMakeLists.txt").read_text()
assert "esp_audio_codec" not in (firmware / "main/idf_component.yml").read_text()
app = (firmware / "main/agent_app.c").read_text()
assert "memo_font_meta()" in app and "memo_font_meta()" in common
assert "memo_font_mono" not in app + common
navigation = app[app.index("static void scroll_chat("):app.index("static void confirm_chat(")]
assert "lv_obj_scroll" in navigation and "LV_ANIM_OFF" in navigation
assert "s_chat_cards[4]" not in app
assert "lv_obj_set_style_text_line_space(s_chat_body, AGENT_CHAT_LINE_SPACE, 0)" in app
print("PASS: font payloads, both GB2312 Chinese levels (including 渥), metadata font and bounded device contract")

