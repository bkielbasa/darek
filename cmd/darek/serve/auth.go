package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "darek_session"

// AuthConfig is the subset of auth state needed by sign/verify and the
// middleware. Built from cfg.Auth + resolved env values in runServe.
type AuthConfig struct {
	Username     string
	PasswordHash []byte // bcrypt hash bytes
	SessionKey   []byte // HMAC key, ≥32 bytes
	SessionTTL   time.Duration
}

// signSession encodes "<user>|<unix-expiry>|<hex-HMAC>" as base64-url.
func (a AuthConfig) signSession(user string, exp time.Time) string {
	payload := fmt.Sprintf("%s|%d", user, exp.Unix())
	mac := hmac.New(sha256.New, a.SessionKey)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + sig))
}

// verifyCookie returns the username from a valid token, or ("", false).
// "Valid" means: parses, HMAC matches with SessionKey, not expired, and the
// username matches a.Username (so rotating Username invalidates old cookies).
func (a AuthConfig) verifyCookie(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", false
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return "", false
	}
	user, expStr, sig := parts[0], parts[1], parts[2]

	payload := user + "|" + expStr
	mac := hmac.New(sha256.New, a.SessionKey)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(user), []byte(a.Username)) != 1 {
		return "", false
	}
	return user, true
}

// routesAuth registers /login (GET, POST) and /logout (POST) on s.mux.
// Called from Server.routes (Task 5).
func (s *Server) routesAuth() {
	s.mux.HandleFunc("GET /login", s.handleLoginGet)
	s.mux.HandleFunc("POST /login", s.handleLoginPost)
	s.mux.HandleFunc("POST /logout", s.handleLogout)
}

// requireAuth wraps next; bypasses isPublicPath, otherwise redirects to /login
// when no valid cookie is present. On success, refreshes the cookie's expiry
// (rolling session).
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(sessionCookieName)
		if err == nil {
			if _, ok := s.auth.verifyCookie(c.Value); ok {
				s.setSessionCookie(w) // rolling
				next.ServeHTTP(w, r)
				return
			}
		}
		nextURL := url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, "/login?next="+nextURL, http.StatusSeeOther)
	})
}

func isPublicPath(p string) bool {
	return p == "/healthz" || p == "/login" || p == "/logout" ||
		strings.HasPrefix(p, "/static/")
}

// sanitizeNext keeps the user's intended URL safe to redirect back to.
// Must start with `/` and NOT with `//` (would be a network-path open redirect).
// Schemes like javascript: never start with `/`, so the first check filters them.
func sanitizeNext(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") {
		return "/"
	}
	return s
}

func (s *Server) setSessionCookie(w http.ResponseWriter) {
	exp := time.Now().Add(s.auth.SessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.auth.signSession(s.auth.Username, exp),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.auth.SessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

type loginPageData struct {
	Error bool
	Next  string
}

func (s *Server) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	data := loginPageData{
		Error: r.URL.Query().Get("error") == "invalid",
		Next:  sanitizeNext(r.URL.Query().Get("next")),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "login.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	user := r.FormValue("username")
	pw := r.FormValue("password")
	next := sanitizeNext(r.URL.Query().Get("next"))

	userOK := subtleEqual(user, s.auth.Username)
	pwOK := bcrypt.CompareHashAndPassword(s.auth.PasswordHash, []byte(pw)) == nil

	if !userOK || !pwOK {
		http.Redirect(w, r,
			"/login?error=invalid&next="+url.QueryEscape(next),
			http.StatusSeeOther)
		return
	}
	s.setSessionCookie(w)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// subtleEqual is a constant-time string comparison wrapper.
func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// NewAuthConfig builds an AuthConfig from resolved values. Used by runServe.
// Returns an error if any required value is missing or the session key is too
// short.
func NewAuthConfig(username string, passwordHash, sessionKey []byte, ttl time.Duration) (AuthConfig, error) {
	if username == "" {
		return AuthConfig{}, fmt.Errorf("auth: username required")
	}
	if len(passwordHash) == 0 {
		return AuthConfig{}, fmt.Errorf("auth: password hash required")
	}
	if len(sessionKey) < 32 {
		return AuthConfig{}, fmt.Errorf("auth: session key must be at least 32 bytes (got %d)", len(sessionKey))
	}
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	return AuthConfig{
		Username:     username,
		PasswordHash: passwordHash,
		SessionKey:   sessionKey,
		SessionTTL:   ttl,
	}, nil
}
