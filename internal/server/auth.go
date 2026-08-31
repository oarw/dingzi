package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "dingzi_session"
	sessionTTL    = 7 * 24 * time.Hour
	// loginMinInterval throttles password attempts per client. Not a defence
	// against a distributed attacker, but it turns an online brute force from
	// thousands of guesses a second into one, which is the difference between
	// minutes and centuries.
	loginMinInterval = 1 * time.Second
)

// sessionStore holds logged-in sessions in memory.
//
// Deliberately not persisted: a panel restart logging everyone out is a
// feature, and it means a stolen database file does not contain live session
// tokens. The cost is re-entering a password after an upgrade.
type sessionStore struct {
	mu      sync.Mutex
	tokens  map[string]time.Time
	lastTry map[string]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{tokens: map[string]time.Time{}, lastTry: map[string]time.Time{}}
}

// issue mints a session token.
func (s *sessionStore) issue() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	// Sweep on issue rather than with a background timer: both maps would
	// otherwise grow for the life of the process, which is exactly the kind of
	// "bounded in practice" that turns into a leak on a panel left running for
	// a year.
	now := time.Now()
	for t, exp := range s.tokens {
		if now.After(exp) {
			delete(s.tokens, t)
		}
	}
	for ip, when := range s.lastTry {
		if now.Sub(when) > time.Hour {
			delete(s.lastTry, ip)
		}
	}
	s.tokens[tok] = now.Add(sessionTTL)
	return tok, nil
}

func (s *sessionStore) valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.tokens, tok)
		return false
	}
	return true
}

func (s *sessionStore) revoke(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, tok)
}

// throttle reports whether this client may attempt a login now.
func (s *sessionStore) throttle(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if last, ok := s.lastTry[ip]; ok && now.Sub(last) < loginMinInterval {
		return false
	}
	s.lastTry[ip] = now
	return true
}

// HashPassword produces a stored password hash.
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

// checkPassword verifies a password against the configured hash.
//
// Fails closed when no hash is configured. An unconfigured panel accepting any
// password is the worst possible default, and it is an easy one to arrive at by
// treating "empty hash" as "no password required".
func (s *Server) checkPassword(pw string) bool {
	if s.opts.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(s.opts.PasswordHash), []byte(pw)) == nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.sessions.throttle(ip) {
		writeErr(w, http.StatusTooManyRequests, "请稍候再试")
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if !s.checkPassword(body.Password) {
		// One message for a wrong password and for a panel with none set: the
		// difference is useful to an attacker and to nobody else.
		s.log.Warn("failed login", "ip", ip)
		writeErr(w, http.StatusUnauthorized, "密码不正确")
		return
	}

	tok, err := s.sessions.issue()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "无法创建会话")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.opts.SecureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	s.log.Info("login", "ip", ip)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: s.opts.SecureCookie,
		SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authed": s.authed(r)})
}

// authed reports whether the request carries a valid session.
func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return s.sessions.valid(c.Value)
}

// requireAuth wraps a handler that must not run unauthenticated.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			writeErr(w, http.StatusUnauthorized, "需要登录")
			return
		}
		next(w, r)
	}
}

// clientIP extracts the caller's address.
//
// X-Forwarded-For is honoured because the panel is expected to sit behind a
// reverse proxy, where RemoteAddr is the proxy. It is spoofable by anyone
// talking to the panel directly, which is why it is only ever used to bound a
// throttle and to write a log line — never to authorise anything.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := len(xff); i > 0 {
			for j := 0; j < len(xff); j++ {
				if xff[j] == ',' {
					return trimSpace(xff[:j])
				}
			}
			return trimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
