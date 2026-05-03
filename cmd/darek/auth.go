package main

import (
	"context"
	"fmt"
	"io"

	"golang.org/x/crypto/bcrypt"
)

// runAuth dispatches `darek auth <subcmd> ...`. Currently only `hash`.
// out is where the hash is printed (os.Stdout in main; bytes.Buffer in tests).
func runAuth(_ context.Context, args []string, out io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: darek auth hash <password>")
	}
	switch args[0] {
	case "hash":
		if len(args) < 2 {
			return fmt.Errorf("usage: darek auth hash <password>")
		}
		h, err := bcrypt.GenerateFromPassword([]byte(args[1]), 12)
		if err != nil {
			return fmt.Errorf("hash: %w", err)
		}
		fmt.Fprintln(out, string(h))
		return nil
	default:
		return fmt.Errorf("unknown auth subcommand %q (try: hash)", args[0])
	}
}
