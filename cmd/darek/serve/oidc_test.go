package serve

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v3/jwt"
	"github.com/stretchr/testify/require"
)

// fakeAuthentik is an httptest-backed OIDC provider used by every oidc_test
// in this package. The test owns the signing key and can mint ID tokens with
// arbitrary claims via signIDToken. Per-task fields are added as later tasks
// need them; T3 uses only discovery + JWKS.
type fakeAuthentik struct {
	srv           *httptest.Server
	key           *rsa.PrivateKey
	issuer        string
	lastChallenge string        // captured by the /authorize route (T4)
	nextClaims    idTokenClaims // returned by /token (T4)
}

type idTokenClaims struct {
	Iss               string   `json:"iss"`
	Sub               string   `json:"sub"`
	Aud               string   `json:"aud"`
	Exp               int64    `json:"exp"`
	Iat               int64    `json:"iat"`
	Nonce             string   `json:"nonce"`
	PreferredUsername string   `json:"preferred_username"`
	Email             string   `json:"email"`
	Groups            []string `json:"groups"`
}

func newFakeAuthentik(t *testing.T) *fakeAuthentik {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	fa := &fakeAuthentik{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                fa.issuer,
			"authorization_endpoint":                fa.issuer + "/authorize",
			"token_endpoint":                        fa.issuer + "/token",
			"jwks_uri":                              fa.issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, Use: "sig", Algorithm: "RS256", KeyID: "test"}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		clientID, clientSecret, ok := r.BasicAuth()
		require.NoError(t, r.ParseForm())
		if !ok {
			clientID = r.PostFormValue("client_id")
			clientSecret = r.PostFormValue("client_secret")
		}
		if clientID != "darek" || clientSecret != "secret" {
			http.Error(w, "bad client", http.StatusUnauthorized)
			return
		}
		verifier := r.PostFormValue("code_verifier")
		if b64sha256(verifier) != fa.lastChallenge {
			http.Error(w, "pkce mismatch", http.StatusBadRequest)
			return
		}
		idToken := fa.signIDToken(t, fa.nextClaims)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	})
	fa.srv = httptest.NewServer(mux)
	fa.issuer = fa.srv.URL
	t.Cleanup(fa.srv.Close)
	return fa
}

func (fa *fakeAuthentik) signIDToken(t *testing.T, claims idTokenClaims) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: fa.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"),
	)
	require.NoError(t, err)
	s, err := jwt.Signed(signer).Claims(claims).CompactSerialize()
	require.NoError(t, err)
	return s
}

// b64sha256 returns base64url(sha256(s)) — used to compute the PKCE
// challenge in T4's token-endpoint mock.
func b64sha256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestNewOIDC_Happy(t *testing.T) {
	fa := newFakeAuthentik(t)
	o, err := NewOIDC(context.Background(), OIDCConfig{
		Issuer:        fa.issuer,
		ClientID:      "darek",
		ClientSecret:  "secret",
		RedirectURL:   "https://darek.example/auth/callback",
		RequiredGroup: "darek-users",
	})
	require.NoError(t, err)
	require.NotNil(t, o)
	require.Equal(t, "darek", o.cfg.ClientID)
}

func TestNewOIDC_DiscoveryFails(t *testing.T) {
	_, err := NewOIDC(context.Background(), OIDCConfig{
		Issuer:        "http://127.0.0.1:1",
		ClientID:      "darek",
		ClientSecret:  "secret",
		RedirectURL:   "https://darek.example/auth/callback",
		RequiredGroup: "darek-users",
	})
	require.Error(t, err)
}

func TestHandleLoginGet_Redirects(t *testing.T) {
	fa := newFakeAuthentik(t)
	o, err := NewOIDC(context.Background(), OIDCConfig{
		Issuer:        fa.issuer,
		ClientID:      "darek",
		ClientSecret:  "secret",
		RedirectURL:   "https://darek.example/auth/callback",
		RequiredGroup: "darek-users",
	})
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/login?next=%2Fall", nil)
	w := httptest.NewRecorder()
	o.handleLoginGet(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code)
	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, fa.srv.URL+"/authorize", loc.Scheme+"://"+loc.Host+loc.Path)
	q := loc.Query()
	require.Equal(t, "darek", q.Get("client_id"))
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, "openid profile email groups", q.Get("scope"))
	require.Equal(t, "https://darek.example/auth/callback", q.Get("redirect_uri"))
	require.NotEmpty(t, q.Get("state"))
	require.NotEmpty(t, q.Get("nonce"))
	require.NotEmpty(t, q.Get("code_challenge"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))

	var seenState, seenNonce, seenPKCE bool
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case oidcStateCookie:
			seenState = true
			require.True(t, strings.HasSuffix(c.Value, "|/all"))
			require.True(t, c.HttpOnly)
			require.True(t, c.Secure)
			require.Equal(t, http.SameSiteLaxMode, c.SameSite)
			require.Equal(t, "/auth/callback", c.Path)
			require.Equal(t, int(oidcCookieTTL.Seconds()), c.MaxAge)
		case oidcNonceCookie:
			seenNonce = true
		case oidcPKCECookie:
			seenPKCE = true
		}
	}
	require.True(t, seenState && seenNonce && seenPKCE)
}

