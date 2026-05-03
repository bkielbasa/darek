package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const sessionCookieName = "darek_session"

// AuthConfig is the subset of auth state needed by sign/verify and the
// middleware. Built from cfg.Auth + resolved env values in runServe.
type AuthConfig struct {
	Username     string
	PasswordHash []byte // bcrypt hash bytes
	SessionKey   []byte // HMAC key, ≥32 bytes
	SessionTTL   time.Duration
}

// signSession encodes "<user>|<unix-expiry>|<hex-HMAC>" as base64-url.
func (a AuthConfig) signSession(user string, exp time.Time) string {
	payload := fmt.Sprintf("%s|%d", user, exp.Unix())
	mac := hmac.New(sha256.New, a.SessionKey)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + sig))
}

// verifyCookie returns the username from a valid token, or ("", false).
// "Valid" means: parses, HMAC matches with SessionKey, not expired, and the
// username matches a.Username (so rotating Username invalidates old cookies).
func (a AuthConfig) verifyCookie(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", false
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return "", false
	}
	user, expStr, sig := parts[0], parts[1], parts[2]

	payload := user + "|" + expStr
	mac := hmac.New(sha256.New, a.SessionKey)
	mac.Write([]byte(payload))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(user), []byte(a.Username)) != 1 {
		return "", false
	}
	return user, true
}
