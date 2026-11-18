package main

// Auth subcommands: start / finish / refresh / show / gh.

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

func cmdAuth(sub string, args []string) {
	switch sub {
	case "start":
		u, err := buildAuthorizeURL()
		die(err)
		fmt.Println(u)
	case "finish":
		if len(args) < 1 {
			die(errors.New("usage: auth finish <url-or-code>"))
		}
		code := extractCode(args[0])
		if code == "" {
			die(errors.New("no code= found in argument"))
		}
		if strings.Contains(args[0], "auth.workos.com") && strings.Contains(args[0], "iss=") {
			// GitHub callback URL: do the WorkOS hop ourselves
			wloc, err := hopNoRedirect(args[0], "")
			die(err)
			code = codeFromRedirect(wloc)
			if code == "" {
				die(fmt.Errorf("no WorkOS code in redirect: %s", truncate(wloc, 200)))
			}
		}
		die(exchangeCode(code))
	case "refresh":
		st := newTokenStore()
		st.mu.Lock()
		// force expiry so AccessToken refreshes
		if st.tok != nil {
			if at, _ := st.tok["access_token"].(string); at != "" {
				exp := time.Now().Add(-time.Hour)
				_ = exp
			}
		}
		st.mu.Unlock()
		tok, err := st.forceRefresh()
		die(err)
		fmt.Println("refreshed, expires", time.Unix(jwtExp(tok), 0).Format(time.RFC3339))
	case "show":
		st := newTokenStore()
		st.load()
		if st.tok == nil {
			die(errors.New("no tokens.json"))
		}
		at, _ := st.tok["access_token"].(string)
		fmt.Printf("access_token : %s...%s (len %d)\n", at[:min(12, len(at))], at[max(0, len(at)-6):], len(at))
		fmt.Printf("expires_at   : %s\n", time.Unix(jwtExp(at), 0).Format(time.RFC3339))
		if u, ok := st.tok["user"].(map[string]any); ok {
			fmt.Printf("email        : %v\n", u["email"])
		}
		fmt.Printf("refresh_token: %v\n", st.tok["refresh_token"] != nil)
	case "gh":
		if len(args) < 1 {
			die(errors.New(`usage: auth gh "user_session=...; _gh_sess=..."`))
		}
		die(ghFlow(args[0]))
	default:
		die(fmt.Errorf("unknown auth command %q", sub))
	}
}

func (s *tokenStore) forceRefresh() (string, error) {
	rt, _ := s.tok["refresh_token"].(string)
	if rt == "" {
		return "", errors.New("no refresh_token stored")
	}
	m, err := workosAuthenticate(map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     workosClientID,
		"refresh_token": rt,
	})
	if err != nil {
		return "", err
	}
	if _, ok := m["refresh_token"]; !ok {
		m["refresh_token"] = rt
	}
	if _, ok := m["user"]; !ok {
		m["user"] = s.tok["user"]
	}
	if err := s.save(m); err != nil {
		return "", err
	}
	s.tok = m
	at, _ := m["access_token"].(string)
	return at, nil
}

func exchangeCode(code string) error {
	p, err := readPKCE()
	if err != nil {
		return fmt.Errorf("read pkce.json: %w", err)
	}
	m, err := workosAuthenticate(map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     workosClientID,
		"code":          code,
		"code_verifier": p.Verifier,
	})
	if err != nil {
		return err
	}
	st := &tokenStore{}
	if err := st.save(m); err != nil {
		return err
	}
	at, _ := m["access_token"].(string)
	fmt.Println("tokens saved ->", tokensPath())
	fmt.Println("  email          :", userEmail(m))
	fmt.Println("  organization_id:", m["organization_id"])
	fmt.Printf("  access_token   : %s...%s (len %d)\n", at[:12], at[len(at)-6:], len(at))
	fmt.Println("  expires_at     :", time.Unix(jwtExp(at), 0).Format(time.RFC3339))
	fmt.Println("  refresh_token  :", m["refresh_token"] != nil)
	return nil
}

