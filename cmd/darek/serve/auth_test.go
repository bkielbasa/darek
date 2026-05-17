package serve

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	testKey  = []byte("0123456789abcdef0123456789abcdef")
	otherKey = []byte("ffffffffffffffffffffffffffffffff")
)

func newTestAuth(ttl time.Duration) AuthConfig {
	return AuthConfig{
		SessionKey: testKey,
		SessionTTL: ttl,
	}
}

func TestSignVerify_Roundtrip(t *testing.T) {
	a := newTestAuth(time.Hour)
	tok := a.signSession("alice", time.Now().Add(time.Hour))
	got, ok := a.verifyCookie(tok)
	require.True(t, ok)
	require.Equal(t, "alice", got)

	tokB := a.signSession("bob", time.Now().Add(time.Hour))
	got, ok = a.verifyCookie(tokB)
	require.True(t, ok)
	require.Equal(t, "bob", got)
}

func TestVerify_TamperedSig(t *testing.T) {
	a := newTestAuth(time.Hour)
	tok := a.signSession("alice", time.Now().Add(time.Hour))
	// flip a char in the middle of the token to ensure the decoded bytes change.
	// The last char may be padding-equivalent, so tampering there is unreliable.
	mid := len(tok) / 2
	bad := tok[:mid] + flipChar(tok[mid]) + tok[mid+1:]
	_, ok := a.verifyCookie(bad)
	require.False(t, ok)
}

func TestVerify_Expired(t *testing.T) {
	a := newTestAuth(time.Hour)
	tok := a.signSession("alice", time.Now().Add(-time.Second))
	_, ok := a.verifyCookie(tok)
	require.False(t, ok)
}

func TestVerify_WrongKey(t *testing.T) {
	signer := newTestAuth(time.Hour)
	verifier := signer
	verifier.SessionKey = otherKey
	tok := signer.signSession("alice", time.Now().Add(time.Hour))
	_, ok := verifier.verifyCookie(tok)
	require.False(t, ok)
}

func TestVerify_Garbage(t *testing.T) {
	a := newTestAuth(time.Hour)
	for _, junk := range []string{"", "notbase64$$$", "YWJj", strings.Repeat("a", 200)} {
		_, ok := a.verifyCookie(junk)
		require.False(t, ok, "junk = %q", junk)
	}
}

// flipChar returns a different rune of the same general class so the result
// stays a valid base64-url char (we want format-valid but signature-invalid).
func flipChar(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

func newAuthedServer(t *testing.T) *Server {
	t.Helper()
	a := AuthConfig{SessionKey: testKey, SessionTTL: time.Hour}
	bundle, err := parseTemplateBundle()
	require.NoError(t, err)
	s := &Server{
		mux:       http.NewServeMux(),
		auth:      a,
		loginTmpl: bundle.loginTmpl,
	}
	s.mux.HandleFunc("GET /private", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "private-ok")
	})
	return s
}

func TestIsPublicPath(t *testing.T) {
	for _, p := range []string{"/healthz", "/metrics", "/login", "/logout", "/auth/callback", "/static/style.css", "/static/img/x.png"} {
		require.True(t, isPublicPath(p), p)
	}
	for _, p := range []string{"/", "/all", "/sync", "/links/abc/rating"} {
		require.False(t, isPublicPath(p), p)
	}
}

func TestSanitizeNext(t *testing.T) {
	require.Equal(t, "/", sanitizeNext(""))
	require.Equal(t, "/", sanitizeNext("//evil.com"))
	require.Equal(t, "/", sanitizeNext("https://evil.com"))
	require.Equal(t, "/", sanitizeNext("javascript:alert(1)"))
	require.Equal(t, "/all", sanitizeNext("/all"))
	require.Equal(t, "/links/abc?x=1", sanitizeNext("/links/abc?x=1"))
}

func TestRequireAuth_BypassesPublic(t *testing.T) {
	s := newAuthedServer(t)
	for _, p := range []string{"/healthz", "/login"} {
		req := httptest.NewRequest("GET", p, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		require.NotEqual(t, http.StatusSeeOther, w.Code, "path %s", p)
	}
}

func TestRequireAuth_RedirectsAnonymousToLogin(t *testing.T) {
	s := newAuthedServer(t)
	req := httptest.NewRequest("GET", "/private?x=1", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code)
	loc := w.Header().Get("Location")
	require.Contains(t, loc, "/login?next=")
	require.Contains(t, loc, url.QueryEscape("/private?x=1"))
}

func TestRequireAuth_PassesValidCookie(t *testing.T) {
	s := newAuthedServer(t)
	tok := s.auth.signSession("alice", time.Now().Add(time.Hour))
	req := httptest.NewRequest("GET", "/private", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tok})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "private-ok", w.Body.String())
	// Rolling expiry: a refreshed cookie was set on the response
	require.NotEmpty(t, w.Result().Cookies(), "expected refreshed cookie")
}

func TestLoginTemplate_RendersErrorBanner(t *testing.T) {
	s := newAuthedServer(t)
	s.mux.HandleFunc("GET /login", s.handleOIDCLogin)
	req := httptest.NewRequest("GET", "/login?error=forbidden", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "Sign in with Authentik")
	require.Contains(t, w.Body.String(), "darek-users")
}
