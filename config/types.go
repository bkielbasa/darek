package config

import "time"

type Config struct {
	OpenAI   OpenAI   `yaml:"openai"`
	Postgres Postgres `yaml:"postgres"`
	OTEL     OTEL     `yaml:"otel"`
	Agent    Agent    `yaml:"agent"`
	Memory   Memory   `yaml:"memory"`
}

type OpenAI struct {
	Model      string `yaml:"model"`
	BaseURL    string `yaml:"base_url"`
	APIKeyEnv  string `yaml:"api_key_env"`
}

type Postgres struct {
	URLEnv string `yaml:"url_env"`
}

type OTEL struct {
	ServiceName      string `yaml:"service_name"`
	ExporterEndpoint string `yaml:"exporter_endpoint"`
	Insecure         bool   `yaml:"insecure"`
}

type Agent struct {
	MaxIterations int           `yaml:"max_iterations"`
	LLMTimeout    time.Duration `yaml:"llm_timeout"`
	ToolTimeout   time.Duration `yaml:"tool_timeout"`
}

type Memory struct {
	Pgvector       bool   `yaml:"pgvector"`
	EmbeddingModel string `yaml:"embedding_model"`
}
