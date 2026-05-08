package config

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

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

	bm := c.BlogMarketing
	if bm.FeedURL != "" || bm.ProjectName != "" || bm.PostTime != "" || bm.Timezone != "" || bm.SyncInterval != 0 {
		if bm.FeedURL == "" {
			return fmt.Errorf("blog_marketing.feed_url required")
		}
		if _, err := url.Parse(bm.FeedURL); err != nil {
			return fmt.Errorf("blog_marketing.feed_url: %w", err)
		}
		if bm.ProjectName == "" {
			return fmt.Errorf("blog_marketing.project_name required")
		}
		if bm.PostTime == "" {
			return fmt.Errorf("blog_marketing.post_time required (HH:MM)")
		}
		if _, err := time.Parse("15:04", bm.PostTime); err != nil {
			return fmt.Errorf("blog_marketing.post_time: expected HH:MM, got %q", bm.PostTime)
		}
		if bm.Timezone != "" {
			if _, err := time.LoadLocation(bm.Timezone); err != nil {
				return fmt.Errorf("blog_marketing.timezone: %w", err)
			}
		}
	}

	return nil
}
