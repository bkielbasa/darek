package config

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// validateBlogMarketing is a no-op when no feeds are configured. When any
// feed is present, every feed's effective (root-merged) config is checked.
func validateBlogMarketing(bm *BlogMarketing) error {
	if len(bm.Feeds) == 0 {
		// Allow root-level defaults to be set even without feeds, so a user
		// can pre-stage config; nothing actually runs until feeds is non-empty.
		return validateBlogMarketingDefaults(bm)
	}
	if err := validateBlogMarketingDefaults(bm); err != nil {
		return err
	}
	seen := make(map[string]bool, len(bm.Feeds))
	for i, f := range bm.Feeds {
		if f.ID == "" {
			return fmt.Errorf("blog_marketing.feeds[%d].id required", i)
		}
		if seen[f.ID] {
			return fmt.Errorf("blog_marketing.feeds: duplicate id %q", f.ID)
		}
		seen[f.ID] = true
		if f.FeedURL == "" {
			return fmt.Errorf("blog_marketing.feeds[%s].feed_url required", f.ID)
		}
		if _, err := url.Parse(f.FeedURL); err != nil {
			return fmt.Errorf("blog_marketing.feeds[%s].feed_url: %w", f.ID, err)
		}
		// Effective fields = override or root default. Empty after merge ⇒ invalid.
		effProject := f.ProjectName
		if effProject == "" {
			effProject = bm.ProjectName
		}
		if effProject == "" {
			return fmt.Errorf("blog_marketing.feeds[%s].project_name required (or set blog_marketing.project_name as default)", f.ID)
		}
		effPostTime := f.PostTime
		if effPostTime == "" {
			effPostTime = bm.PostTime
		}
		if effPostTime == "" {
			return fmt.Errorf("blog_marketing.feeds[%s].post_time required (HH:MM, or set blog_marketing.post_time as default)", f.ID)
		}
		if _, err := time.Parse("15:04", effPostTime); err != nil {
			return fmt.Errorf("blog_marketing.feeds[%s].post_time: expected HH:MM, got %q", f.ID, effPostTime)
		}
		effTZ := f.Timezone
		if effTZ == "" {
			effTZ = bm.Timezone
		}
		if effTZ != "" {
			if _, err := time.LoadLocation(effTZ); err != nil {
				return fmt.Errorf("blog_marketing.feeds[%s].timezone: %w", f.ID, err)
			}
		}
	}
	return nil
}

// validateBlogMarketingDefaults checks root-level defaults in isolation —
// PostTime parse, Timezone load — without requiring presence (defaults are
// allowed to be empty if every feed provides its own override).
func validateBlogMarketingDefaults(bm *BlogMarketing) error {
	if bm.PostTime != "" {
		if _, err := time.Parse("15:04", bm.PostTime); err != nil {
			return fmt.Errorf("blog_marketing.post_time: expected HH:MM, got %q", bm.PostTime)
		}
	}
	if bm.Timezone != "" {
		if _, err := time.LoadLocation(bm.Timezone); err != nil {
			return fmt.Errorf("blog_marketing.timezone: %w", err)
		}
	}
	return nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(c *Config) {
	if c.Agent.MaxIterations == 0 {
		c.Agent.MaxIterations = 10
	}
	if c.Agent.LLMTimeout == 0 {
		c.Agent.LLMTimeout = 60 * time.Second
	}
	if c.Agent.ToolTimeout == 0 {
		c.Agent.ToolTimeout = 30 * time.Second
	}
	if c.OTEL.ServiceName == "" {
		c.OTEL.ServiceName = "darek"
	}
	if c.ExecutionHistory.Enabled {
		if c.ExecutionHistory.Retention == 0 {
			c.ExecutionHistory.Retention = 720 * time.Hour
		}
		if c.ExecutionHistory.CleanupPeriod == 0 {
			c.ExecutionHistory.CleanupPeriod = 24 * time.Hour
		}
	}
}

func validate(c *Config) error {
	if c.OpenAI.Model == "" {
		return fmt.Errorf("openai.model is required")
	}
	if c.OpenAI.APIKeyEnv == "" {
		return fmt.Errorf("openai.api_key_env is required")
	}
	if os.Getenv(c.OpenAI.APIKeyEnv) == "" {
		return fmt.Errorf("env var %s (openai.api_key_env) is empty", c.OpenAI.APIKeyEnv)
	}
	if c.Postgres.URLEnv == "" {
		return fmt.Errorf("postgres.url_env is required")
	}
	if os.Getenv(c.Postgres.URLEnv) == "" {
		return fmt.Errorf("env var %s (postgres.url_env) is empty", c.Postgres.URLEnv)
	}

	if err := validateBlogMarketing(&c.BlogMarketing); err != nil {
		return err
	}

	if c.ExecutionHistory.Enabled {
		if c.ExecutionHistory.Retention <= 0 {
			return fmt.Errorf("execution_history.retention must be > 0 when enabled")
		}
		if c.ExecutionHistory.CleanupPeriod <= 0 {
			return fmt.Errorf("execution_history.cleanup_period must be > 0 when enabled")
		}
	}

	if c.Auth.Issuer != "" {
		if c.Auth.ClientID == "" {
			return fmt.Errorf("auth.client_id is required when auth.issuer is set")
		}
		if c.Auth.ClientSecretEnv == "" {
			return fmt.Errorf("auth.client_secret_env is required when auth.issuer is set")
		}
		if os.Getenv(c.Auth.ClientSecretEnv) == "" {
			return fmt.Errorf("env var %s (auth.client_secret_env) is empty", c.Auth.ClientSecretEnv)
		}
		if c.Auth.RedirectURL == "" {
			return fmt.Errorf("auth.redirect_url is required when auth.issuer is set")
		}
		if c.Auth.RequiredGroup == "" {
			return fmt.Errorf("auth.required_group is required when auth.issuer is set")
		}
		if c.Auth.SessionKeyEnv == "" {
			return fmt.Errorf("auth.session_key_env is required when auth.issuer is set")
		}
		if os.Getenv(c.Auth.SessionKeyEnv) == "" {
			return fmt.Errorf("env var %s (auth.session_key_env) is empty", c.Auth.SessionKeyEnv)
		}
	}

	return nil
}
