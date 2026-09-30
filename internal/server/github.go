package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	githubCallbackPath = "/auth/github/callback"
	githubStateCookie  = "dingzi_github_state"
	githubStateTTL     = 5 * time.Minute
	githubMaxPending   = 256
)

// GitHubConfig is only read from the operator's local config, never a public API.
// Numeric IDs survive account renames; a new owner of an old login gains no access.
type GitHubConfig struct {
	ClientID       string  `yaml:"client_id"`
	ClientSecret   string  `yaml:"client_secret"`
	CallbackURL    string  `yaml:"callback_url"`
	AllowedUserIDs []int64 `yaml:"allowed_user_ids"`
}

type githubPending struct {
	browserHash [32]byte
	verifier    string
	expires     time.Time
}

type githubAuth struct {
	config       GitHubConfig
	callbackHost string
	allowed      map[int64]bool
	client       *http.Client
	authorizeURL string
	tokenURL     string
	userURL      string
	mu           sync.Mutex
	pending      map[string]githubPending
	inflight     chan struct{}
}

func newGitHubAuth(cfg *GitHubConfig, secureCookie bool) (*githubAuth, error) {
	if cfg == nil {
		return nil, nil
	}
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" || len(cfg.AllowedUserIDs) == 0 {
		return nil, errors.New("github requires client_id, client_secret and allowed_user_ids")
	}
	u, err := url.Parse(cfg.CallbackURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != githubCallbackPath || u.RawPath != "" {
		return nil, errors.New("github callback_url must be an absolute URL ending in /auth/github/callback without credentials, query or fragment")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback && !secureCookie) {
		return nil, errors.New("github callback_url requires HTTPS (plain HTTP is allowed only on loopback without secure-cookie)")
	}
	if u.Scheme == "https" && !secureCookie {
		return nil, errors.New("github HTTPS login requires --secure-cookie")
	}
	allowed := make(map[int64]bool, len(cfg.AllowedUserIDs))
	for _, id := range cfg.AllowedUserIDs {
		if id <= 0 {
			return nil, errors.New("github allowed_user_ids must contain positive numeric user IDs")
		}
		allowed[id] = true
	}
	copyConfig := *cfg
	copyConfig.AllowedUserIDs = append([]int64(nil), cfg.AllowedUserIDs...)
	return &githubAuth{
		config: copyConfig, callbackHost: u.Host, allowed: allowed,
		client:       &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		authorizeURL: "https://github.com/login/oauth/authorize",
		tokenURL:     "https://github.com/login/oauth/access_token",
		userURL:      "https://api.github.com/user",
		pending:      make(map[string]githubPending),
		inflight:     make(chan struct{}, 16),
	}, nil
}

func (g *githubAuth) issue(browser string) (string, string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for state, p := range g.pending {
		if !now.Before(p.expires) {
			delete(g.pending, state)
		}
	}
	if len(g.pending) >= githubMaxPending {
		return "", "", false
	}
	state, verifier := rand.Text(), rand.Text()+rand.Text()
	g.pending[state] = githubPending{browserHash: sha256.Sum256([]byte(browser)), verifier: verifier, expires: now.Add(githubStateTTL)}
	challenge := sha256.Sum256([]byte(verifier))
	return state, base64.RawURLEncoding.EncodeToString(challenge[:]), true
}

// take atomically consumes a state only for the browser that initiated it.
func (g *githubAuth) take(state, browser string) (githubPending, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.pending[state]
	if !ok {
		return githubPending{}, false
	}
	if !time.Now().Before(p.expires) {
		delete(g.pending, state)
		return githubPending{}, false
	}
	hash := sha256.Sum256([]byte(browser))
	if browser == "" || subtle.ConstantTimeCompare(hash[:], p.browserHash[:]) != 1 {
		return githubPending{}, false
	}
	delete(g.pending, state)
	return p, true
}

