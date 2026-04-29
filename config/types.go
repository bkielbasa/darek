package config

import "time"

type Config struct {
	OpenAI    OpenAI        `yaml:"openai"`
	Postgres  Postgres      `yaml:"postgres"`
	OTEL      OTEL          `yaml:"otel"`
	Agent     Agent         `yaml:"agent"`
	Memory    Memory        `yaml:"memory"`
	Links     Links         `yaml:"links"`
	Todoist   Todoist       `yaml:"todoist"`
	FreshRSS  FreshRSS      `yaml:"freshrss"`
	Calendars []CalendarSrc `yaml:"calendars"`
	Mail      Mail          `yaml:"mail"`
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

type Links struct {
	Pgvector bool `yaml:"pgvector"`
}

type Todoist struct {
	TokenEnv string `yaml:"token_env"`
}

type FreshRSS struct {
	BaseURL     string `yaml:"base_url"`
	Username    string `yaml:"username"`
	PasswordEnv string `yaml:"password_env"`
}

type CalendarSrc struct {
	Kind            string `yaml:"kind"`              // "google" | "ical"
	Nickname        string `yaml:"nickname"`
	URL             string `yaml:"url"`               // for ical
	CalendarID      string `yaml:"calendar_id"`       // for google, default "primary"
	ClientIDEnv     string `yaml:"client_id_env"`     // for google
	ClientSecretEnv string `yaml:"client_secret_env"` // for google
}

type Mail struct {
	AttachmentsDir    string           `yaml:"attachments_dir"`
	AttachmentTTLDays int              `yaml:"attachment_ttl_days"`
	Accounts          []MailAccountCfg `yaml:"accounts"`
}

type MailIMAP struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	TLS  bool   `yaml:"tls"`
}

type MailSMTP struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	TLS  bool   `yaml:"tls"`
}

type MailAccountCfg struct {
	Nickname    string   `yaml:"nickname"`
	Email       string   `yaml:"email"`
	IMAP        MailIMAP `yaml:"imap"`
	SMTP        MailSMTP `yaml:"smtp"`
	Username    string   `yaml:"username"`
	SecretEnv   string   `yaml:"secret_env"`
	SyncFolders []string `yaml:"sync_folders"`
}
