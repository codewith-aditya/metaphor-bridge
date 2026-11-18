package main

// Optional egress proxy for every upstream request (gateway, WorkOS, GitHub).
// Supports http://, https:// (self-signed proxies via Insecure) and socks5://.

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

type proxyConfig struct {
	URL      *url.URL
	Insecure bool // skip verification of the *proxy's* TLS cert only
}

func parseProxy(raw string, insecure bool) (*proxyConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("bad proxy URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy URL has no host")
	}
	if u.Port() == "" {
		port := "80"
		switch u.Scheme {
		case "https":
			port = "443"
		default:
			port = "1080"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		u.Host = net.JoinHostPort(u.Hostname(), port)
	}
	return &proxyConfig{URL: u, Insecure: insecure}, nil
}

func (p *proxyConfig) String() string {
	if p == nil || p.URL == nil {
		return "direct"
	}
	return p.URL.Scheme + "://" + p.URL.Host
}

func (p *proxyConfig) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if p == nil || p.URL == nil {
		return (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, network, addr)
	}
	switch p.URL.Scheme {
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if p.URL.User != nil {
			pw, _ := p.URL.User.Password()
			auth = &xproxy.Auth{User: p.URL.User.Username(), Password: pw}
		}
		d, err := xproxy.SOCKS5("tcp", p.URL.Host, auth, xproxy.Direct)
		if err != nil {
			return nil, err
		}
		cd, ok := d.(xproxy.ContextDialer)
		if !ok {
			return d.Dial(network, addr)
		}
		return cd.DialContext(ctx, network, addr)
	case "http", "https":
		return p.dialViaConnect(ctx, network, addr)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", p.URL.Scheme)
	}
}

func (p *proxyConfig) dialViaConnect(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", p.URL.Host)
	if err != nil {
		return nil, fmt.Errorf("proxy dial: %w", err)
	}
	if p.URL.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         p.URL.Hostname(),
			InsecureSkipVerify: p.Insecure, //nolint:gosec // self-signed proxies are the norm
			MinVersion:         tls.VersionTLS12,
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("proxy TLS: %w", err)
		}
		conn = tlsConn
	}

	var req strings.Builder
	req.WriteString("CONNECT " + addr + " HTTP/1.1\r\n")
	req.WriteString("Host: " + addr + "\r\n")
	if p.URL.User != nil {
		pw, _ := p.URL.User.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(p.URL.User.Username() + ":" + pw))
		req.WriteString("Proxy-Authorization: Basic " + cred + "\r\n")
	}
	req.WriteString("User-Agent: metaphor-bridge-go/" + version + "\r\n")
	req.WriteString("\r\n")

	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	if _, err := conn.Write([]byte(req.String())); err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT write: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT read: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT rejected: %s", resp.Status)
	}
	conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// bufferedConn keeps bytes the proxy sent right after the CONNECT response.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

func proxyPort(u *url.URL) int {
	if p, err := strconv.Atoi(u.Port()); err == nil {
		return p
	}
	return 0
}
