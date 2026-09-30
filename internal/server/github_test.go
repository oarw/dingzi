package server

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func githubTestConfig() *GitHubConfig {
	return &GitHubConfig{ClientID: "fixture-client", ClientSecret: "fixture-secret",
		CallbackURL: "https://panel.example/auth/github/callback", AllowedUserIDs: []int64{42}}
}

func githubTestPanel(t *testing.T) *Server {
	t.Helper()
	base, _ := testPanel(t)
	options := base.opts
	options.GitHub, options.SecureCookie = githubTestConfig(), true
	s, err := New(options, base.store, base.log)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func githubRequest(s *Server, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if strings.Contains(target, "/callback") {
		// A real provider callback is a cross-site top-level navigation.
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func startGitHub(t *testing.T, s *Server) (*url.URL, *http.Cookie) {
	t.Helper()
	w := githubRequest(s, "https://panel.example/auth/github", nil)
	if w.Code != http.StatusFound {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "github.com" || u.Query().Get("redirect_uri") != s.github.config.CallbackURL || u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("scope") != "" || u.Query().Get("client_secret") != "" {
		t.Fatal("unexpected authorization redirect")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == githubStateCookie {
			if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/auth/github" || c.MaxAge != 300 {
				t.Fatal("unsafe state cookie")
			}
			return u, c
		}
	}
	t.Fatal("missing browser cookie")
	return nil, nil
}

func TestGitHubConfigValidation(t *testing.T) {
	if g, err := newGitHubAuth(nil, false); err != nil || g != nil {
		t.Fatal("GitHub should default to disabled")
	}
	for _, tc := range []struct {
		name          string
		edit          func(*GitHubConfig)
		secure, valid bool
	}{
		{"https", func(*GitHubConfig) {}, true, true},
		{"insecure cookies", func(*GitHubConfig) {}, false, false},
		{"missing client", func(c *GitHubConfig) { c.ClientID = "" }, true, false},
		{"missing secret", func(c *GitHubConfig) { c.ClientSecret = " " }, true, false},
		{"open enrollment", func(c *GitHubConfig) { c.AllowedUserIDs = nil }, true, false},
		{"zero id", func(c *GitHubConfig) { c.AllowedUserIDs = []int64{0} }, true, false},
		{"negative id", func(c *GitHubConfig) { c.AllowedUserIDs = []int64{-1} }, true, false},
		{"remote http", func(c *GitHubConfig) { c.CallbackURL = "http://panel.example/auth/github/callback" }, false, false},
		{"loopback", func(c *GitHubConfig) { c.CallbackURL = "http://127.0.0.1:8008/auth/github/callback" }, false, true},
		{"loopback secure", func(c *GitHubConfig) { c.CallbackURL = "http://127.0.0.1:8008/auth/github/callback" }, true, false},
		{"wrong path", func(c *GitHubConfig) { c.CallbackURL = "https://panel.example/elsewhere" }, true, false},
		{"query", func(c *GitHubConfig) { c.CallbackURL += "?next=other" }, true, false},
		{"fragment", func(c *GitHubConfig) { c.CallbackURL += "#x" }, true, false},
		{"credentials", func(c *GitHubConfig) { c.CallbackURL = "https://user@panel.example/auth/github/callback" }, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := githubTestConfig()
			tc.edit(cfg)
			_, err := newGitHubAuth(cfg, tc.secure)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestGitHubLoginAuthorizationAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, tokenBody, userBody string
		tokenStatus, userStatus   int
		want                      string
	}{
		{"authorized", `{"access_token":"fixture-access-token","token_type":"bearer"}`, `{"id":42,"login":"renamed-admin"}`, 200, 200, "/admin/"},
		{"username taken over", `{"access_token":"fixture-access-token","token_type":"bearer"}`, `{"id":43,"login":"original-admin"}`, 200, 200, "/admin/login?github_error=denied"},
		{"token rejected", `{"error":"bad_verification_code"}`, `{}`, 200, 200, "/admin/login?github_error=failed"},
		{"provider unavailable", `{}`, `{}`, 503, 200, "/admin/login?github_error=failed"},
		{"user unavailable", `{"access_token":"fixture-access-token","token_type":"bearer"}`, `{}`, 200, 403, "/admin/login?github_error=failed"},
		{"missing id", `{"access_token":"fixture-access-token","token_type":"bearer"}`, `{"login":"admin"}`, 200, 200, "/admin/login?github_error=failed"},
		{"invalid json", `not json`, `{}`, 200, 200, "/admin/login?github_error=failed"},
		{"oversize body", strings.Repeat("x", (64<<10)+1), `{}`, 200, 200, "/admin/login?github_error=failed"},
		{"wrong token type", `{"access_token":"fixture-access-token","token_type":"mac"}`, `{}`, 200, 200, "/admin/login?github_error=failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := githubTestPanel(t)
			u, cookie := startGitHub(t, s)
			var exchanges atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/token":
					exchanges.Add(1)
					if r.Method != "POST" {
						t.Error("token exchange is not POST")
					}
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if r.Form.Get("code") != "fixture-code" || r.Form.Get("client_secret") != "fixture-secret" || r.Form.Get("redirect_uri") != s.github.config.CallbackURL {
						t.Error("bad token exchange parameters")
					}
					verifier := r.Form.Get("code_verifier")
					hash := sha256.Sum256([]byte(verifier))
					if len(verifier) < 43 || base64.RawURLEncoding.EncodeToString(hash[:]) != u.Query().Get("code_challenge") {
						t.Error("PKCE mismatch")
					}
					w.WriteHeader(tc.tokenStatus)
					fmt.Fprint(w, tc.tokenBody)
				case "/user":
					if r.Header.Get("Authorization") != "Bearer fixture-access-token" || r.Method != "GET" {
						t.Error("missing bearer token")
					}
					w.WriteHeader(tc.userStatus)
					fmt.Fprint(w, tc.userBody)
				default:
					t.Error("unexpected provider request")
					http.NotFound(w, r)
				}
			}))
			defer provider.Close()
			s.github.tokenURL, s.github.userURL = provider.URL+"/token", provider.URL+"/user"
			target := s.github.config.CallbackURL + "?code=fixture-code&state=" + u.Query().Get("state")
			w := githubRequest(s, target, cookie)
			if w.Code != 303 || w.Header().Get("Location") != tc.want || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatalf("callback: %d %s", w.Code, w.Header())
			}
			var session *http.Cookie
			for _, c := range w.Result().Cookies() {
				if c.Name == sessionCookie {
					session = c
				}
			}
			if tc.want == "/admin/" {
				if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
					t.Fatal("missing/unsafe session")
				}
				if got := requestJSON(s, session, "GET", "/api/v1/servers", ""); got.Code != 200 {
					t.Fatal("GitHub session cannot access admin API")
				}
				if got := requestJSON(s, session, "POST", "/api/v1/logout", ""); got.Code != 200 {
					t.Fatal("logout failed")
				}
				if s.sessions.valid(session.Value) {
					t.Fatal("logout did not revoke GitHub session")
				}
			} else if session != nil {
				t.Fatal("failed login issued a session")
			}
			if strings.Contains(w.Body.String(), "fixture-access-token") || strings.Contains(w.Header().Get("Location"), "fixture-code") {
				t.Fatal("provider credentials leaked")
			}
			if replay := githubRequest(s, target, cookie); replay.Header().Get("Location") != "/admin/login?github_error=failed" {
				t.Fatal("state replay succeeded")
			}
			if exchanges.Load() != 1 {
				t.Fatal("callback replay made a provider request")
			}
		})
	}
}

