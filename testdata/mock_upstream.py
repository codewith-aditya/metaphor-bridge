#!/usr/bin/env python3
"""Local stand-in for llms.withmetaphor.com used by the README demo recording.

Serves /v1/models and a streaming /v1/chat/completions so the bridge can be
demonstrated end-to-end without spending account quota. Never used in production.
"""
import json
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODELS = ["minimax-m3", "kimi-k3", "qwen3-coder-next", "deepseek-v4-1-flash",
          "glm-5.3", "kimi-k3-1-pro", "deepseek-v4-pro"]
ANSWER = ("A bridge exposes an upstream model API on a local address, so any OpenAI "
          "client can talk to it without changing code.")


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
                {"id": m, "object": "model", "owned_by": "metaphor"} for m in MODELS]})
            return
        self._json({"error": "not found"}, 404)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        try:
            req = json.loads(self.rfile.read(n) or b"{}")
        except Exception:
            req = {}
        if not self.path.startswith("/v1/chat/completions"):
            self._json({"error": "not found"}, 404)
            return
        model = req.get("model") or "minimax-m3"
        words = ANSWER.split(" ")
        if req.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()

            def send(obj):
                payload = ("data: " + json.dumps(obj) + "\n\n").encode()
                self.wfile.write(f"{len(payload):X}\r\n".encode() + payload + b"\r\n")
                self.wfile.flush()

            cid = "chatcmpl_mock"
            for i, w in enumerate(words):
                piece = w if i == len(words) - 1 else w + " "
                send({"id": cid, "object": "chat.completion.chunk", "created": int(time.time()),
                      "model": model, "choices": [{"index": 0, "finish_reason": None,
                                                   "delta": {"content": piece}}]})
                time.sleep(0.035)
            send({"id": cid, "object": "chat.completion.chunk", "created": int(time.time()),
                  "model": model, "choices": [{"index": 0, "finish_reason": "stop", "delta": {}}]})
            done = b"data: [DONE]\n\n"
            self.wfile.write(f"{len(done):X}\r\n".encode() + done + b"\r\n0\r\n\r\n")
            self.wfile.flush()
            return
        self._json({"id": "chatcmpl_mock", "object": "chat.completion", "model": model,
                    "choices": [{"index": 0, "finish_reason": "stop",
                                 "message": {"role": "assistant", "content": ANSWER}}],
                    "usage": {"prompt_tokens": 18, "completion_tokens": 30, "total_tokens": 48}})


if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", 8199), H).serve_forever()
