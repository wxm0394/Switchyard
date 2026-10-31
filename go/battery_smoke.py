#!/usr/bin/env python3
"""Switchyard Phase 6 binary smoke battery (LOCAL ONLY).

Starts the built switchyard binary against a mock upstream and a
throwaway SQLite file, seeds routes/keys/token/aliases through the
admin API, and replays Cline-shaped requests: plain chat, tool-rescue,
tool-args repair, large-history (70k+ token shape), streaming
pass-through, privacy off/low, /v1/models, and direct-pin.

NEVER point this at the Ohio gateway, a production DB, or a real
provider. Every key here is fake, every upstream answer is canned,
every token count is mock data, and the DB file is deleted on exit.
Run:  python3 go/battery_smoke.py  (from the repo root, after go build)
"""
import json
import os
import signal
import subprocess
import sys
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

WORK = os.path.dirname(os.path.abspath(__file__))  # go/
ROOT = os.path.dirname(WORK)
BIN = os.path.join(WORK, "switchyard-battery")
DB = os.path.join(WORK, "battery.db")
GW_PORT = 18080
MOCK_PORT = 18081
GW = f"http://127.0.0.1:{GW_PORT}"
MOCK = f"http://127.0.0.1:{MOCK_PORT}"
FAKE_MASTER_KEY = "0123456789abcdef" * 4  # 64 hex chars, test-only, not real

LAST_LEN = {"n": 0}
LOCK = threading.Lock()


def plain(content, tokens=50):
    return {"id": "chatcmpl-mock", "choices": [{"message": {"role": "assistant", "content": content}, "finish_reason": "stop"}], "usage": {"total_tokens": tokens}}


