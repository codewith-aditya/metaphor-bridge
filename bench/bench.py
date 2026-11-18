#!/usr/bin/env python3
"""Benchmark an OpenAI-compatible endpoint (default: a running metaphor-bridge).

Measures tokens/sec, wall time and stream TTFT per model, one request at a time so
the upstream concurrency limiter is not what you end up measuring.

    python3 bench/bench.py --base-url http://127.0.0.1:8190/v1 --runs 2
"""
import argparse
import json
import statistics
import sys
import time
import urllib.error
import urllib.request

DEFAULT_MODELS = [
    "qwen3-coder-next",
    "minimax-m3",
    "kimi-k3",
    "kimi-k3-1-pro",
    "deepseek-v4-1-flash",
    "deepseek-v4-pro",
    "glm-5.3",
]
PROMPT = (
    "Write a self-contained Go function that merges two sorted integer slices "
    "without allocating a new backing array, then explain its complexity in two sentences."
)


def request(base, model, max_tokens, stream=False, timeout=300):
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": PROMPT}],
        "max_tokens": max_tokens,
        "temperature": 0,
    }
    if stream:
        payload["stream"] = True
    req = urllib.request.Request(
        base.rstrip("/") + "/chat/completions",
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json"},
    )
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            if stream:
                ttft, chunks, usage = None, 0, None
                for raw in r:
                    line = raw.decode(errors="replace").strip()
                    if not line.startswith("data:"):
                        continue
                    data = line[5:].strip()
                    if data == "[DONE]":
                        break
                    try:
                        obj = json.loads(data)
                    except Exception:
                        continue
                    if ttft is None:
                        ttft = time.time() - t0
                    choices = obj.get("choices") or []
                    if choices and (choices[0].get("delta") or {}).get("content"):
                        chunks += 1
                    if obj.get("usage"):
                        usage = obj["usage"]
                wall = time.time() - t0
                tokens = (usage or {}).get("completion_tokens") or chunks
                return {"ok": True, "wall": wall, "ttft": ttft, "tokens": tokens,
                        "usage_available": bool(usage)}
            body = json.loads(r.read().decode())
            wall = time.time() - t0
            usage = body.get("usage") or {}
            return {
                "ok": True,
                "wall": wall,
                "tokens": usage.get("completion_tokens", 0),
                "reasoning": (usage.get("completion_tokens_details") or {}).get("reasoning_tokens", 0),
                "finish": body["choices"][0].get("finish_reason"),
                "cost": (body.get("cost") or {}).get("total"),
            }
    except urllib.error.HTTPError as e:
        return {"ok": False, "status": e.code, "body": e.read().decode(errors="replace")[:160],
                "wall": time.time() - t0}
    except Exception as e:  # noqa: BLE001 - benchmark should keep going
        return {"ok": False, "error": str(e), "wall": time.time() - t0}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base-url", default="http://127.0.0.1:8190/v1")
    ap.add_argument("--models", default=",".join(DEFAULT_MODELS))
    ap.add_argument("--runs", type=int, default=2)
    ap.add_argument("--max-tokens", type=int, default=512)
    ap.add_argument("--gap", type=float, default=4.0, help="seconds between requests")
    ap.add_argument("--out", default="bench-results.json")
    args = ap.parse_args()

    models = [m.strip() for m in args.models.split(",") if m.strip()]
    results = {}
    print(f"{'model':30s} {'TPS':>7s} {'tokens':>7s} {'wall':>7s} {'TTFT':>7s} {'cost':>12s}")
    for model in models:
        runs = []
        for _ in range(args.runs):
            r = request(args.base_url, model, args.max_tokens)
            if not r["ok"] and r.get("status") == 429:
                time.sleep(20)
                r = request(args.base_url, model, args.max_tokens)
            if not r["ok"]:
                print(f"{model:30s} FAILED {r.get('status') or r.get('error')}")
                break
            runs.append(r)
            time.sleep(args.gap)
        if not runs:
            continue
        tps = [r["tokens"] / r["wall"] for r in runs if r["wall"] > 0]
        s = request(args.base_url, model, args.max_tokens, stream=True)
        results[model] = {
            "tps_median": round(statistics.median(tps), 1) if tps else None,
            "tokens_median": int(statistics.median([r["tokens"] for r in runs])),
            "wall_median": round(statistics.median([r["wall"] for r in runs]), 2),
            "ttft": round(s["ttft"], 2) if s.get("ok") and s.get("ttft") else None,
            "cost": runs[0].get("cost"),
            "reasoning_tokens": runs[0].get("reasoning"),
            "runs": [{k: v for k, v in r.items() if k != "ok"} for r in runs],
        }
        out = results[model]
        print(f"{model:30s} {out['tps_median'] or 0:7.1f} {out['tokens_median']:7d} "
              f"{out['wall_median']:7.2f} {(out['ttft'] or 0):7.2f} {out['cost'] if out['cost'] is not None else '-':>12}")
        time.sleep(args.gap)

    json.dump(results, open(args.out, "w"), indent=2)
    print(f"\nsaved {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
