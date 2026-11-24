#!/usr/bin/env python3
"""Demo shim: exposes Metaphor model ids, forwards to a private OpenAI-compatible backend.

Set SHIM_BACKEND / SHIM_BACKEND_MODEL in the environment; nothing is hard-coded here.

Only used to record the README clip. The public model catalogue stays Metaphor's;
the backend host and its model name never appear in the demo.
"""
import json
import os
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

BACKEND = os.environ.get("SHIM_BACKEND", "http://127.0.0.1:8000")
BACKEND_MODEL = os.environ.get("SHIM_BACKEND_MODEL", "local-model")
PUBLIC_MODELS = ["minimax-m3", "kimi-k3", "qwen3-coder-next", "deepseek-v4-1-flash",
                 "glm-5.3", "kimi-k3-1-pro", "deepseek-v4-pro"]
DEFAULT_MODEL = "minimax-m3"


class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def _json(self, obj, code=200):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        if self.path.startswith("/v1/models"):
            self._json({"object": "list", "data": [
                {"id": m, "object": "model", "owned_by": "metaphor"} for m in PUBLIC_MODELS]})
            return
        self._json({"error": "not found"}, 404)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n) if n else b"{}"
        try:
            req = json.loads(raw)
        except Exception:
            req = {}
        if not self.path.startswith("/v1/chat/completions"):
            self._json({"error": "not found"}, 404)
            return

        public = req.get("model") or DEFAULT_MODEL
        if public not in PUBLIC_MODELS:
            public = DEFAULT_MODEL
        req["model"] = BACKEND_MODEL
        body = json.dumps(req).encode()
        up = urllib.request.Request(BACKEND + "/v1/chat/completions", data=body,
                                    headers={"Content-Type": "application/json"})
        try:
            resp = urllib.request.urlopen(up, timeout=180)
        except urllib.error.HTTPError as e:
            data = e.read()
            self.send_response(e.code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return

        ctype = resp.headers.get("Content-Type", "application/json")
        if "text/event-stream" in ctype:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()
            for line in resp:
                payload = line.replace(BACKEND_MODEL.encode(), public.encode())
                if not payload.strip():
                    continue
                self.wfile.write(f"{len(payload):X}\r\n".encode() + payload + b"\r\n")
                self.wfile.flush()
            self.wfile.write(b"0\r\n\r\n")
            self.wfile.flush()
            return

        data = resp.read().replace(BACKEND_MODEL.encode(), public.encode())
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", 8199), H).serve_forever()