func ghFlow(cookie string) error {
	authURL, err := os.ReadFile(loginURLPath())
	if err != nil || len(authURL) == 0 {
		authURL = []byte(mustAuthorizeURL())
	}
	ghURL, err := hopNoRedirect(string(authURL), "")
	if err != nil {
		return err
	}
	logf("1) workos -> %s", truncate(ghURL, 70))
	if !strings.Contains(ghURL, "github.com") {
		return fmt.Errorf("no github redirect: %s", truncate(ghURL, 120))
	}

	loc, page, err := hop(ghURL, cookie)
	if err != nil {
		return err
	}
	switch {
	case strings.Contains(loc, "auth.workos.com"):
		logf("2) github auto-approved (grant exists)")
	case strings.Contains(loc, "metaphor://"):
		logf("2) github auto-approved -> app redirect")
		code := codeFromRedirect(loc)
		if code == "" {
			return fmt.Errorf("no code in app redirect")
		}
		return exchangeCode(code)
	default:
		if !strings.Contains(page, `action="/login/oauth/authorize"`) {
			return fmt.Errorf("no authorize form (last hop %s) -> GitHub cookies invalid or rate limited", truncate(loc, 60))
		}
		fields := map[string]string{"authorize": "1"}
		tokRe := regexp.MustCompile(`name="authenticity_token"\s+value="([^"]+)"`)
		if m := tokRe.FindStringSubmatch(page); len(m) == 2 {
			fields["authenticity_token"] = html.UnescapeString(m[1])
		}
		inRe := regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`)
		for _, m := range inRe.FindAllStringSubmatch(page, -1) {
			if _, ok := fields[m[1]]; !ok {
				fields[m[1]] = html.UnescapeString(m[2])
			}
		}
		form := url.Values{}
		for k, v := range fields {
			form.Set(k, v)
		}
		req, _ := http.NewRequest("POST", "https://github.com/login/oauth/authorize", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Referer", ghURL)
		req.Header.Set("User-Agent", uaString)
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		loc = resp.Header.Get("Location")
		logf("2) github approve -> %s", truncate(loc, 70))
		if !strings.Contains(loc, "auth.workos.com") {
			return fmt.Errorf("approve did not reach workos: %s", truncate(loc, 120))
		}
	}

	wloc, err := hopNoRedirect(loc, "")
	if err != nil {
		return err
	}
	logf("3) workos -> %s", truncate(wloc, 70))
	if u, err := url.Parse(wloc); err == nil {
		if e := u.Query().Get("error"); e != "" {
			return fmt.Errorf("workos error: %s (%s)", e, u.Query().Get("error_description"))
		}
	}
	code := codeFromRedirect(wloc)
	if code == "" {
		return fmt.Errorf("no workos code in %s", truncate(wloc, 120))
	}
	return exchangeCode(code)
}

const uaString = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"

func hop(u, cookie string) (location, body string, err error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", uaString)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return resp.Header.Get("Location"), string(b), nil
}

func hopNoRedirect(u, cookie string) (string, error) {
	loc, _, err := hop(u, cookie)
	return loc, err
}

func codeFromRedirect(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil {
		return ""
	}
	return u.Query().Get("code")
}

var codeRe = regexp.MustCompile(`[?&]code=([^&#]+)`)

func extractCode(arg string) string {
	arg = strings.TrimSpace(strings.Trim(arg, `"'`))
	if strings.Contains(arg, "code=") {
		if u, err := url.Parse(arg); err == nil {
			if c := u.Query().Get("code"); c != "" {
				return c
			}
		}
		if m := codeRe.FindStringSubmatch(arg); len(m) == 2 {
			if dec, err := url.QueryUnescape(m[1]); err == nil {
				return dec
			}
			return m[1]
		}
	}
	return strings.SplitN(arg, "#", 2)[0]
}

func mustAuthorizeURL() string {
	u, err := buildAuthorizeURL()
	if err != nil {
		die(err)
	}
	return u
}

func userEmail(m map[string]any) any {
	if u, ok := m["user"].(map[string]any); ok {
		return u["email"]
	}
	return nil
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ = json.Marshal
