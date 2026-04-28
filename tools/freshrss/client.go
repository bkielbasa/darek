package freshrss

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Stream IDs in the GReader protocol.
const (
	StreamReadingList = "user/-/state/com.google/reading-list"
	StreamRead        = "user/-/state/com.google/read"
	StreamStarred     = "user/-/state/com.google/starred"
	StreamKeptUnread  = "user/-/state/com.google/kept-unread"
)

type Client struct {
	base     string // FreshRSS base URL, e.g., "https://rss.example.com"
	username string
	password string
	http     *http.Client

	mu  sync.Mutex
	sid string
}

type Options struct {
	BaseURL  string
	Username string
	Password string
	Timeout  time.Duration
}

func New(opt Options) (*Client, error) {
	if opt.BaseURL == "" || opt.Username == "" || opt.Password == "" {
		return nil, fmt.Errorf("base url, username, and password required")
	}
	if opt.Timeout == 0 {
		opt.Timeout = 30 * time.Second
	}
	return &Client{
		base:     strings.TrimRight(opt.BaseURL, "/"),
		username: opt.Username,
		password: opt.Password,
		http: &http.Client{
			Timeout:   opt.Timeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}, nil
}

// login obtains a SID via ClientLogin and caches it. Subsequent failures
// (e.g. SID expired) trigger a re-login.
func (c *Client) login(ctx context.Context) error {
	form := url.Values{}
	form.Set("Email", c.username)
	form.Set("Passwd", c.password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/api/greader.php/accounts/ClientLogin",
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("clientlogin: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("clientlogin status %d: %s", resp.StatusCode, string(body))
	}
	// Body is line-oriented "KEY=VALUE\n" — find SID=...
	for _, line := range strings.Split(string(body), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == "SID" {
			c.mu.Lock()
			c.sid = v
			c.mu.Unlock()
			return nil
		}
	}
	return fmt.Errorf("clientlogin: SID not in response: %s", string(body))
}

func (c *Client) currentSID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sid
}

// authedDo runs an HTTP request with the current SID, refreshing on 401/403.
func (c *Client) authedDo(ctx context.Context, req *http.Request) (*http.Response, error) {
	if c.currentSID() == "" {
		if err := c.login(ctx); err != nil {
			return nil, err
		}
	}
	req.Header.Set("Authorization", "GoogleLogin auth="+c.currentSID())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		_ = resp.Body.Close()
		// Retry once with a fresh SID.
		if err := c.login(ctx); err != nil {
			return nil, err
		}
		req2, _ := http.NewRequestWithContext(ctx, req.Method, req.URL.String(), nil)
		req2.Header = req.Header.Clone()
		req2.Header.Set("Authorization", "GoogleLogin auth="+c.currentSID())
		// req body cannot be replayed for POSTs without a GetBody; for GET this is fine.
		// For POSTs, callers must reconstruct the body in a higher-level retry.
		return c.http.Do(req2)
	}
	return resp, nil
}

// Article is the subset of GReader item fields we surface.
type Article struct {
	ID        string    // GReader item id, e.g. "tag:google.com,2005:reader/item/<hex>"
	Title     string
	URL       string
	Summary   string
	Published time.Time
	Feed      string
	Read      bool
	Starred   bool
}

type ListFilter int

const (
	FilterUnread ListFilter = iota
	FilterStarred
	FilterAll
)

type ListOpts struct {
	Filter  ListFilter
	FeedURL string // optional: scope to a specific feed
	Limit   int    // 1..1000; default 50
}

