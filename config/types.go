package config

import (
	"fmt"
	"time"
)

type Config struct {
	OpenAI    OpenAI        `yaml:"openai"`
	Postgres  Postgres      `yaml:"postgres"`
	OTEL      OTEL          `yaml:"otel"`
	Agent     Agent         `yaml:"agent"`
	Memory    Memory        `yaml:"memory"`
	Links     Links         `yaml:"links"`
	Analyze   Analyze       `yaml:"analyze"`
	Recommend Recommend     `yaml:"recommend"`
	Todoist   Todoist       `yaml:"todoist"`
	FreshRSS  FreshRSS      `yaml:"freshrss"`
	Calendars []CalendarSrc `yaml:"calendars"`
	Mail      Mail          `yaml:"mail"`
	Server    Server        `yaml:"server"`
	Auth      Auth          `yaml:"auth"`
	WhatsApp  WhatsApp      `yaml:"whatsapp"`

	CalendarDigest   CalendarDigest   `yaml:"calendar_digest"`
	BlogMarketing    BlogMarketing    `yaml:"blog_marketing"`
	ExecutionHistory ExecutionHistory `yaml:"execution_history"`
}

type CalendarDigest struct {
	To          string `yaml:"to"`
	FromAccount string `yaml:"from_account"`
	Subject     string `yaml:"subject"` // optional; default "Calendar — <YYYY-MM-DD>"
	// RateBaseURL, when set, is the public base URL of `darek serve` (e.g.
	// "https://darek.example.com"). Reading suggestions then get clickable
	// 1-5 stars that rate a link via GET /r/{id}/{value}. Empty → no stars.
	RateBaseURL string `yaml:"rate_base_url"`
}

