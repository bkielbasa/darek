package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestRunAuth_HashRoundtrip(t *testing.T) {
	var out bytes.Buffer
	err := runAuth(context.Background(), []string{"hash", "secretpw"}, &out)
	require.NoError(t, err)

	hash := strings.TrimSpace(out.String())
	require.NotEmpty(t, hash)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("secretpw")))
}

func TestRunAuth_BadUsage(t *testing.T) {
	cases := [][]string{
		{},
		{"hash"},
		{"unknown"},
	}
	for _, args := range cases {
		var out bytes.Buffer
		err := runAuth(context.Background(), args, &out)
		require.Error(t, err)
	}
}
