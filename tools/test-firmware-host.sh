#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf -- "$test_dir"' EXIT
cd "$repo_root"
python3 tools/check-firmware.py
"${CC:-cc}" -std=c11 -Wall -Wextra -Werror -Ifirmware/main -Ifirmware/tests/vendor/cjson \
    firmware/tests/test_agent_protocol.c firmware/main/agent_protocol.c \
    firmware/main/memo_text.c firmware/tests/vendor/cjson/cJSON.c -lm -o "$test_dir/protocol"
"$test_dir/protocol"
for test in test_memo_key test_key_guard test_memo_power; do
    "${CC:-cc}" -std=c11 -Wall -Wextra -Werror -Ifirmware/main \
        "firmware/tests/$test.c" -o "$test_dir/$test"
    "$test_dir/$test"
done
"${CC:-cc}" -std=c11 -Wall -Wextra -Werror -Ifirmware/main -Ifirmware/tests/settings_stubs \
    firmware/tests/test_memo_settings.c -o "$test_dir/settings"
"$test_dir/settings"

"${CC:-cc}" -std=c11 -Wall -Wextra -Werror -Ifirmware/main -Ifirmware/tests/client_stubs \
    -Ifirmware/tests/vendor/cjson firmware/tests/test_agent_client.c \
    firmware/main/agent_protocol.c firmware/main/memo_text.c \
    firmware/tests/vendor/cjson/cJSON.c -lm -o "$test_dir/client"
"$test_dir/client"
"${CC:-cc}" -std=c11 -Wall -Wextra -Werror -Ifirmware/main -Ifirmware/tests/discovery_stubs \
    firmware/tests/test_memo_discovery.c firmware/main/memo_discovery.c -o "$test_dir/discovery"
"$test_dir/discovery"
