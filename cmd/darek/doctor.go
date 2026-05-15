package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"darek/config"
	"darek/db"
	"darek/llm"
	"darek/tools/blogfeed"
	"darek/tools/mastodon"
	"darek/tools/todoist"
)

func runMigrate(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool.Inner()); err != nil {
		return err
	}
	fmt.Println("migrations applied")
	return nil
}

type check struct {
	name string
	ok   bool
	msg  string
}

func runDoctor(ctx context.Context, cfgPath string) error {
	results := []check{}
	add := func(name string, err error, okMsg string) {
		if err != nil {
			results = append(results, check{name: name, ok: false, msg: err.Error()})
		} else {
			results = append(results, check{name: name, ok: true, msg: okMsg})
		}
	}

	cfg, err := config.Load(cfgPath)
	add("config", err, fmt.Sprintf("loaded %s", cfgPath))
	if err == nil {
		dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
		add("postgres dsn", err, "resolved")
		if err == nil {
			pool, err := db.Open(ctx, dsn)
			add("postgres connect", err, "connected")
			if err == nil {
				_, err = pool.Exec(ctx, "SELECT 1")
				add("postgres query", err, "ok")
				if cfg.ExecutionHistory.Enabled {
					var n int
					perr := pool.QueryRow(ctx, `SELECT COUNT(*) FROM executions WHERE started_at > now() - interval '1 day'`).Scan(&n)
					add("executions_24h", perr, fmt.Sprintf("count=%d", n))
				}
				pool.Close()
			}
		}
		key, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
		add("openai key", err, "present")
		if err == nil {
			cl, err := llm.New(llm.Options{APIKey: key, BaseURL: cfg.OpenAI.BaseURL, Model: cfg.OpenAI.Model, Timeout: 5 * time.Second})
			add("openai client construct", err, fmt.Sprintf("model=%s known=%v", cfg.OpenAI.Model, llm.KnownModel(cfg.OpenAI.Model)))
			_ = cl
		}
		conn, err := net.DialTimeout("tcp", cfg.OTEL.ExporterEndpoint, 2*time.Second)
		add("otel exporter reachable", err, cfg.OTEL.ExporterEndpoint)
		if err == nil {
			conn.Close()
		}
		if cfg.Auth.Issuer != "" {
			client := &http.Client{Timeout: 5 * time.Second}
			urlStr := strings.TrimRight(cfg.Auth.Issuer, "/") + "/.well-known/openid-configuration"
			resp, dErr := client.Get(urlStr)
			if dErr == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					add("oidc discovery", nil, "ok")
				} else {
					add("oidc discovery", fmt.Errorf("status %d", resp.StatusCode), "")
				}
			} else {
				add("oidc discovery", dErr, "")
			}
		}
		if len(cfg.BlogMarketing.Feeds) > 0 {
			var td *todoist.Client
			if cfg.Todoist.TokenEnv != "" {
				if tok, terr := config.ResolveSecret("env:" + cfg.Todoist.TokenEnv); terr == nil && tok != "" {
					td, _ = todoist.New(todoist.Options{Token: tok, Timeout: 5 * time.Second})
				}
			}
			for _, f := range cfg.BlogMarketing.Feeds {
				feed, err := blogfeed.New(blogfeed.Options{URL: f.FeedURL, Timeout: 5 * time.Second})
				add(fmt.Sprintf("blog %s feed client", f.ID), err, f.FeedURL)
				if err == nil {
					_, lErr := feed.List(ctx)
					add(fmt.Sprintf("blog %s feed reachable", f.ID), lErr, "ok")
				}
				if td != nil {
					project := f.ProjectName
					if project == "" {
						project = cfg.BlogMarketing.ProjectName
					}
					_, rerr := td.ResolveProjectID(ctx, project)
					add(fmt.Sprintf("blog %s project resolvable", f.ID), rerr, fmt.Sprintf("project=%s", project))
				}
				// Mastodon auto-poster credential probe (only when configured).
				if mc, ok := f.Accounts["mastodon"]; ok && mc.TokenEnv != "" {
					tok, terr := config.ResolveSecret("env:" + mc.TokenEnv)
					if terr != nil || tok == "" {
						add(fmt.Sprintf("blog %s mastodon token", f.ID),
							fmt.Errorf("env %s unset or empty", mc.TokenEnv), "")
					} else if mc.Instance == "" {
						add(fmt.Sprintf("blog %s mastodon instance", f.ID),
							fmt.Errorf("instance missing while token_env is set"), "")
					} else {
						mast, merr := mastodon.New(mastodon.Options{Instance: mc.Instance, Token: tok, Timeout: 5 * time.Second})
						if merr != nil {
							add(fmt.Sprintf("blog %s mastodon client", f.ID), merr, "")
						} else {
							acct, vErr := mast.VerifyCredentials(ctx)
							if vErr != nil {
								add(fmt.Sprintf("blog %s mastodon credentials", f.ID), vErr, "")
							} else {
								add(fmt.Sprintf("blog %s mastodon credentials", f.ID), nil,
									fmt.Sprintf("authenticated as @%s on %s", acct.Username, mc.Instance))
							}
						}
					}
				}
			}
		}
	}

	hasFail := false
	for _, c := range results {
		mark := "OK "
		if !c.ok {
			mark = "FAIL"
			hasFail = true
		}
		fmt.Printf("[%s] %-30s %s\n", mark, c.name, strings.TrimSpace(c.msg))
	}
	if hasFail {
		return fmt.Errorf("doctor: one or more checks failed")
	}
	return nil
}
