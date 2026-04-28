package mail

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIConfirmer_Yes(t *testing.T) {
	out := &bytes.Buffer{}
	c := &CLIConfirmer{In: strings.NewReader("y\n"), Out: out}
	ok, err := c.Confirm(context.Background(), Preview{
		Account: "p", From: "me@x.com", To: []string{"a@x.com"}, Subject: "Hi", Body: "hello",
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, out.String(), "From:    p")
	require.Contains(t, out.String(), "Subject: Hi")
}

func TestCLIConfirmer_No(t *testing.T) {
	out := &bytes.Buffer{}
	c := &CLIConfirmer{In: strings.NewReader("n\n"), Out: out}
	ok, err := c.Confirm(context.Background(), Preview{Subject: "x", To: []string{"a@x"}})
	require.NoError(t, err)
	require.False(t, ok)
}

func TestCLIConfirmer_EOF(t *testing.T) {
	out := &bytes.Buffer{}
	c := &CLIConfirmer{In: strings.NewReader(""), Out: out}
	ok, err := c.Confirm(context.Background(), Preview{Subject: "x", To: []string{"a@x"}})
	require.NoError(t, err)
	require.False(t, ok)
}