type callbackHarness struct {
	fa    *fakeAuthentik
	o     *OIDC
	state string
	nonce string
	pkceV string
}

func newCallbackHarness(t *testing.T) *callbackHarness {
	fa := newFakeAuthentik(t)
	o, err := NewOIDC(context.Background(), OIDCConfig{
		Issuer:        fa.issuer,
		ClientID:      "darek",
		ClientSecret:  "secret",
		RedirectURL:   "https://darek.example/auth/callback",
		RequiredGroup: "darek-users",
	})
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/login?next=%2Fall", nil)
	w := httptest.NewRecorder()
	o.handleLoginGet(w, req)
	loc, _ := url.Parse(w.Header().Get("Location"))
	state := loc.Query().Get("state")
	fa.lastChallenge = loc.Query().Get("code_challenge")

	h := &callbackHarness{fa: fa, o: o, state: state}
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case oidcNonceCookie:
			h.nonce = c.Value
		case oidcPKCECookie:
			h.pkceV = c.Value
		}
	}
	return h
}

func (h *callbackHarness) callback(t *testing.T, q url.Values, claims idTokenClaims) (*httptest.ResponseRecorder, string) {
	h.fa.nextClaims = claims
	req := httptest.NewRequest("GET", "/auth/callback?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: h.state + "|/all"})
	req.AddCookie(&http.Cookie{Name: oidcNonceCookie, Value: h.nonce})
	req.AddCookie(&http.Cookie{Name: oidcPKCECookie, Value: h.pkceV})
	w := httptest.NewRecorder()
	var capturedSubject string
	h.o.handleCallback(w, req, func(subject, next string) {
		capturedSubject = subject
		http.Redirect(w, req, next, http.StatusSeeOther)
	})
	return w, capturedSubject
}

func TestHandleCallback_Happy(t *testing.T) {
	h := newCallbackHarness(t)
	q := url.Values{"code": {"abc"}, "state": {h.state}}
	w, subject := h.callback(t, q, idTokenClaims{
		Iss:               h.fa.issuer,
		Sub:               "u-1",
		Aud:               "darek",
		Exp:               time.Now().Add(time.Hour).Unix(),
		Iat:               time.Now().Unix(),
		Nonce:             h.nonce,
		PreferredUsername: "bartek",
		Groups:            []string{"darek-users"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/all", w.Header().Get("Location"))
	require.Equal(t, "bartek", subject)
}

func TestHandleCallback_GroupMissing(t *testing.T) {
	h := newCallbackHarness(t)
	q := url.Values{"code": {"abc"}, "state": {h.state}}
	w, subject := h.callback(t, q, idTokenClaims{
		Iss:    h.fa.issuer,
		Sub:    "u-1",
		Aud:    "darek",
		Exp:    time.Now().Add(time.Hour).Unix(),
		Iat:    time.Now().Unix(),
		Nonce:  h.nonce,
		Groups: []string{"some-other-group"},
	})
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Contains(t, w.Header().Get("Location"), "/login?error=forbidden")
	require.Empty(t, subject)
}

func TestHandleCallback_NonceMismatch(t *testing.T) {
	h := newCallbackHarness(t)
	q := url.Values{"code": {"abc"}, "state": {h.state}}
	w, _ := h.callback(t, q, idTokenClaims{
		Iss:    h.fa.issuer,
		Sub:    "u-1",
		Aud:    "darek",
		Exp:    time.Now().Add(time.Hour).Unix(),
		Iat:    time.Now().Unix(),
		Nonce:  "wrong-nonce",
		Groups: []string{"darek-users"},
	})
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleCallback_AudMismatch(t *testing.T) {
	h := newCallbackHarness(t)
	q := url.Values{"code": {"abc"}, "state": {h.state}}
	w, _ := h.callback(t, q, idTokenClaims{
		Iss:    h.fa.issuer,
		Sub:    "u-1",
		Aud:    "not-darek",
		Exp:    time.Now().Add(time.Hour).Unix(),
		Iat:    time.Now().Unix(),
		Nonce:  h.nonce,
		Groups: []string{"darek-users"},
	})
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleCallback_StateMismatch(t *testing.T) {
	h := newCallbackHarness(t)
	q := url.Values{"code": {"abc"}, "state": {"WRONG"}}
	w, _ := h.callback(t, q, idTokenClaims{
		Iss:    h.fa.issuer,
		Sub:    "u-1",
		Aud:    "darek",
		Exp:    time.Now().Add(time.Hour).Unix(),
		Iat:    time.Now().Unix(),
		Nonce:  h.nonce,
		Groups: []string{"darek-users"},
	})
	require.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandleCallback_ProviderError(t *testing.T) {
	h := newCallbackHarness(t)
	q := url.Values{"error": {"access_denied"}, "error_description": {"user cancelled"}}
	req := httptest.NewRequest("GET", "/auth/callback?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	h.o.handleCallback(w, req, func(subject, next string) { t.Fatal("onSuccess should not be called") })
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Contains(t, w.Header().Get("Location"), "/login?error=forbidden")
}
