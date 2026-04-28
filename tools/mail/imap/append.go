package imap

import (
	"context"
	"fmt"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Append uploads raw RFC 5322 bytes into folder with the given flags.
// It uses the streaming APPEND command (Write/Close/Wait).
func (a *Account) Append(ctx context.Context, folder string, flags []string, raw []byte) error {
	ctx, span := a.tracer.Start(ctx, "imap.append",
		trace.WithAttributes(
			attribute.String("imap.folder", folder),
			attribute.Int("imap.bytes", len(raw)),
		),
	)
	defer span.End()

	c, err := a.connect(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
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
		err = fmt.Errorf("append write: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	if err := cmd.Close(); err != nil {
		err = fmt.Errorf("append close: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	if _, err := cmd.Wait(); err != nil {
		err = fmt.Errorf("append wait: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	return nil
}
