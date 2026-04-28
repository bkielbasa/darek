package main

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"darek/config"
	"darek/tools/calendar/google"
)

// runCalendar dispatches `darek calendar <subcmd> <args...>`.
func runCalendar(ctx context.Context, cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: darek calendar refresh-token <nickname>")
	}
	switch args[0] {
	case "refresh-token":
		if len(args) < 2 {
			return fmt.Errorf("usage: darek calendar refresh-token <nickname>")
		}
		return runRefreshToken(ctx, cfgPath, args[1])
	default:
		return fmt.Errorf("unknown calendar subcommand %q (try: refresh-token)", args[0])
	}
}

func runRefreshToken(ctx context.Context, cfgPath, nickname string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	var src *config.CalendarSrc
	for i := range cfg.Calendars {
		c := &cfg.Calendars[i]
		if c.Nickname == nickname {
			src = c
			break
		}
	}
	if src == nil {
		return fmt.Errorf("no calendar with nickname %q in config", nickname)
	}
	if src.Kind != "google" {
		return fmt.Errorf("calendar %q is not a Google source (kind=%s)", nickname, src.Kind)
	}
	clientID, err := config.ResolveSecret("env:" + src.ClientIDEnv)
	if err != nil {
		return fmt.Errorf("client id: %w", err)
	}
	clientSecret, err := config.ResolveSecret("env:" + src.ClientSecretEnv)
	if err != nil {
		return fmt.Errorf("client secret: %w", err)
	}
	oauthCfg := google.Config(clientID, clientSecret)

	authURL := oauthCfg.AuthCodeURL("state-token")
	fmt.Fprintf(os.Stderr, "Open this URL in a browser, grant access, then paste the code shown:\n\n%s\n\n", authURL)
	fmt.Fprint(os.Stderr, "code: ")
	code, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read code: %w", err)
	}
	code = strings.TrimSpace(code)
	// In case the user pasted a URL with `code=...` instead of the bare code, extract it.
	if u, perr := url.Parse(code); perr == nil && u.Query().Get("code") != "" {
		code = u.Query().Get("code")
	}

	tok, err := oauthCfg.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}
	home, _ := os.UserHomeDir()
	store := google.NewTokenStore(filepath.Join(home, ".darek", "oauth"))
	if err := store.Save(nickname, tok); err != nil {
		return err
	}
	fmt.Printf("token saved for calendar %q\n", nickname)
	return nil
}