func (s *Server) githubRequest(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.github == nil {
		http.NotFound(w, r)
		return false
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErr(w, http.StatusMethodNotAllowed, "请使用浏览器登录")
		return false
	}
	// The configured callback, never Host/X-Forwarded-Host, defines the redirect.
	if r.Host != s.github.callbackHost {
		writeErr(w, http.StatusBadRequest, "请从配置的面板地址使用 GitHub 登录")
		return false
	}
	return true
}

func (s *Server) handleGitHubStart(w http.ResponseWriter, r *http.Request) {
	if !s.githubRequest(w, r) {
		return
	}
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "请求来源不正确")
		return
	}
	if !s.sessions.throttle(clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "请稍候再试")
		return
	}
	browser := rand.Text()
	state, challenge, ok := s.github.issue(browser)
	if !ok {
		writeErr(w, http.StatusTooManyRequests, "登录请求较多，请稍候再试")
		return
	}
	params := url.Values{
		"client_id": {s.github.config.ClientID}, "redirect_uri": {s.github.config.CallbackURL},
		"state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
		// Empty scope requests only public identity, never repo/email permissions.
		"scope":  {""},
		"prompt": {"select_account"},
	}
	http.SetCookie(w, &http.Cookie{
		Name: githubStateCookie, Value: browser, Path: "/auth/github",
		HttpOnly: true, Secure: s.opts.SecureCookie, SameSite: http.SameSiteLaxMode,
		MaxAge: int(githubStateTTL.Seconds()),
	})
	http.Redirect(w, r, s.github.authorizeURL+"?"+params.Encode(), http.StatusFound)
}

func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if !s.githubRequest(w, r) {
		return
	}
	fail := func(reason string) {
		http.Redirect(w, r, "/admin/login?github_error="+reason, http.StatusSeeOther)
	}
	cookie, err := r.Cookie(githubStateCookie)
	if err != nil {
		fail("failed")
		return
	}
	pending, ok := s.github.take(r.URL.Query().Get("state"), cookie.Value)
	if !ok {
		fail("failed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: githubStateCookie, Path: "/auth/github", HttpOnly: true,
		Secure: s.opts.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	code := r.URL.Query().Get("code")
	if r.URL.Query().Get("error") != "" || code == "" || len(code) > 4096 {
		fail("failed")
		return
	}
	select {
	case s.github.inflight <- struct{}{}:
		defer func() { <-s.github.inflight }()
	default:
		fail("failed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	id, err := s.github.userID(ctx, code, pending.verifier)
	if err != nil {
		// Never log callback codes, tokens, provider bodies or transport URLs.
		s.log.Warn("github login failed", "ip", clientIP(r))
		fail("failed")
		return
	}
	if !s.github.allowed[id] {
		s.log.Warn("github login denied", "ip", clientIP(r))
		fail("denied")
		return
	}
	token, err := s.sessions.issue()
	if err != nil {
		fail("failed")
		return
	}
	if previous, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.revoke(previous.Value)
	}
	s.setSessionCookie(w, token)
	s.log.Info("github login", "user_id", id, "ip", clientIP(r))
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (g *githubAuth) userID(ctx context.Context, code, verifier string) (int64, error) {
	form := url.Values{
		"client_id": {g.config.ClientID}, "client_secret": {g.config.ClientSecret},
		"code": {code}, "redirect_uri": {g.config.CallbackURL}, "code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
	}
	if err := g.readJSON(req, &token); err != nil {
		return 0, err
	}
	if token.Error != "" || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "bearer") {
		return 0, errors.New("github token exchange failed")
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, g.userURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	var user struct {
		ID int64 `json:"id"`
	}
	if err := g.readJSON(req, &user); err != nil {
		return 0, err
	}
	if user.ID <= 0 {
		return 0, errors.New("github returned no user ID")
	}
	return user.ID, nil
}

func (g *githubAuth) readJSON(req *http.Request, dst any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dingzi-panel")
	res, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("github HTTP status %d", res.StatusCode)
	}
	const limit = 64 << 10
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return err
	}
	if len(body) > limit {
		return errors.New("github response too large")
	}
	return json.Unmarshal(body, dst)
}
