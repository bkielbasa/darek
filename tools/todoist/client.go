package todoist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const defaultBaseURL = "https://api.todoist.com/api/v1"

type Client struct {
	base  string
	token string
	http  *http.Client
}

type Options struct {
	Token   string
	BaseURL string        // optional; defaults to https://api.todoist.com/api/v1
	Timeout time.Duration // optional; defaults to 30s
}

func New(opt Options) (*Client, error) {
	if opt.Token == "" {
		return nil, fmt.Errorf("todoist token required")
	}
	if opt.BaseURL == "" {
		opt.BaseURL = defaultBaseURL
	}
	if opt.Timeout == 0 {
		opt.Timeout = 30 * time.Second
	}
	return &Client{
		base:  opt.BaseURL,
		token: opt.Token,
		http:  &http.Client{Timeout: opt.Timeout},
	}, nil
}

type Due struct {
	Date        string `json:"date,omitempty"`
	String      string `json:"string,omitempty"`
	IsRecurring bool   `json:"is_recurring,omitempty"`
}

type Task struct {
	ID          string   `json:"id"`
	Content     string   `json:"content"`
	Description string   `json:"description,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	Due         *Due     `json:"due,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	ProjectID   string   `json:"project_id,omitempty"`
	IsCompleted bool     `json:"is_completed,omitempty"`
	URL         string   `json:"url,omitempty"`
}

type ListFilter struct {
	Filter    string
	ProjectID string
	Label     string
}

// listEnvelope is the v1 paginated response wrapper.
type listEnvelope struct {
	Results    []Task `json:"results"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (c *Client) ListTasks(ctx context.Context, f ListFilter) ([]Task, error) {
	q := url.Values{}
	path := "/tasks"
	if f.Filter != "" {
		// v1 dedicated filter endpoint takes the Todoist filter expression as `query`.
		path = "/tasks/filter"
		q.Set("query", f.Filter)
	}
	if f.ProjectID != "" {
		q.Set("project_id", f.ProjectID)
	}
	if f.Label != "" {
		q.Set("label", f.Label)
	}
	var env listEnvelope
	if err := c.doJSON(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &env); err != nil {
		return nil, err
	}
	return env.Results, nil
}

type CreateRequest struct {
	Content     string   `json:"content"`
	Description string   `json:"description,omitempty"`
	ProjectID   string   `json:"project_id,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	DueString   string   `json:"due_string,omitempty"`
	DueDate     string   `json:"due_date,omitempty"`
	Labels      []string `json:"labels,omitempty"`
}

func (c *Client) CreateTask(ctx context.Context, req CreateRequest) (*Task, error) {
	var out Task
	if err := c.doJSON(ctx, http.MethodPost, "/tasks", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CompleteTask(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/tasks/"+id+"/close", nil, nil)
}

type UpdateRequest struct {
	Content     *string  `json:"content,omitempty"`
	Description *string  `json:"description,omitempty"`
	Priority    *int     `json:"priority,omitempty"`
	DueString   *string  `json:"due_string,omitempty"`
	Labels      []string `json:"labels,omitempty"`
}

func (c *Client) UpdateTask(ctx context.Context, id string, req UpdateRequest) (*Task, error) {
	var out Task
	if err := c.doJSON(ctx, http.MethodPost, "/tasks/"+id, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("todoist %s %s: status %d: %s", method, path, resp.StatusCode, string(b))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}
