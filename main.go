package main

// Metaphor gateway bridge: local OpenAI-compatible endpoint backed by
// https://llms.withmetaphor.com with WorkOS token auto-refresh.
//
//	go run . serve            # listen on 127.0.0.1:8190
//	go run . auth start       # print login URL
//	go run . auth finish URL  # exchange code
//	go run . auth refresh     # force refresh
//	go run . auth show        # token status
//	go run . auth gh COOKIE   # finish OAuth with GitHub cookies

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

var upstreamBase = func() string {
	if v := os.Getenv("METAPHOR_UPSTREAM"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://llms.withmetaphor.com"
}()

const version = "1.1.1"

var (
	httpClient = newHTTPClient()
	logger     = log.New(os.Stdout, "", log.LstdFlags)
	proxyCfg   *proxyConfig
)

func logf(format string, args ...any) { logger.Printf(format, args...) }

func newHTTPClient() *http.Client {
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // we handle redirects manually
		},
	}
}

// clientThroughProxy builds a client whose dialer goes through pc (nil = direct).
func clientThroughProxy(pc *proxyConfig) *http.Client {
	c := newHTTPClient()
	if pc == nil {
		return c
	}
	tr := c.Transport.(*http.Transport)
	tr.Proxy = nil                  // the dialer already does the tunnelling
	tr.DialContext = pc.dialContext // CONNECT / SOCKS5
	tr.ForceAttemptHTTP2 = true
	return c
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// ---------- server ----------

type bridge struct {
	store *tokenStore
	sem   chan struct{}
}

func (b *bridge) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		tok, err := b.store.AccessToken()
		exp := int64(0)
		if err == nil {
			exp = jwtExp(tok)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"status":      "ok",
			"version":     version,
			"token_ok":    err == nil,
			"token_exp":   time.Unix(exp, 0).Format(time.RFC3339),
			"upstream":    upstreamBase,
			"concurrency": cap(b.sem),
			"proxy":       proxyCfg.String(),
		})
	})
	mux.HandleFunc("/debug/ip", func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), "GET", "https://api.ipify.org", nil)
		resp, err := httpClient.Do(req)
		if err != nil {
			writeJSONError(w, 502, "proxy_check_failed", err.Error())
			return
		}
		defer resp.Body.Close()
		ip, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		json.NewEncoder(w).Encode(map[string]any{
			"egress_ip": string(ip),
			"proxy":     proxyCfg.String(),
			"proto":     resp.Proto,
		})
	})
	mux.HandleFunc("/v1/", b.proxy)
	return mux
}

func (b *bridge) proxy(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), 400)
		return
	}

	// queue: the upstream allows ~1-2 in-flight requests
	select {
	case b.sem <- struct{}{}:
		defer func() { <-b.sem }()
	case <-r.Context().Done():
		return
	}

	token, err := b.store.AccessToken()
	if err != nil {
		writeJSONError(w, 502, "auth_error", err.Error())
		return
	}

	streaming := strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") ||
		strings.Contains(string(body), `"stream":true`) || strings.Contains(string(body), `"stream": true`)

	var resp *http.Response
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamBase+r.URL.Path, strings.NewReader(string(body)))
		if err != nil {
			writeJSONError(w, 500, "request_error", err.Error())
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		ct := r.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
		if streaming {
			req.Header.Set("Accept", "text/event-stream")
		} else if a := r.Header.Get("Accept"); a != "" {
			req.Header.Set("Accept", a)
		}
		req.Header.Set("User-Agent", "metaphor-bridge-go/1.0")

		resp, err = httpClient.Do(req)
		if err != nil {
			writeJSONError(w, 502, "upstream_error", err.Error())
			return
		}
		if resp.StatusCode != 429 && resp.StatusCode != 503 {
			break
		}
		wait := 2 * time.Second
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && n > 0 {
				wait = time.Duration(n) * time.Second
			}
		}
		resp.Body.Close()
		if attempt == 3 {
			writeJSONError(w, 429, "rate_limit_error", "upstream still rate limited after retries")
			return
		}
		logf("429 from upstream, retrying in %s (attempt %d)", wait, attempt+1)
		select {
		case <-time.After(wait):
		case <-r.Context().Done():
			return
		}
	}
	defer resp.Body.Close()

	w.Header().Set("X-Metaphor-Upstream-Proto", resp.Proto)
	ctype := resp.Header.Get("Content-Type")
	if ctype == "" {
		ctype = "application/json"
	}
	w.Header().Set("Content-Type", ctype)
	w.WriteHeader(resp.StatusCode)

	if strings.Contains(ctype, "text/event-stream") {
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 8192)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	}
	io.Copy(w, resp.Body)
}

func writeJSONError(w http.ResponseWriter, code int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": msg},
	})
}

func serve(port int, concurrency int) error {
	b := &bridge{store: newTokenStore(), sem: make(chan struct{}, concurrency)}
	mux := b.handler()
	h2s := &http2.Server{}
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           h2c.NewHandler(mux, h2s),
		ReadHeaderTimeout: 20 * time.Second,
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	logf("metaphor-bridge-go listening on http://%s (h2c), concurrency=%d -> %s", srv.Addr, concurrency, upstreamBase)
	return srv.Serve(ln)
}

// ---------- main ----------

func main() {
	var (
		port          = flag.Int("port", envInt("METAPHOR_PORT", 8190), "listen port")
		concurrency   = flag.Int("concurrency", envInt("METAPHOR_CONCURRENCY", 1), "max in-flight upstream requests")
		proxyURL      = flag.String("proxy", os.Getenv("METAPHOR_PROXY"), "egress proxy: http://user:pass@host:port, https://…, socks5://…")
		proxyInsecure = flag.Bool("proxy-insecure", envBool("METAPHOR_PROXY_INSECURE", false), "skip TLS verification of the proxy itself (self-signed proxies)")
	)
	flag.Usage = usage
	flag.Parse()

	pc, err := parseProxy(*proxyURL, *proxyInsecure)
	if err != nil {
		log.Fatal(err)
	}
	proxyCfg = pc
	httpClient = clientThroughProxy(pc)
	if pc != nil {
		logf("egress proxy: %s (insecure=%v)", pc.String(), pc.Insecure)
	}

	args := flag.Args()
	if len(args) == 0 || args[0] == "serve" {
		log.Fatal(serve(*port, *concurrency))
	}
	switch args[0] {
	case "auth":
		if len(args) < 2 {
			fmt.Println("usage: metaphor auth {start|finish|refresh|show|gh}")
			os.Exit(1)
		}
		cmdAuth(args[1], args[2:])
	default:
		fmt.Println("usage: metaphor [serve|auth ...]")
		os.Exit(1)
	}
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

var _ = context.Background
var _ sync.Mutex
