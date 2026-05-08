package blogfeed

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"darek/obs"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type Client struct {
	url  string
	http *http.Client
}

type Options struct {
	URL     string
	Timeout time.Duration // optional; default 30s
}

// New constructs a feed client. URL must be the absolute http(s) URL of the
// RSS/Atom document.
func New(opt Options) (*Client, error) {
	if opt.URL == "" {
		return nil, fmt.Errorf("blogfeed: url required")
	}
	if opt.Timeout == 0 {
		opt.Timeout = 30 * time.Second
	}
	return &Client{
		url: opt.URL,
		http: &http.Client{
			Timeout:   opt.Timeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}, nil
}

// List fetches the feed once and returns parsed entries (newest first).
func (c *Client) List(ctx context.Context) ([]Entry, error) {
	var entries []Entry
	err := obs.Dep(ctx, "blogfeed", "list", func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
		if err != nil {
			return fmt.Errorf("new request: %w", err)
		}
		req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.8")
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("http: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("blogfeed: status %d", resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
		entries, err = Parse(body)
		return err
	})
	return entries, err
}