func TestGitHubStateBindingExpiryAndBounds(t *testing.T) {
	g, err := newGitHubAuth(githubTestConfig(), true)
	if err != nil {
		t.Fatal(err)
	}
	state, _, ok := g.issue("browser-a")
	if !ok {
		t.Fatal("issue failed")
	}
	if _, ok = g.take(state, "browser-b"); ok {
		t.Fatal("another browser consumed state")
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, ok := g.take(state, "browser-a"); ok {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("state was not consumed exactly once")
	}
	state, _, _ = g.issue("browser-a")
	p := g.pending[state]
	p.expires = time.Now().Add(-time.Second)
	g.pending[state] = p
	if _, ok = g.take(state, "browser-a"); ok {
		t.Fatal("expired state accepted")
	}
	for range githubMaxPending {
		if _, _, ok := g.issue("browser"); !ok {
			t.Fatal("premature capacity limit")
		}
	}
	if _, _, ok := g.issue("overflow"); ok || len(g.pending) != githubMaxPending {
		t.Fatal("pending state storage is not bounded")
	}
	for key, p := range g.pending {
		p.expires = time.Now().Add(-time.Second)
		g.pending[key] = p
	}
	if _, _, ok := g.issue("browser"); !ok || len(g.pending) != 1 {
		t.Fatal("expired states not pruned")
	}
}

func TestGitHubCallbackRejectsInvalidRequestsBeforeExchange(t *testing.T) {
	for _, kind := range []string{"no cookie", "wrong browser", "wrong state", "expired", "denied", "missing code", "busy"} {
		t.Run(kind, func(t *testing.T) {
			s := githubTestPanel(t)
			u, cookie := startGitHub(t, s)
			state := u.Query().Get("state")
			query := url.Values{"state": {state}, "code": {"fixture-code"}}
			switch kind {
			case "no cookie":
				cookie = nil
			case "wrong browser":
				cookie.Value = "another-browser"
			case "wrong state":
				query.Set("state", "not-issued")
			case "expired":
				p := s.github.pending[state]
				p.expires = time.Now().Add(-time.Second)
				s.github.pending[state] = p
			case "denied":
				query.Set("error", "access_denied")
			case "missing code":
				query.Del("code")
			case "busy":
				for range cap(s.github.inflight) {
					s.github.inflight <- struct{}{}
				}
			}
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
			defer provider.Close()
			s.github.tokenURL = provider.URL
			w := githubRequest(s, s.github.config.CallbackURL+"?"+query.Encode(), cookie)
			if w.Header().Get("Location") != "/admin/login?github_error=failed" || calls.Load() != 0 {
				t.Fatal("invalid callback reached provider")
			}
		})
	}
}

func TestGitHubProviderRedirectDoesNotLeakCredentials(t *testing.T) {
	s := githubTestPanel(t)
	u, cookie := startGitHub(t, s)
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer other.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) }))
	defer provider.Close()
	s.github.tokenURL = provider.URL
	w := githubRequest(s, s.github.config.CallbackURL+"?code=fixture-code&state="+u.Query().Get("state"), cookie)
	if leaked.Load() || w.Header().Get("Location") != "/admin/login?github_error=failed" {
		t.Fatal("token request followed provider redirect")
	}
}

