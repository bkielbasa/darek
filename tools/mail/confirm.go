package mail

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// Preview is what's shown to the user before they decide whether to send.
type Preview struct {
	Account     string
	From        string
	To          []string
	Cc          []string
	Bcc         []string
	Subject     string
	Body        string
	Attachments []string // local paths
}

// Confirmer asks the user whether to perform an action.
type Confirmer interface {
	Confirm(ctx context.Context, p Preview) (bool, error)
}

// CLIConfirmer renders the preview to Out and reads y/N from In.
// Defaults to stderr/stdin so it works under piped stdout.
type CLIConfirmer struct {
	In  io.Reader
	Out io.Writer
}

func NewCLIConfirmer() *CLIConfirmer {
	return &CLIConfirmer{In: os.Stdin, Out: os.Stderr}
}

func (c *CLIConfirmer) Confirm(_ context.Context, p Preview) (bool, error) {
	out := c.Out
	if out == nil {
		out = os.Stderr
	}
	in := c.In
	if in == nil {
		in = os.Stdin
	}

	fmt.Fprintln(out, "—— mail preview ——")
	fmt.Fprintf(out, "From:    %s (%s)\n", p.Account, p.From)
	fmt.Fprintf(out, "To:      %s\n", strings.Join(p.To, ", "))
	if len(p.Cc) > 0 {
		fmt.Fprintf(out, "Cc:      %s\n", strings.Join(p.Cc, ", "))
	}
	if len(p.Bcc) > 0 {
		fmt.Fprintf(out, "Bcc:     %s\n", strings.Join(p.Bcc, ", "))
	}
	fmt.Fprintf(out, "Subject: %s\n", p.Subject)
	if len(p.Attachments) > 0 {
		fmt.Fprintf(out, "Attachments: %s\n", strings.Join(p.Attachments, ", "))
	}
	fmt.Fprintln(out, "————")
	fmt.Fprintln(out, p.Body)
	fmt.Fprintln(out, "————")
	fmt.Fprint(out, "Send? [y/N] ")

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("read input: %w", err)
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes", nil
}
