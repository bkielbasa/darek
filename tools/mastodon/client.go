// Package mastodon is a minimal Mastodon API v1 client. Today it only knows
// how to post a status (toot) — that's all the auto-poster needs.
package mastodon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"darek/obs"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Client posts to one Mastodon instance with one user's access token.
type Client struct {
	instance string
	token    string
	http     *http.Client
}

type Options struct {
	// Instance is the base URL of the Mastodon server (e.g.
	// "https://fosstodon.org"). Required.
	Instance string
	// Token is the OAuth2 access token with at least `write:statuses` scope.
	// Required.
	Token string
	// Timeout caps the HTTP round-trip; defaults to 30s.
	Timeout time.Duration
}

func New(opt Options) (*Client, error) {
	if opt.Instance == "" {
		return nil, fmt.Errorf("mastodon: instance required")
	}
	if opt.Token == "" {
		return nil, fmt.Errorf("mastodon: token required")
	}
	if opt.Timeout == 0 {
		opt.Timeout = 30 * time.Second
	}
	return &Client{
		instance: strings.TrimRight(opt.Instance, "/"),
		token:    opt.Token,
		http: &http.Client{
			Timeout:   opt.Timeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}, nil
}

// Status is the subset of Mastodon's Status object we care about.
type Status struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// Account is the subset of Mastodon's CredentialAccount we surface — used by
// VerifyCredentials so callers can confirm the token is valid AND that it
// authenticates to the expected handle (catches accidentally crossed wires
// where the token belongs to a different account than the configured handle).
type Account struct {
	ID         string `json:"id"`
	Username   string `json:"username"`     // local handle without instance, e.g. "bk"
	Acct       string `json:"acct"`         // local handle, often equal to username
	DisplayName string `json:"display_name"`
}

// VerifyCredentials returns the authenticated account, confirming the token
// is valid and identifying which Mastodon user it belongs to. Cheap; used by
// darek doctor to surface bad/expired tokens before the auto-poster trips
// on them at midnight.
func (c *Client) VerifyCredentials(ctx context.Context) (*Account, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.instance+"/api/v1/accounts/verify_credentials", nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	var out Account
	err = obs.Dep(ctx, "mastodon", "verify_credentials", func(ctx context.Context) error {
		resp, err := c.http.Do(req.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("http: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("mastodon verify_credentials: status %d: %s", resp.StatusCode, string(b))
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Toot posts a status to the configured account. idempotencyKey, if non-empty,
// is sent as the Idempotency-Key header — Mastodon servers use it to dedup
// retries within their idempotency window (typically a few minutes), returning
// the same Status object instead of creating a duplicate post. Callers should
// pass a stable per-task key so a darek crash between publish and DB write
// doesn't double-post on the next tick.
func (c *Client) Toot(ctx context.Context, text, idempotencyKey string) (*Status, error) {
	body, err := json.Marshal(struct {
		Status string `json:"status"`
	}{Status: text})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.instance+"/api/v1/statuses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	var out Status
	err = obs.Dep(ctx, "mastodon", "toot", func(ctx context.Context) error {
		resp, err := c.http.Do(req.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("http: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("mastodon toot: status %d: %s", resp.StatusCode, string(b))
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
