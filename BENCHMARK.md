# Benchmark — Metaphor gateway through metaphor-bridge

Numbers below are measured, not vendor claims. Re-run them yourself with
[`bench/bench.py`](bench/bench.py).

## Test setup

| | |
| --- | --- |
| Date | 2026-11-18 07:45 UTC |
| Bridge | `metaphor-bridge-go` (this repo), `METAPHOR_CONCURRENCY=1` |
| Upstream hop | HTTP/2 to `llms.withmetaphor.com` (Vercel edge, `iad1`) |
| Client | Python 3 `urllib`, non-streaming, one request at a time |
| Host | 4 vCPU AMD EPYC 7443P, 7 GB RAM, egress in Secaucus, US |
| Prompt | "Write a self-contained Go function that merges two sorted integer slices …" (identical for every run) |
| Generation | `max_tokens=512`, `temperature=0` |
| Runs | 2 per model, median reported; 1 streaming run per model for TTFT |

TPS = `usage.completion_tokens / wall_clock_seconds` (whole request, not just decode).
TTFT = time to first SSE chunk.

## Results

| Model | TPS (median) | Output tokens | Wall (median) | TTFT (stream) | Cost / request* |
| --- | ---: | ---: | ---: | ---: | ---: |
| `glm-5.3` | **135.6** | 512 (all reasoning) | 3.88 s | 1.28 s | 0.001499809 |
| `kimi-k3` | **99.2** | 512 | 5.65 s | 0.76 s | 0.007392 |
| `qwen3-coder-next` | **88.6** | 374 | 5.25 s | 0.79 s | 0.0004723 |
| `deepseek-v4-1-flash` | **87.2** | 512 (all reasoning) | 8.14 s | 0.28 s | 0.0006318 |
| `kimi-k3-1-pro` | **80.0** | 512 | 6.40 s | 0.81 s | 0.005826559 |
| `deepseek-v4-pro` | **67.1** | 242 | 4.47 s | 1.13 s | 0.00049 |
| `minimax-m3` | **32.7** | 210 | 6.99 s | 1.05 s | 0.000271079 |
| `kimi-k3-1-pro-highspeed` | n/a | — | — | — | upstream returned `upstream_rate_limit_exceeded` during the run |

\* the `cost.total` field the gateway returns per response (USD, as billed to the
account's plan). The Hobby plan includes open-model usage, so these are the
gateway's accounting numbers, not your invoice.

Single-run spread matters: the same `qwen3-coder-next` prompt measured **112.7 tok/s**
in an earlier run and **88.6 tok/s** median here — treat anything within ~25 % as noise.

### Behaviour worth knowing

- **Reasoning models eat the budget.** `glm-5.3` and `deepseek-v4-1-flash` spent all
  512 tokens on reasoning (`completion_tokens_details.reasoning_tokens == 512`,
  `finish_reason: "length"`, empty `content`). Give them ≥ 1–2 k `max_tokens` if you
  want visible output.
- **Streaming usage is not reported.** SSE chunks carry no `usage` object, so a
  stream-side TPS number can only be estimated from chunk counts (the script reports
  it as a proxy, see `stream_tps_proxy` in the JSON).
- **Concurrency is the real wall.** The gateway answers a second in-flight request with
  `429 {"type":"rate_limit_error","message":"Too many concurrent Darwin requests."}`
  and `retry-after: 2`. The bridge serialises requests (default
  `METAPHOR_CONCURRENCY=1`) and retries, which is why the table above is single-stream.

## Rate limit probe

Sequential tiny requests (`max_tokens=4`, 200 ms apart, one at a time) through the
bridge:

```
ok 27  fail 3  elapsed 29.3s   →  27 requests in 28.7 s before the first 429
first 429 at request 28: Too many concurrent Darwin requests (retry-after: 2)
```

So a burst of roughly **25–30 requests completes, then the limiter trips** and needs
tens of seconds to drain — consistent with a small concurrency window rather than a
strict per-minute quota. Keep one request in flight and pace bursts if you care about
latency.

## Reproducing

```bash
# bridge running with a valid token
python3 bench/bench.py --base-url http://127.0.0.1:8190/v1 --runs 2
```

The script prints a table and writes `bench-results.json` next to your CWD.
Model availability depends on the account's plan; only open models are free.
