package serve

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig is resolved by runServe from cfg.Auth plus the resolved
// client-secret env var. All fields are required.
type OIDCConfig struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	RequiredGroup string
}

// OIDC wraps the provider, oauth2 config, and ID-token verifier used by
// the login and callback handlers (added in T4). Constructed once at
// startup; safe for concurrent use.
type OIDC struct {
	cfg      OIDCConfig
	provider *oidc.Provider
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewOIDC performs OIDC discovery against cfg.Issuer. Discovery failure is
// fatal — the caller (runServe) returns the error so the process exits and
// Kubernetes' pod-restart backoff handles transient outages.
func NewOIDC(ctx context.Context, cfg OIDCConfig) (*OIDC, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("oidc: issuer is required")
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("oidc: client_id is required")
	}
	if cfg.ClientSecret == "" {
		return nil, fmt.Errorf("oidc: client_secret is required")
	}
	if cfg.RedirectURL == "" {
		return nil, fmt.Errorf("oidc: redirect_url is required")
	}
	if cfg.RequiredGroup == "" {
		return nil, fmt.Errorf("oidc: required_group is required")
	}

	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	return &OIDC{
		cfg:      cfg,
		provider: provider,
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email", "groups"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

const (
	oidcStateCookie = "oidc_state"
	oidcNonceCookie = "oidc_nonce"
	oidcPKCECookie  = "oidc_pkce"
	oidcCookieTTL   = 5 * time.Minute
)

func randB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func setShortCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/auth/callback",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(oidcCookieTTL.Seconds()),
	})
}

func clearShortCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/auth/callback",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (o *OIDC) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	next := sanitizeNext(r.URL.Query().Get("next"))
	state, err := randB64(32)
	if err != nil {
		http.Error(w, "rng failure", http.StatusInternalServerError)
		return
	}
	nonce, err := randB64(32)
	if err != nil {
		http.Error(w, "rng failure", http.StatusInternalServerError)
		return
	}
	pkceVerifier, err := randB64(32)
	if err != nil {
		http.Error(w, "rng failure", http.StatusInternalServerError)
		return
	}
	setShortCookie(w, oidcStateCookie, state+"|"+next)
	setShortCookie(w, oidcNonceCookie, nonce)
	setShortCookie(w, oidcPKCECookie, pkceVerifier)

	authURL := o.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", pkceChallenge(pkceVerifier)),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func (o *OIDC) handleCallback(w http.ResponseWriter, r *http.Request, onSuccess func(subject, next string)) {
	if e := r.URL.Query().Get("error"); e != "" {
		slog.Warn("oidc callback: provider returned error",
			"error", e,
			"description", r.URL.Query().Get("error_description"),
			"remote_addr", r.RemoteAddr)
		http.Redirect(w, r, "/login?error=forbidden", http.StatusSeeOther)
		return
	}

	stateCookie, err := r.Cookie(oidcStateCookie)
	if err != nil {
		http.Error(w, "missing state cookie", http.StatusBadRequest)
		return
	}
	nonceCookie, err := r.Cookie(oidcNonceCookie)
	if err != nil {
		http.Error(w, "missing nonce cookie", http.StatusBadRequest)
		return
	}
	pkceCookie, err := r.Cookie(oidcPKCECookie)
	if err != nil {
		http.Error(w, "missing pkce cookie", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(stateCookie.Value, "|", 2)
	if len(parts) != 2 {
		http.Error(w, "bad state cookie", http.StatusBadRequest)
		return
	}
	wantState, next := parts[0], sanitizeNext(parts[1])
	gotState := r.URL.Query().Get("state")
	if subtle.ConstantTimeCompare([]byte(wantState), []byte(gotState)) != 1 {
		slog.Warn("oidc callback: state mismatch", "remote_addr", r.RemoteAddr)
		http.Error(w, "state mismatch", http.StatusForbidden)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	tok, err := o.oauth.Exchange(r.Context(), code,
		oauth2.SetAuthURLParam("code_verifier", pkceCookie.Value),
	)
	if err != nil {
		slog.Warn("oidc callback: code exchange failed", "err", err, "remote_addr", r.RemoteAddr)
		http.Error(w, "code exchange failed", http.StatusUnauthorized)
		return
	}
	idTokenRaw, ok := tok.Extra("id_token").(string)
	if !ok || idTokenRaw == "" {
		http.Error(w, "no id_token", http.StatusUnauthorized)
		return
	}
	idt, err := o.verifier.Verify(r.Context(), idTokenRaw)
	if err != nil {
		slog.Warn("oidc callback: id token invalid", "err", err, "remote_addr", r.RemoteAddr)
		http.Error(w, "id token invalid", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonceCookie.Value)) != 1 {
		slog.Warn("oidc callback: nonce mismatch", "remote_addr", r.RemoteAddr)
		http.Error(w, "nonce mismatch", http.StatusUnauthorized)
		return
	}

	var claims struct {
		Sub               string   `json:"sub"`
		PreferredUsername string   `json:"preferred_username"`
		Email             string   `json:"email"`
		Groups            []string `json:"groups"`
	}
	if err := idt.Claims(&claims); err != nil {
		http.Error(w, "claims decode", http.StatusInternalServerError)
		return
	}
	if !slices.Contains(claims.Groups, o.cfg.RequiredGroup) {
		slog.Warn("oidc callback: required group missing",
			"required", o.cfg.RequiredGroup,
			"got_groups", claims.Groups,
			"sub", claims.Sub,
			"remote_addr", r.RemoteAddr)
		http.Redirect(w, r, "/login?error=forbidden", http.StatusSeeOther)
		return
	}
	subject := claims.PreferredUsername
	if subject == "" {
		subject = claims.Sub
	}

	clearShortCookie(w, oidcStateCookie)
	clearShortCookie(w, oidcNonceCookie)
	clearShortCookie(w, oidcPKCECookie)

	onSuccess(subject, next)
}