func TestGitHubEntryAndLocalRecovery(t *testing.T) {
	disabled, _ := testPanel(t)
	if w := requestJSON(disabled, nil, "GET", "/admin/login", ""); strings.Contains(w.Body.String(), "使用 GitHub 登录") {
		t.Fatal("disabled provider displayed")
	}
	for _, path := range []string{"/auth/github", "/auth/github/callback"} {
		if w := requestJSON(disabled, nil, "GET", path, ""); w.Code != 404 {
			t.Fatal("disabled route available")
		}
	}
	s := githubTestPanel(t)
	if w := requestJSON(s, nil, "GET", "/admin/login", ""); !strings.Contains(w.Body.String(), "使用 GitHub 登录") || strings.Contains(w.Body.String(), "fixture-secret") {
		t.Fatal("bad configured login page")
	}
	if w := githubRequest(s, "https://wrong.example/auth/github", nil); w.Code != 400 {
		t.Fatal("unconfigured host accepted")
	}
	r := httptest.NewRequest("GET", "https://panel.example/auth/github", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-site login initiation accepted")
	}
	if w := requestJSON(s, nil, "HEAD", "https://panel.example/auth/github", ""); w.Code != 405 {
		t.Fatal("HEAD issued OAuth state")
	}
	hash, err := HashPassword("local-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	s.opts.PasswordHash = hash
	w = requestJSON(s, nil, "POST", "/api/v1/login", `{"password":"local-fixture-password"}`)
	if w.Code != 200 {
		t.Fatal("GitHub configuration broke local password recovery")
	}
}
