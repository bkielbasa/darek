package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"

	"darek/agent"
	"darek/config"
	"darek/db"
	"darek/llm"
	"darek/memory"
	"darek/obs"
	"darek/tools"
)

func runChat(ctx context.Context, cfgPath, userInput string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	apiKey, err := config.ResolveSecret("env:" + cfg.OpenAI.APIKeyEnv)
	if err != nil {
		return fmt.Errorf("openai key: %w", err)
	}
	dsn, err := config.ResolveSecret("env:" + cfg.Postgres.URLEnv)
	if err != nil {
		return fmt.Errorf("postgres dsn: %w", err)
	}

	_, otelShutdown, err := obs.Init(ctx, obs.Options{
		ServiceName: cfg.OTEL.ServiceName,
		Endpoint:    cfg.OTEL.ExporterEndpoint,
		Insecure:    cfg.OTEL.Insecure,
	})
	if err != nil {
		return fmt.Errorf("otel init: %w", err)
	}
	defer func() { _ = otelShutdown(context.Background()) }()
	logger := obs.NewLogger(cfg.OTEL.ServiceName)

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer pool.Close()

	llmClient, err := llm.New(llm.Options{
		APIKey:  apiKey,
		BaseURL: cfg.OpenAI.BaseURL,
		Model:   cfg.OpenAI.Model,
		Timeout: cfg.Agent.LLMTimeout,
	})
	if err != nil {
		return err
	}

	reg, err := tools.NewRegistry(cfg.Agent.ToolTimeout)
	if err != nil {
		return err
	}
	store := memory.NewStore(pool)
	if err := reg.Register(memory.RecallTool{Store: store}); err != nil {
		return err
	}
	if err := reg.Register(memory.SaveTool{Store: store}); err != nil {
		return err
	}

	a, err := agent.New(agent.Options{
		LLM: llmClient, Tools: reg, MaxIterations: cfg.Agent.MaxIterations,
	})
	if err != nil {
		return err
	}

	if userInput == "" {
		userInput, err = readStdin()
		if err != nil {
			return err
		}
	}
	if userInput == "" {
		return errors.New("empty input (pass a prompt or pipe stdin)")
	}

	res, err := a.RunTurn(ctx, userInput)
	if err != nil {
		return err
	}
	fmt.Println(res.Output)
	logger.Info("turn complete", "iterations", res.Iterations)
	return nil
}

func readStdin() (string, error) {
	st, _ := os.Stdin.Stat()
	if st.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var b []byte
	for sc.Scan() {
		b = append(b, sc.Bytes()...)
		b = append(b, '\n')
	}
	return string(b), sc.Err()
}
