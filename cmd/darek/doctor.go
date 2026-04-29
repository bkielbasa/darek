package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"darek/config"
	"darek/db"
	"darek/llm"
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
