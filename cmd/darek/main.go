package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	cfgPath := os.Getenv("DAREK_CONFIG")
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".darek", "config.yaml")
	}

	switch cmd {
	case "migrate":
		return runMigrate(ctx, cfgPath)
	case "doctor":
		return runDoctor(ctx, cfgPath)
	case "calendar":
		return runCalendar(ctx, cfgPath, args)
	case "mail":
		return runMail(ctx, cfgPath, args)
	case "", "chat":
		return runChat(ctx, cfgPath, strings.Join(args, " "))
	default:
		return fmt.Errorf("unknown subcommand %q (try: chat, migrate, doctor, calendar, mail)", cmd)
	}
}
