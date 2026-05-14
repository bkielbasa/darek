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
	"testing"

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
