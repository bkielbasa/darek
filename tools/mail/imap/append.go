package imap

import (
	"context"
	"fmt"
	"time"

	goimap "github.com/emersion/go-imap/v2"
)

// Append uploads raw RFC 5322 bytes into folder with the given flags.
// It uses the streaming APPEND command (Write/Close/Wait).
func (a *Account) Append(ctx context.Context, folder string, flags []string, raw []byte) error {
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Logout().Wait() }()

	gflags := make([]goimap.Flag, len(flags))
	for i, f := range flags {
		gflags[i] = goimap.Flag(f)
	}

	cmd := c.Append(folder, int64(len(raw)), &goimap.AppendOptions{
		Time:  time.Now(),
		Flags: gflags,
	})

	if _, err := cmd.Write(raw); err != nil {
		_ = cmd.Close()
		return fmt.Errorf("append write: %w", err)
	}
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("append close: %w", err)
	}
	if _, err := cmd.Wait(); err != nil {
		return fmt.Errorf("append wait: %w", err)
	}

	return nil
}
