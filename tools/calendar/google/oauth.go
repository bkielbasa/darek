package google

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
)

// TokenStore persists OAuth2 tokens to ~/.darek/oauth/<nickname>.json.
type TokenStore struct {
	dir string
}

func NewTokenStore(dir string) *TokenStore { return &TokenStore{dir: dir} }

func (s *TokenStore) path(nickname string) string {
	return filepath.Join(s.dir, nickname+".json")
}

func (s *TokenStore) Save(nickname string, tok *oauth2.Token) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", s.dir, err)
	}
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	if err := os.WriteFile(s.path(nickname), b, 0o600); err != nil {
		return fmt.Errorf("write token: %w", err)
	}
	return nil
}

func (s *TokenStore) Load(nickname string) (*oauth2.Token, error) {
	b, err := os.ReadFile(s.path(nickname))
	if err != nil {
		return nil, fmt.Errorf("read token %s: %w", nickname, err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, fmt.Errorf("unmarshal token: %w", err)
	}
	return &tok, nil
}