type OpenAI struct {
	Model     string `yaml:"model"`
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

type Postgres struct {
	URLEnv string `yaml:"url_env"`
}

type OTEL struct {
	ServiceName      string `yaml:"service_name"`
	ExporterEndpoint string `yaml:"exporter_endpoint"`
	Insecure         bool   `yaml:"insecure"`
	JaegerUIURL      string `yaml:"jaeger_ui_url"`
}

type Agent struct {
	MaxIterations int           `yaml:"max_iterations"`
	LLMTimeout    time.Duration `yaml:"llm_timeout"`
	ToolTimeout   time.Duration `yaml:"tool_timeout"`
}

type Memory struct {
	Pgvector       bool   `yaml:"pgvector"`
	EmbeddingModel string `yaml:"embedding_model"`
	Enabled        bool   `yaml:"enabled"` // enables digest memory extraction + recall
}

type Links struct {
	Pgvector bool `yaml:"pgvector"`
}

// Analyze configures automatic summary+tag generation for newly-ingested links.
type Analyze struct {
	Enabled       bool     `yaml:"enabled"`
	Kinds         []string `yaml:"kinds"`           // default: article, video
	TagVocabLimit int      `yaml:"tag_vocab_limit"` // existing tags fed to the analyzer; default 200
}

// Recommend configures the daily reading recommender.
type Recommend struct {
	Enabled        bool   `yaml:"enabled"`
	Count          int    `yaml:"count"`          // suggestions per run; default 3
	CandidatePool  int    `yaml:"candidate_pool"` // candidates retrieved per run; default 30
	LookbackDays   int    `yaml:"lookback_days"`  // 0 = any age
	CooldownDays   int    `yaml:"cooldown_days"`  // 0 = never repeat
	MinLiked       int    `yaml:"min_liked"`      // liked examples needed before using embeddings; default 3
	EmbeddingModel string `yaml:"embedding_model"`
	EmbedBatch     int    `yaml:"embed_batch"` // default 64
	EmbedLimit     int    `yaml:"embed_limit"` // per Build run; default 256
}

// WithDefaults returns a copy with zero fields filled in.
func (r Recommend) WithDefaults() Recommend {
	if r.Count <= 0 {
		r.Count = 3
	}
	if r.CandidatePool <= 0 {
		r.CandidatePool = 30
	}
	if r.MinLiked <= 0 {
		r.MinLiked = 3
	}
	if r.EmbeddingModel == "" {
		r.EmbeddingModel = "text-embedding-3-small"
	}
	if r.EmbedBatch <= 0 {
		r.EmbedBatch = 64
	}
	if r.EmbedLimit <= 0 {
		r.EmbedLimit = 256
	}
	return r
}

type Todoist struct {
	TokenEnv     string        `yaml:"token_env"`
	SyncInterval time.Duration `yaml:"sync_interval"`
}

type FreshRSS struct {
	BaseURL      string        `yaml:"base_url"`
	Username     string        `yaml:"username"`
	PasswordEnv  string        `yaml:"password_env"`
	SyncInterval time.Duration `yaml:"sync_interval"`
}

// BlogMarketing is multi-blog. Root-level fields are defaults that each
// feed entry can override per blog. Adding a new feed entry to config later
// triggers a per-blog backfill (no tasks for posts that pre-date the first
// poll for that blog).
type BlogMarketing struct {
	SyncInterval       time.Duration `yaml:"sync_interval"`
	PublishInterval    time.Duration `yaml:"publish_interval"`    // auto-poster cadence; 0 ⇒ default 1h
	RegenerateInterval time.Duration `yaml:"regenerate_interval"` // re-roll scanner cadence; 0 ⇒ default 5m
	ProjectName        string        `yaml:"project_name"`        // default for all feeds
	PostTime           string        `yaml:"post_time"`           // default for all feeds, "HH:MM"
	Timezone           string        `yaml:"timezone"`            // default for all feeds; optional
	Feeds              []BlogFeed    `yaml:"feeds"`
}

// BlogFeed is one blog's worth of config. ID is the stable identifier persisted
// in blog_posts_scheduled.blog_id. Accounts maps platform name ("x", "mastodon",
// "linkedin") to per-platform account credentials and metadata.
// ProjectName/PostTime/Timezone, if empty, fall back to the BlogMarketing root.
type BlogFeed struct {
	ID          string                   `yaml:"id"`
	FeedURL     string                   `yaml:"feed_url"`
	Accounts    map[string]AccountConfig `yaml:"accounts"`
	ProjectName string                   `yaml:"project_name"` // optional override
	PostTime    string                   `yaml:"post_time"`    // optional override
	Timezone    string                   `yaml:"timezone"`     // optional override
}

// AccountConfig is one social-media account for a blog. Handle is the user-
// visible identifier the LLM is told to weave into copy (e.g. "@bk@fosstodon.org").
// Instance + TokenEnv are needed by the auto-poster; an entry with Handle
// only is still valid for drafting but can't publish until secrets are set.
type AccountConfig struct {
	// Handle is the public display identifier (e.g. "@bk@fosstodon.org",
	// "@bk_tech", "bartlomiej-klimczak"). Required.
	Handle string `yaml:"handle"`

	// Instance is the platform base URL the poster targets. Only meaningful
	// for Mastodon (e.g. "https://fosstodon.org"). Other platforms ignore it.
	Instance string `yaml:"instance"`

	// TokenEnv is the environment variable name from which the auto-poster
	// reads the API token / access token for this account. Empty disables
	// posting for this (blog, platform) — drafting still works.
	TokenEnv string `yaml:"token_env"`
}

type Server struct {
	Bind string `yaml:"bind"` // e.g. 127.0.0.1:7777
}

type Auth struct {
	Issuer          string        `yaml:"issuer"`
	ClientID        string        `yaml:"client_id"`
	ClientSecretEnv string        `yaml:"client_secret_env"`
	RedirectURL     string        `yaml:"redirect_url"`
	RequiredGroup   string        `yaml:"required_group"`
	SessionKeyEnv   string        `yaml:"session_key_env"`
	SessionTTL      time.Duration `yaml:"session_ttl"`
}

type WhatsApp struct {
	Enabled   bool   `yaml:"enabled"`
	StorePath string `yaml:"store_path"` // sqlite path for whatsmeow session; defaults to ~/.darek/whatsapp/store.db

	// PostgresURLEnv, when set, selects a Postgres-backed whatsmeow session
	// store instead of SQLite. The value is the name of the environment
	// variable holding a pgx-compatible DSN (e.g. DAREK_POSTGRES_URL).
	// Prefer Postgres when the SQLite store would live on a network
	// filesystem, where SQLite locking is unreliable.
	PostgresURLEnv string `yaml:"postgres_url_env"`
}

type CalendarSrc struct {
	Kind            string `yaml:"kind"` // "google" | "ical"
	Nickname        string `yaml:"nickname"`
	URL             string `yaml:"url"`               // for ical (literal URL; mutually exclusive with url_env)
	URLEnv          string `yaml:"url_env"`           // for ical (env var holding URL; mutually exclusive with url)
	CalendarID      string `yaml:"calendar_id"`       // for google, default "primary"
	ClientIDEnv     string `yaml:"client_id_env"`     // for google
	ClientSecretEnv string `yaml:"client_secret_env"` // for google
}

// ICalURL returns the iCal source URL for this calendar entry. Prefers the
// literal URL if set; otherwise resolves URLEnv. Errors if both are set or
// if neither is set.
func (c CalendarSrc) ICalURL() (string, error) {
	switch {
	case c.URL != "" && c.URLEnv != "":
		return "", fmt.Errorf("calendar %q: set only one of url or url_env", c.Nickname)
	case c.URL != "":
		return c.URL, nil
	case c.URLEnv != "":
		return ResolveSecret("env:" + c.URLEnv)
	default:
		return "", fmt.Errorf("calendar %q: url or url_env is required for kind ical", c.Nickname)
	}
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

type ExecutionHistory struct {
	Enabled       bool          `yaml:"enabled"`
	Retention     time.Duration `yaml:"retention"`
	CleanupPeriod time.Duration `yaml:"cleanup_period"`
}
