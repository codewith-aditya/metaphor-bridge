package main

// Token store + WorkOS PKCE helpers for the Metaphor gateway.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	workosClientID = "client_01KAHJW6K5B09TYCG4P597H63N"
	workosAuthURL  = "https://api.workos.com/user_management/authorize"
	workosAuthnURL = "https://api.workos.com/user_management/authenticate"
	redirectURI    = "metaphor://auth/callback"
)

var baseDir = func() string {
	if v := os.Getenv("METAPHOR_DIR"); v != "" {
		return v
	}
	return "/root/metaphor"
}()

func tokensPath() string   { return filepath.Join(baseDir, "tokens.json") }
func pkcePath() string     { return filepath.Join(baseDir, "pkce.json") }
func loginURLPath() string { return filepath.Join(baseDir, "login_url.txt") }

type tokenStore struct {
	mu  sync.Mutex
	tok map[string]any
}

func newTokenStore() *tokenStore {
	s := &tokenStore{}
	s.load()
	return s
}

func (s *tokenStore) load() {
	b, err := os.ReadFile(tokensPath())
	if err != nil {
		return
	}
	var m map[string]any
	if json.Unmarshal(b, &m) == nil {
		s.tok = m
	}
}

func (s *tokenStore) save(m map[string]any) error {
	m["saved_at"] = time.Now().Unix()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := tokensPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, tokensPath())
}

func jwtExp(tok string) int64 {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return 0
	}
	p := parts[1]
	if m := len(p) % 4; m != 0 {
		p += strings.Repeat("=", 4-m)
	}
	raw, err := base64.URLEncoding.DecodeString(p)
	if err != nil {
		return 0
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return 0
	}
	return claims.Exp
}

// AccessToken returns a valid bearer token, refreshing when within 5 minutes of expiry.
func (s *tokenStore) AccessToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok == nil {
		s.load()
	}
	at, _ := s.tok["access_token"].(string)
	if at == "" {
		return "", errors.New("no access token; run: metaphor auth login")
	}
	exp := jwtExp(at)
	if exp == 0 {
		if saved, ok := s.tok["saved_at"].(float64); ok {
			exp = int64(saved) + 3600
		}
	}
	if time.Now().Unix() < exp-300 {
		return at, nil
	}
	rt, _ := s.tok["refresh_token"].(string)
	if rt == "" {
		return "", errors.New("token expired and no refresh_token; re-run auth")
	}
	m, err := workosAuthenticate(map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     workosClientID,
		"refresh_token": rt,
	})
	if err != nil {
		return "", fmt.Errorf("refresh failed: %w", err)
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
	at, _ = m["access_token"].(string)
	if at == "" {
		return "", errors.New("refresh returned no access_token")
	}
	logf("token refreshed, expires %s", time.Unix(jwtExp(at), 0).Format(time.RFC3339))
	return at, nil
}

func workosAuthenticate(payload map[string]any) (map[string]any, error) {
	body, _ := json.Marshal(payload)
	resp, err := httpClient.Post(workosAuthnURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("bad JSON from WorkOS (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		b, _ := json.Marshal(out)
		return nil, fmt.Errorf("WorkOS HTTP %d: %s", resp.StatusCode, truncate(string(b), 300))
	}
	return out, nil
}

func randomB64(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

type pkceState struct {
	Verifier string `json:"verifier"`
	State    string `json:"state"`
	Created  int64  `json:"created"`
}

func writePKCE(p pkceState) error {
	b, _ := json.Marshal(p)
	return os.WriteFile(pkcePath(), b, 0o600)
}

func readPKCE() (pkceState, error) {
	var p pkceState
	b, err := os.ReadFile(pkcePath())
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(b, &p)
	return p, err
}

func buildAuthorizeURL() (string, error) {
	verifier := randomB64(32)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomB64(16)
	if err := writePKCE(pkceState{Verifier: verifier, State: state, Created: time.Now().Unix()}); err != nil {
		return "", err
	}
	u := fmt.Sprintf("%s?client_id=%s&provider=GitHubOAuth&redirect_uri=%s&response_type=code&code_challenge=%s&code_challenge_method=S256&state=%s",
		workosAuthURL, workosClientID, "%s", challenge, state)
	u = strings.Replace(u, "%s", urlQueryEscape(redirectURI), 1)
	_ = os.WriteFile(loginURLPath(), []byte(u), 0o644)
	return u, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
