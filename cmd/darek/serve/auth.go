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
)

const sessionCookieName = "darek_session"

type AuthConfig struct {
	SessionKey []byte
	SessionTTL time.Duration
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
	return user, true
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
			if sub, ok := s.auth.verifyCookie(c.Value); ok {
				s.setSessionCookie(w, sub) // rolling — re-signs with the same subject
				next.ServeHTTP(w, r)
				return
			}
		}
		nextURL := url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, "/login?next="+nextURL, http.StatusSeeOther)
	})
}

func isPublicPath(p string) bool {
	return p == "/healthz" || p == "/login" || p == "/logout" || p == "/auth/callback" ||
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

func (s *Server) setSessionCookie(w http.ResponseWriter, subject string) {
	exp := time.Now().Add(s.auth.SessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.auth.signSession(subject, exp),
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

func NewAuthConfig(sessionKey []byte, ttl time.Duration) (AuthConfig, error) {
	if len(sessionKey) < 32 {
		return AuthConfig{}, fmt.Errorf("auth: session key must be at least 32 bytes (got %d)", len(sessionKey))
	}
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	return AuthConfig{SessionKey: sessionKey, SessionTTL: ttl}, nil
}
