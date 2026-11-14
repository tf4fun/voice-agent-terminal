#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

static_checks() {
    "$repo_root/tools/test-firmware-host.sh"
    (
        cd "$repo_root/server"
        test -z "$(gofmt -l cmd internal)"
        go vet ./...
        go test -race -count=1 ./...
    )
    "$repo_root/tools/smoke-server.sh"
}

firmware_checks() (
    command -v idf.py >/dev/null || { echo "Activate ESP-IDF 5.5.3 first." >&2; exit 1; }
    test "$(idf.py --version)" = "ESP-IDF v5.5.3" || { echo "ESP-IDF 5.5.3 is required." >&2; exit 1; }
    validation_dir="$(mktemp -d /tmp/voice-agent-firmware.XXXXXX)"
    trap 'rm -rf -- "$validation_dir"' EXIT
    cd "$repo_root/firmware"
    SDKCONFIG_DEFAULTS="$repo_root/firmware/sdkconfig.defaults" \
        idf.py -B "$validation_dir" -D "SDKCONFIG=$validation_dir/sdkconfig" build
    idf.py -B "$validation_dir" merge-bin -o "$validation_dir/Voice-Agent-Terminal-full.bin"
    python3 "$repo_root/tools/verify_firmware.py" "$validation_dir"
    python3 "$repo_root/tools/archive_firmware.py" create "$validation_dir" --archive-root "$repo_root/build/firmware"
    mkdir -p "$repo_root/build"
    install -m 0644 "$validation_dir/Voice-Agent-Terminal-full.bin" "$repo_root/build/Voice-Agent-Terminal-full.bin"
    python3 - "$repo_root/build/Voice-Agent-Terminal-full.bin" <<'PY'
import hashlib
from pathlib import Path
import sys
image = Path(sys.argv[1])
digest = hashlib.sha256(image.read_bytes()).hexdigest()
image.with_name(image.name + ".sha256").write_text(f"{digest}  {image.name}\n")
PY
    echo "Build: PASS"
)

case "${1:---all}" in
    --static) static_checks ;;
    --firmware) firmware_checks ;;
    --all) static_checks; firmware_checks ;;
    *) echo "Usage: $0 [--all|--static|--firmware]" >&2; exit 2 ;;
esac

