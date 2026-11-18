package main

// usage prints the CLI help, including the direct and proxied invocation examples.

import (
	"flag"
	"fmt"
	"os"
)

func usage() {
	out := os.Stderr
	fmt.Fprintf(out, `metaphor-go %s — local OpenAI-compatible bridge for llms.withmetaphor.com

USAGE
  metaphor-go [flags] [serve]                 run the bridge (default)
  metaphor-go [flags] auth start              print the WorkOS login URL
  metaphor-go [flags] auth finish <url|code>  exchange the code for tokens
  metaphor-go [flags] auth gh "<cookies>"     finish the flow with GitHub cookies
  metaphor-go [flags] auth refresh            force a refresh-token exchange
  metaphor-go [flags] auth show               show token status

FLAGS
`, version)
	flag.PrintDefaults()
	fmt.Fprint(out, `
RUNNING WITHOUT A PROXY (direct egress)
  metaphor-go serve
  metaphor-go -port 8190 -concurrency 1 serve
  METAPHOR_PORT=8191 METAPHOR_CONCURRENCY=2 metaphor-go serve

RUNNING WITH A PROXY (applies to the gateway, token refresh and auth hops)
  # HTTP CONNECT proxy
  metaphor-go -proxy http://user:pass@proxy.local:8080 serve
  METAPHOR_PROXY=http://proxy.local:8080 metaphor-go serve

  # HTTPS proxy with a self-signed certificate (proxies usually are)
  metaphor-go -proxy https://user:pass@gw.example.com:443 -proxy-insecure serve
  METAPHOR_PROXY='https://user:pass@gw.example.com:443' METAPHOR_PROXY_INSECURE=1 metaphor-go serve

  # SOCKS5
  metaphor-go -proxy socks5://127.0.0.1:1080 serve
  METAPHOR_PROXY=socks5://127.0.0.1:1080 metaphor-go serve

  # auth through the proxy as well (useful when GitHub rate-limits your IP)
  METAPHOR_PROXY=https://user:pass@gw.example.com:443 METAPHOR_PROXY_INSECURE=1 \
    metaphor-go auth gh "user_session=...; _gh_sess=..."

VERIFY
  curl -sS localhost:8190/healthz     # token expiry, concurrency, active proxy
  curl -sS localhost:8190/debug/ip    # which egress IP the bridge is using

DOCS
  README.md      setup, endpoints, troubleshooting
  BENCHMARK.md   measured TPS/TTFT and rate limits
  AGENTS.md      architecture and invariants for coding agents
`)
}
