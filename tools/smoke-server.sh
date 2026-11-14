#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
task_dir="$(mktemp -d)"
trap 'rm -rf -- "$task_dir"' EXIT
cd "${repo_root}/server"
go build -mod=readonly -o "${task_dir}/voice-agent-server" ./cmd/voice-agent-server
python3 - "${task_dir}/voice-agent-server" "$task_dir" <<'PY'
import http.server
import json
import os
import pathlib
import socket
import subprocess
import sys
import threading
import time
import urllib.request
import uuid

binary, directory = sys.argv[1:]
calls = []

class Hermes(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        assert self.headers.get("Authorization") == "Bearer smoke-hermes"
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.path == "/api/sessions":
            self.send_response(201)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"session": {"id": body["id"], "source": "api_server"}}).encode())
            return
        assert self.path == "/v1/chat/completions"
        calls.append((body, self.headers.get("Idempotency-Key")))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for chunk in [
            {"choices": [{"delta": {"content": "你好，测试回复"}}]},
            {"choices": [{"delta": {}, "finish_reason": "stop"}]},
        ]:
            self.wfile.write(b"data: " + json.dumps(chunk, ensure_ascii=False).encode() + b"\n\n")
        self.wfile.write(b"data: [DONE]\n\n")

upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Hermes)
thread = threading.Thread(target=upstream.serve_forever, daemon=True)
thread.start()
with socket.socket() as reservation:
    reservation.bind(("127.0.0.1", 0))
    port = reservation.getsockname()[1]
base = f"http://127.0.0.1:{port}"
environment = os.environ.copy()
environment.update({
    "VOICE_ADDR": f"127.0.0.1:{port}",
    "VOICE_DATA_DIR": str(pathlib.Path(directory) / "data"),
    "VOICE_MDNS": "false",
    "VOICE_API_KEY": "smoke-device",
    "VOICE_ASR_PROVIDER": "fake",
    "OPENAI_API_KEY": "",
    "HERMES_API_KEY": "smoke-hermes",
    "HERMES_BASE_URL": f"http://127.0.0.1:{upstream.server_port}",
})
process = None

def request(method, path, body=None, content_type="application/json"):
    data = body if isinstance(body, bytes) else None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method, headers={
        "Authorization": "Bearer smoke-device", "Content-Type": content_type,
    })
    with urllib.request.urlopen(req, timeout=5) as response:
        return json.load(response)

def start():
    child = subprocess.Popen([binary, "-env", str(pathlib.Path(directory) / "missing.env")],
                             env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    for _ in range(100):
        if child.poll() is not None:
            raise AssertionError(child.stderr.read().decode())
        try:
            request("GET", "/api/v1/health")
            return child
        except OSError:
            time.sleep(0.05)
    child.terminate()
    child.wait(timeout=10)
    raise AssertionError("server did not become ready")

def stop(child):
    child.terminate()
    try:
        assert child.wait(timeout=10) == 0
    finally:
        if child.poll() is None:
            child.kill()
            child.wait(timeout=5)
        child.stderr.close()

try:
    assert subprocess.check_output([binary, "version"], text=True).strip() == "0.2.0"
    process = start()
    session, first, second, message = [str(uuid.uuid4()) for _ in range(4)]
    path = f"/api/v1/sessions/{session}"
    request("POST", "/api/v1/sessions", {"id": session, "title": "烟雾测试"})
    request("POST", path + "/segments", {"id": first, "revision": 1, "text": "第一段"})
    request("POST", path + "/segments", {"id": second, "revision": 2})
    request("POST", path + f"/segments/{second}/audio?sample_rate=16000&revision=3", bytes(6400), "audio/pcm")
    draft = request("GET", path + "/draft")
    assert len(draft["items"]) == 2 and calls == []
    revised = request("PATCH", path + f"/segments/{first}", {"revision": draft["revision"], "text": "修改第一段"})
    send = {"id": message, "revision": revised["revision"]}
    request("POST", path + "/messages", send)
    request("POST", path + "/messages", send)
    for _ in range(100):
        result = request("GET", path + f"/messages/{message}")
        if result["state"] == "completed":
            break
        time.sleep(0.05)
    assert result["state"] == "completed", result
    assert len(calls) == 1 and calls[0][0]["messages"][0]["content"] == "修改第一段\n\n测试转写"
    assert request("GET", path + f"/messages/{message}/text")["text"] == "你好，测试回复"
    stop(process)
    process = None
    process = start()
    assert request("GET", path + f"/messages/{message}")["state"] == "completed"
    assert len(calls) == 1
    print("PASS: real binary lifecycle, draft editing, fake audio, explicit send, retry identity and restart persistence")
finally:
    if process is not None:
        stop(process)
    upstream.shutdown()
    upstream.server_close()
    thread.join(timeout=5)
PY