def toolcalls(name, args):
    return {"id": "chatcmpl-mock", "choices": [{"message": {"role": "assistant", "content": "", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": name, "arguments": args}}]}, "finish_reason": "tool_calls"}], "usage": {"total_tokens": 50}}


class Mock(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(n) if n else b""
        with LOCK:
            LAST_LEN["n"] = len(raw)
        try:
            req = json.loads(raw.decode() or "{}")
        except Exception:
            req = {}
        case = req.get("battery_case", "plain")
        if case == "rescue":
            body = plain('<function=get_weather>{"city": "Tokyo"}</function>')
        elif case == "args":
            body = toolcalls("search", '{"tags": "[\\"a\\", \\"b\\"]"}')
        elif case == "stream":
            body = toolcalls("search", '{"tags": "[\\"a\\"]"}')
            body["choices"][0]["message"]["content"] = '<function=search>{"tags": ["a"]}</function>'
        else:
            body = plain("4")
        data = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def call(method, path, body=None, headers=None, base=GW):
    req = urllib.request.Request(base + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:
            return e.code, {}


def chat(body, token, extra=None):
    return call("POST", "/v1/chat/completions", body, {"Authorization": "Bearer " + token, **(extra or {})})


def main():
    results = []

    def check(name, cond, detail=""):
        results.append((name, bool(cond), detail))
        print(("PASS " if cond else "FAIL ") + name + ((" | " + str(detail)) if detail and not cond else ""))

    # Build.
    env = dict(os.environ, PATH=os.path.expanduser("~/workspace/tools/go/bin") + ":" + os.environ["PATH"])
    r = subprocess.run(["go", "build", "-o", BIN, "./cmd/switchyard"], cwd=WORK, env=env, capture_output=True, text=True)
    if r.returncode != 0:
        print("BUILD FAILED", r.stdout, r.stderr)
        return 1
    mock = ThreadingHTTPServer(("127.0.0.1", MOCK_PORT), Mock)
    threading.Thread(target=mock.serve_forever, daemon=True).start()
    for f in (DB, DB + "-wal", DB + "-shm"):
        if os.path.exists(f):
            os.remove(f)
    gw_env = dict(os.environ, SWITCHYARD_PORT=str(GW_PORT), SWITCHYARD_HOST="127.0.0.1",
                  SWITCHYARD_DB_PATH=DB, GATEWAY_MASTER_KEY=FAKE_MASTER_KEY)
    gw_env.pop("KAGGLE_REFRESH_ENABLED", None)
    proc = subprocess.Popen([BIN], cwd=WORK, env=gw_env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    try:
        # Wait for readiness.
        for _ in range(50):
            try:
                s, _ = call("GET", "/v1/models")
                if s == 200:
                    break
            except Exception:
                pass
            time.sleep(0.2)
        # Seed via admin API (no password set, non-production -> allowed).
        s, key = call("POST", "/api/gateway/keys", {"name": "Battery", "rawApiKey": "battery-smoke-key-0001", "baseUrl": MOCK + "/v1"})
        assert s == 201, (s, key)
        kid = key["id"]
        s, route = call("POST", "/api/gateway/routes", {"modelId": "battery-model", "upstreamModel": "battery-model", "providerKeyId": kid, "baseUrl": MOCK + "/v1", "taskRole": "auto", "supportsVision": True, "supportsTools": True, "maxContext": 128000})
        assert s == 201, (s, route)
        s, route2 = call("POST", "/api/gateway/routes", {"modelId": "pinned-model", "upstreamModel": "pinned-model", "providerKeyId": kid, "baseUrl": MOCK + "/v1", "taskRole": "auto", "supportsVision": True, "supportsTools": True, "maxContext": 128000})
        assert s == 201, (s, route2)
        s, tok = call("POST", "/api/gateway/tokens", {"name": "battery"})
        assert s == 201, (s, tok)
        token = tok["token"]
        s, _ = call("POST", "/api/gateway/aliases", {"alias": "coder", "targetModels": ["battery-model"]})
        assert s == 200, s
        s, _ = call("PATCH", "/api/gateway/config", {"privacyMode": "off"})
        assert s == 200, s

        # 1. Plain small chat.
        s, b = chat({"model": "coder", "messages": [{"role": "user", "content": "2+2?"}]}, token)
        check("plain-chat-200", s == 200 and b["choices"][0]["message"]["content"] == "4", (s, b))
        # 2. Tool-rescue (function tag -> tool_calls, content kept).
        s, b = chat({"model": "coder", "messages": [{"role": "user", "content": "weather?"}],
                     "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}}],
                     "battery_case": "rescue"}, token)
        msg = b["choices"][0]["message"]
        check("rescue", s == 200 and b["choices"][0]["finish_reason"] == "tool_calls" and msg["tool_calls"][0]["function"]["name"] == "get_weather" and msg["content"].startswith("<function="), (s, b))
        # 3. Tool-args repair.
        s, b = chat({"model": "coder", "messages": [{"role": "user", "content": "search"}],
                     "tools": [{"type": "function", "function": {"name": "search", "parameters": {"type": "object", "properties": {"tags": {"type": "array", "items": {"type": "string"}}}}}}],
                     "battery_case": "args"}, token)
        args = b["choices"][0]["message"]["tool_calls"][0]["function"]["arguments"]
        check("tool-args", s == 200 and json.loads(args) == {"tags": ["a", "b"]}, (s, args))
        # 4. Large history: ~162k ASCII chars -> >70k estimated tokens.
        big = "The quick brown fox jumps over the lazy dog. " * 3600
        s, b = chat({"model": "coder", "messages": [{"role": "user", "content": big}]}, token)
        check("large-history", s == 200 and LAST_LEN["n"] > 150000, (s, LAST_LEN["n"]))
        # 5. Streaming passes through untouched.
        s, b = chat({"model": "coder", "stream": True, "messages": [{"role": "user", "content": "search"}],
                     "tools": [{"type": "function", "function": {"name": "search", "parameters": {"type": "object", "properties": {"tags": {"type": "array", "items": {"type": "string"}}}}}}],
                     "battery_case": "stream"}, token)
        sm = b["choices"][0]["message"]
        check("streaming-untouched", s == 200 and sm["tool_calls"][0]["function"]["arguments"] == '{"tags": "[\\"a\\"]"}' and sm["content"].startswith("<function="), (s, b))
        # 6. Privacy: low + secret words -> 503; back to off -> 200.
        s, _ = call("PATCH", "/api/gateway/config", {"privacyMode": "low"})
        s, b = chat({"model": "coder", "messages": [{"role": "user", "content": "my api_secret is xyz"}]}, token)
        check("privacy-low-blocks", s == 503 and "no_healthy_route" in json.dumps(b), (s, b))
        s, _ = call("PATCH", "/api/gateway/config", {"privacyMode": "off"})
        s, b = chat({"model": "coder", "messages": [{"role": "user", "content": "my api_secret is xyz"}]}, token)
        check("privacy-off-passes", s == 200, (s, b))
        # 7. /v1/models lists aliases only.
        s, b = call("GET", "/v1/models")
        check("models-alias-only", s == 200 and [d["id"] for d in b["data"]] == ["coder"], (s, b))
        # 8. Direct-pin bypasses the alias.
        s, b = chat({"model": "coder", "messages": [{"role": "user", "content": "hi"}]}, token, {"X-Portal-Target-Model": "pinned-model"})
        s2, logs = call("GET", "/api/gateway/logs?limit=1")
        # AuditRow has no JSON tags, so Go emits field names as-is.
        sel = logs["logs"][0]["SelectedModelID"] if s2 == 200 and logs.get("logs") else None
        check("direct-pin", s == 200 and sel == "pinned-model", (s, sel))
        # 9. No token -> 401.
        s, b = chat({"model": "coder", "messages": [{"role": "user", "content": "hi"}]}, "sk-portal-wrong")
        check("auth-401", s == 401, (s, b))
    finally:
        proc.send_signal(signal.SIGTERM)
        try:
            proc.wait(timeout=10)
        except Exception:
            proc.kill()
        mock.shutdown()
        for f in (DB, DB + "-wal", DB + "-shm", BIN):
            if os.path.exists(f):
                os.remove(f)
    failed = [n for n, ok, _ in results if not ok]
    print(f"\n{len(results) - len(failed)}/{len(results)} binary battery checks passed; real token burn: 0 (mock upstream only)")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
