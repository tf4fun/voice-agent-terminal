#!/usr/bin/env bash
set -euo pipefail

arch="${1:-}"
if [[ $# -ne 1 || ( "$arch" != amd64 && "$arch" != arm64 ) ]]; then
    echo "Usage: $0 <amd64|arm64>" >&2
    exit 2
fi

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
output_dir="${repo_root}/build/server/linux-${arch}"
binary="${output_dir}/voice-agent-server-linux-${arch}"
mkdir -p "$output_dir"

cd "${repo_root}/server"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
    -mod=readonly -trimpath -ldflags="-s -w" -o "$binary" ./cmd/voice-agent-server

python3 - "$binary" <<'PY'
import hashlib
import pathlib
import sys

binary = pathlib.Path(sys.argv[1])
digest = hashlib.sha256()
with binary.open("rb") as stream:
    for chunk in iter(lambda: stream.read(1024 * 1024), b""):
        digest.update(chunk)
checksum = f"{digest.hexdigest()}  {binary.name}\n"
(binary.parent / "SHA256SUMS").write_text(checksum, encoding="ascii")
print(checksum, end="")
PY
