# AGENTS.md

Instructions for coding agents (Codex, Claude Code, OpenCode, Hermes) working in this
repo. Humans: [README.md](README.md) is the friendlier entry point.

## What this project is

A single Go binary that turns Metaphor's model gateway into a local
OpenAI-compatible API. The upstream (`https://llms.withmetaphor.com`) accepts a
WorkOS bearer token; this repo mints that token with the same OAuth flow Metaphor's
desktop app uses, keeps it fresh, and proxies `/v1/*` over HTTP/2 with a request
queue that respects the upstream's ~1 request concurrency limit.

```
client → :8190 (h2c or HTTP/1.1) → metaphor-bridge → HTTP/2 → llms.withmetaphor.com
                        ↑
                 tokens.json ← WorkOS PKCE (auth.go) ← refresh_token rotation
```

## File map

| File | Responsibility |
| --- | --- |
| `main.go` | HTTP server, `/v1/*` proxy, semaphore queue, 429 retry, SSE flush, `/healthz` |
| `creds.go` | token store (`tokens.json`), JWT expiry parsing, WorkOS refresh + PKCE helpers |
| `auth.go` | `auth start/finish/refresh/show/gh` subcommands, GitHub → WorkOS hop walking |
| `bench/bench.py` | throughput benchmark used for `BENCHMARK.md` |
| `systemd/` | example unit (edit paths before installing) |
| `BENCHMARK.md` | measured TPS/TTFT and rate-limit behaviour |
| `CONTRIBUTING.md` | scope, credential rules, PR checklist |

Runtime state lives **outside** the repo, in `METAPHOR_DIR` (default `/root/metaphor`):
`tokens.json` (0600), `pkce.json`, `login_url.txt`. Never copy those into the tree.

## Constants that matter

- WorkOS client id: `client_01KAHJW6K5B09TYCG4P597H63N` (public client, PKCE, no secret)
- Allowed redirect URIs: `metaphor://auth/callback` (whitelisted) — localhost is rejected
- Upstream: `https://llms.withmetaphor.com`, endpoints `/v1/{models,chat/completions,messages,responses}`
- Access token = WorkOS JWT, ~24 h; refresh via `grant_type=refresh_token`

## Commands

```bash
go build -o metaphor-go .            # always run before claiming a change works
gofmt -l . && go vet ./...           # must be clean
METAPHOR_PORT=8191 ./metaphor-go serve
./metaphor-go auth show              # token status
python3 bench/bench.py --runs 1      # smoke benchmark against a running bridge
```

Verification for any proxy change:

```bash
curl -sS http://127.0.0.1:8191/healthz
curl -sS --http2-prior-knowledge -o /dev/null -w '%{http_version}\n' http://127.0.0.1:8191/v1/models   # expect 2
curl -sS http://127.0.0.1:8191/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}],"max_tokens":16}'
curl -sS -N http://127.0.0.1:8191/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"qwen3-coder-next","messages":[{"role":"user","content":"count 1-5"}],"max_tokens":32,"stream":true}'
```

## Invariants — do not break these

1. **No secrets in git.** `tokens.json`, `pkce.json`, cookies, HAR files, logs stay out.
   `git diff --cached` before every push.
2. **One upstream, one account.** No pooling, no key rotation across users, no resale
   plumbing. Such PRs get closed.
3. **Queue before fan-out.** Upstream caps concurrency at ~1–2; keep the semaphore in
   `proxy()` and default `METAPHOR_CONCURRENCY=1`.
4. **Refresh by `exp`, not by wall clock.** `expires_in` is absent from the WorkOS
   response — `creds.go:jwtExp` is the source of truth. A refresh on every request
   rotates the refresh token needlessly.
5. **Streams are pass-through.** Flush per chunk, never buffer an SSE response, and
   never retry a stream after the first byte reached the client.
6. **`METAPHOR_DIR` stays outside the repo** and files are written 0600.

## Failure playbook

| Symptom | Cause | Fix |
| --- | --- | --- |
| `401 authentication_error` from upstream | expired/invalid bearer | `auth refresh`, else re-run the login flow |
| `403 email_verification_required` | WorkOS user never verified email | owner signs in once at `withmetaphor.com/signin`, completes the code, retry |
| `429 Too many concurrent Darwin requests` | upstream concurrency window | keep one in flight; the bridge already retries with `Retry-After` |
| GitHub `429` on `/login/oauth/authorize` | per-IP rate limit from repeated auth attempts | wait it out or use another egress for that hop |
| `invalid_grant` on `auth finish` | the code was already consumed (browser visited the URL first) | restart with `auth start` |
| Blank browser page after GitHub | Chrome refuses the `metaphor://` scheme silently | Firefox shows the URL; or DevTools → Network → the `metaphor://` request |

## Style

- Stdlib first. `golang.org/x/net` (h2c) is the only allowed dependency without a strong
  argument.
- Errors get context and the HTTP status; never swallow a non-2xx from upstream.
- Commits: `area: short imperative summary` (`auth: keep old refresh token when omitted`).
- Docs changes that alter claims (limits, TPS) must point at a re-runnable measurement.

## Provenance

The auth flow was reverse-engineered from Metaphor's desktop build (published npm
package `@withmetaphor/darwin` and the Tauri app) — the gateway URL, `METAPHOR_ACCESS_TOKEN`,
the WorkOS client id and the deep-link redirect all come from there. The Python
prototypes (`metaphor_auth.py`, `gh_oauth.py`, `gateway.py`) live outside the repo and are
kept only as a reference; the Go implementation is the supported one.
