#!/usr/bin/env bash
# Render firmware screens on the host with the locked LVGL version.
# Output: build/ui-preview/frames/*.png plus sheet-dark.png / sheet-light.png.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
lvgl_version="$(awk '/lvgl\/lvgl:/{f=1} f&&/^    version:/{print $2; exit}' "$repo_root/firmware/dependencies.lock")"
lvgl_version="${lvgl_version%%~*}"
work="$repo_root/build/ui-preview"
lvgl="$work/lvgl-$lvgl_version"
preview="$repo_root/firmware/tests/ui_preview"
mkdir -p "$work/obj-$lvgl_version"
if [[ ! -f "$lvgl/lvgl.h" ]]; then
    # Unpack beside the cache and move it in only once complete, so an
    # interrupted download is fetched again instead of reused half-written.
    unpack="$(mktemp -d "$work/lvgl-download.XXXXXX")"
    trap 'rm -rf -- "$unpack"' EXIT
    curl -fsSL "https://github.com/lvgl/lvgl/archive/refs/tags/v$lvgl_version.tar.gz" | tar xz -C "$unpack"
    rm -rf -- "$lvgl"
    mv "$unpack/lvgl-$lvgl_version" "$lvgl"
fi
cflags=(-std=gnu11 -O1 -fno-pie -DLV_CONF_INCLUDE_SIMPLE -DLV_LVGL_H_INCLUDE_SIMPLE -I"$preview" -I"$lvgl")
library="$work/liblvgl-$lvgl_version.a"
if [[ ! -f "$library" ]]; then
    while IFS= read -r source; do
        object="$work/obj-$lvgl_version/$(echo "${source#"$lvgl/"}" | tr / _).o"
        echo "${CC:-cc} ${cflags[*]} -w -c $source -o $object"
    done < <(find "$lvgl/src" -name '*.c') | xargs -P "$(nproc)" -I{} sh -c '{}'
    ar rcs "$library" "$work"/obj-"$lvgl_version"/*.o
fi
main="$repo_root/firmware/main"
"${CC:-cc}" "${cflags[@]}" -Wall -Wno-unused-function -Wno-unused-variable \
    -I"$preview/stubs" -I"$repo_root/firmware/tests/client_stubs" -I"$main" \
    -I"$repo_root/firmware/components/bsp/include" -I"$repo_root/firmware/components/bsp/src" \
    -I"$repo_root/firmware/tests/vendor/cjson" \
    "$preview/preview.c" "$main/memo_ui_common.c" "$main/memo_ui_settings.c" "$main/memo_ui_provision.c" \
    "$main/agent_ui_status.c" "$main/memo_font.c" "$main/memo_text.c" "$main/agent_protocol.c" \
    "$repo_root/firmware/tests/vendor/cjson/cJSON.c" \
    "$repo_root/firmware/components/bsp/src/bsp_display_rounding.c" \
    "$repo_root/firmware/assets/fonts/memo_font_16.c" "$repo_root/firmware/assets/fonts/memo_font_20.c" \
    "$repo_root/firmware/assets/fonts/memo_font_meta_16.c" "$repo_root/firmware/assets/fonts/memo_font_emoji_16.c" \
    "$library" -lm -no-pie -Wl,--unresolved-symbols=ignore-all -o "$work/preview"
rm -rf "$work/frames" && mkdir -p "$work/frames"
"$work/preview" "$work/frames" >/dev/null
python3 "$preview/png.py" "$work/frames"
