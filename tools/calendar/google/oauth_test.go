package google

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestTokenStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewTokenStore(filepath.Join(dir, "oauth"))
	tok := &oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour).Truncate(time.Second)}
	require.NoError(t, s.Save("personal", tok))
	got, err := s.Load("personal")
	require.NoError(t, err)
	require.Equal(t, "a", got.AccessToken)
	require.Equal(t, "r", got.RefreshToken)
}

func TestTokenStore_LoadMissing(t *testing.T) {
	s := NewTokenStore(t.TempDir())
	_, err := s.Load("nope")
	require.Error(t, err)
}
