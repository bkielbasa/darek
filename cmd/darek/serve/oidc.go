package serve

import (
	"context"
	"fmt"

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
