package config

import (
	"fmt"
	"os"
	"strings"
)

// ResolveSecret turns a config-space reference into the actual secret value.
// Supported schemes:
//   - "env:NAME"  → value of $NAME
//   - "NAME"      → shorthand for env:NAME
//
// Reserved for later: "keyring:..." (returns ErrUnsupportedScheme today).
func ResolveSecret(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty secret reference")
	}
	scheme, rest, hasColon := strings.Cut(ref, ":")
	if !hasColon {
		scheme, rest = "env", ref
	}
	switch scheme {
	case "env":
		v := os.Getenv(rest)
		if v == "" {
			return "", fmt.Errorf("env var %s is empty", rest)
		}
		return v, nil
	default:
		return "", fmt.Errorf("unsupported secret scheme %q (only env:NAME for now)", scheme)
	}
}