func (c *Client) List(ctx context.Context, opts ListOpts) ([]Article, error) {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}

	streamID := StreamReadingList
	excludeRead := false
	switch opts.Filter {
	case FilterUnread:
		excludeRead = true
	case FilterStarred:
		streamID = StreamStarred
	}
	if opts.FeedURL != "" {
		streamID = "feed/" + opts.FeedURL
	}

	q := url.Values{}
	q.Set("output", "json")
	q.Set("n", fmt.Sprint(opts.Limit))
	if excludeRead {
		q.Set("xt", StreamRead)
	}

	u := c.base + "/api/greader.php/reader/api/0/stream/contents/" + url.PathEscape(streamID) + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.authedDo(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("list status %d: %s", resp.StatusCode, string(body))
	}
	var raw struct {
		Items []struct {
			ID         string   `json:"id"`
			Title      string   `json:"title"`
			Published  int64    `json:"published"`
			Categories []string `json:"categories"`
			Canonical  []struct {
				Href string `json:"href"`
			} `json:"canonical"`
			Alternate []struct {
				Href string `json:"href"`
			} `json:"alternate"`
			Summary struct {
				Content string `json:"content"`
			} `json:"summary"`
			Origin struct {
				Title string `json:"title"`
			} `json:"origin"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]Article, 0, len(raw.Items))
	for _, it := range raw.Items {
		a := Article{
			ID:        it.ID,
			Title:     it.Title,
			Summary:   it.Summary.Content,
			Published: time.Unix(it.Published, 0),
			Feed:      it.Origin.Title,
		}
		if len(it.Canonical) > 0 {
			a.URL = it.Canonical[0].Href
		} else if len(it.Alternate) > 0 {
			a.URL = it.Alternate[0].Href
		}
		for _, c := range it.Categories {
			switch c {
			case StreamRead:
				a.Read = true
			case StreamStarred:
				a.Starred = true
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// Get returns a single article by id. The GReader stream-items-contents
// endpoint accepts a list of ids and returns the same item shape as List.
func (c *Client) Get(ctx context.Context, id string) (*Article, error) {
	form := url.Values{}
	form.Set("output", "json")
	form.Set("i", id)
	u := c.base + "/api/greader.php/reader/api/0/stream/items/contents"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.authedDo(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("get status %d: %s", resp.StatusCode, string(body))
	}
	var raw struct {
		Items []struct {
			ID         string   `json:"id"`
			Title      string   `json:"title"`
			Published  int64    `json:"published"`
			Categories []string `json:"categories"`
			Canonical  []struct {
				Href string `json:"href"`
			} `json:"canonical"`
			Alternate []struct {
				Href string `json:"href"`
			} `json:"alternate"`
			Summary struct {
				Content string `json:"content"`
			} `json:"summary"`
			Origin struct {
				Title string `json:"title"`
			} `json:"origin"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(raw.Items) == 0 {
		return nil, fmt.Errorf("article %s not found", id)
	}
	it := raw.Items[0]
	a := &Article{
		ID:        it.ID,
		Title:     it.Title,
		Summary:   it.Summary.Content,
		Published: time.Unix(it.Published, 0),
		Feed:      it.Origin.Title,
	}
	if len(it.Canonical) > 0 {
		a.URL = it.Canonical[0].Href
	} else if len(it.Alternate) > 0 {
		a.URL = it.Alternate[0].Href
	}
	for _, c := range it.Categories {
		switch c {
		case StreamRead:
			a.Read = true
		case StreamStarred:
			a.Starred = true
		}
	}
	return a, nil
}

type Action int

const (
	ActionMarkRead Action = iota
	ActionMarkUnread
	ActionStar
	ActionUnstar
)

func (c *Client) Mark(ctx context.Context, id string, act Action) error {
	form := url.Values{}
	form.Set("i", id)
	switch act {
	case ActionMarkRead:
		form.Set("a", StreamRead)
		form.Set("r", StreamKeptUnread)
	case ActionMarkUnread:
		form.Set("r", StreamRead)
		form.Set("a", StreamKeptUnread)
	case ActionStar:
		form.Set("a", StreamStarred)
	case ActionUnstar:
		form.Set("r", StreamStarred)
	default:
		return fmt.Errorf("unknown action")
	}
	// edit-tag requires a GReader token. Get one via /api/greader.php/reader/api/0/token.
	tok, err := c.editToken(ctx)
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	form.Set("T", tok)

	u := c.base + "/api/greader.php/reader/api/0/edit-tag"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.authedDo(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("edit-tag status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *Client) editToken(ctx context.Context) (string, error) {
	u := c.base + "/api/greader.php/reader/api/0/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.authedDo(ctx, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("token status %d: %s", resp.StatusCode, string(b))
	}
	return strings.TrimSpace(string(b)), nil
}
