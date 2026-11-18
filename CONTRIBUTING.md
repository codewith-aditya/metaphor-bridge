# Contributing

Thanks for taking a look. This repo is a small Go bridge plus the auth plumbing
needed to talk to one upstream, so the bar for changes is low but the ground rules
are narrow.

## Ground rules

- **Never commit credentials.** `tokens.json`, `pkce.json`, `login_url.txt`, GitHub
  cookies, HAR files, logs and `.env` files stay out of git. `.gitignore` covers the
  common ones; check `git diff --cached` before you push.
- **One upstream, no key sharing.** The bridge authenticates *your own* account.
  PRs that add pooled accounts, key rotation across users, or anything meant to
  resell free-tier access are out of scope and will be closed.
- **Keep the dependency surface tiny.** `golang.org/x/net` for h2c is the only
  non-stdlib dependency. Adding a router, logging framework or ORM needs a good
  reason.
- Be decent in issues and reviews. Reproduce before you speculate.

## Development setup

```bash
git clone https://github.com/codewith-aditya/metaphor-bridge
cd metaphor-bridge
go build -o metaphor-go .        # Go 1.22+
./metaphor-go auth show          # needs a token first, see README
METAPHOR_PORT=8191 ./metaphor-go serve
```

Sanity checks before opening a PR:

```bash
gofmt -l .        # must print nothing
go vet ./...
go build ./...
```

Smoke test against a running bridge (uses your own token):

```bash
curl -sS http://127.0.0.1:8191/healthz
curl -sS http://127.0.0.1:8191/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"minimax-m3","messages":[{"role":"user","content":"hi"}],"max_tokens":16}'
```

## What is worth a PR

| Area | Examples |
| --- | --- |
| Auth | better error messages for `email_verification_required`, handling a rotated refresh token, cleanup of stale `pkce.json` |
| Bridge | backpressure, per-request timeouts, graceful shutdown, `Retry-After` edge cases |
| Compatibility | Anthropic `/v1/messages` payload quirks, `stream_options.include_usage`, tool-call deltas |
| Docs | corrections to `README.md` / `BENCHMARK.md`, extra troubleshooting entries |
| Bench | new models, longer runs, a second workload (long-context, tool calls) |

## Pull requests

1. Fork, branch off `master`, keep the diff focused on one thing.
2. Match the commit style: `area: short imperative summary`
   (`auth: keep old refresh token when WorkOS omits it`, `bridge: flush on every SSE chunk`).
3. Describe **how you verified it** — a curl transcript, a log line, a benchmark run.
4. If you touch the request path, say what happens with `METAPHOR_CONCURRENCY=1`
   and with `2`, because the upstream limiter is the usual failure mode.
5. If you change measured behaviour (`BENCHMARK.md` numbers, limits), include the
   new run: `python3 bench/bench.py --runs 2`.

## Reporting bugs

Include all of:

- bridge version / commit (`git rev-parse --short HEAD`)
- `curl http://127.0.0.1:8190/healthz`
- the exact request body (redact `messages` if private) and the response, including
  status code and any `Retry-After`
- relevant lines from the bridge log (`journalctl -u metaphor-bridge-go` or the log
  file), with tokens and cookies stripped

Auth failures need one extra detail: which command was used (`auth start/finish`,
`auth gh`) and the WorkOS error code (`email_verification_required`,
`invalid_grant`, …). Never paste a full `metaphor://…?code=` URL or a cookie value
into an issue — codes are single-use but cookies are not.

## Security issues

Do not open a public issue for anything that leaks tokens, cookies or session state.
Email the maintainer (see the GitHub profile) or use a private security advisory on
this repo, and give a couple of days for a reply.

## License

By contributing you agree your work is licensed under the MIT license in
[LICENSE](LICENSE).
