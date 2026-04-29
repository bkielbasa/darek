package config

import (
	"fmt"
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
	return nil
}
