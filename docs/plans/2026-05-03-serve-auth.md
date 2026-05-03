# Serve Auth (login/password) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add HTML login/password authentication to `darek serve`. Single user, credentials from env vars (`DAREK_AUTH_USERNAME`, `DAREK_AUTH_PASSWORD_HASH`, `DAREK_SESSION_KEY`). Stateless signed-cookie sessions (HMAC-SHA256), 30-day rolling expiry. Bypass list: `/healthz`, `/static/`, `/login`, `/logout`.

**Architecture:** New file `cmd/darek/serve/auth.go` holds cookie sign/verify, the `requireAuth` middleware, and login/logout HTTP handlers. `serve/server.go` wraps the mux in the middleware via `Handler()`. New `cmd/darek/auth.go` adds a `darek auth hash <password>` subcommand for generating bcrypt hashes locally.

**Tech Stack:** `golang.org/x/crypto/bcrypt`, `crypto/hmac`/`crypto/sha256`/`crypto/subtle` (stdlib), `encoding/base64`, existing `html/template` infra.

**Design source:** brainstormed in conversation 2026-05-03; user accepted 5-section design (architecture, config, login UI, middleware+verify, error handling/testing).

**Out of scope:** Multi-user, OAuth/SSO, user-management endpoints, password reset flow, login rate limiting (single user, behind ingress, low risk), API token auth (separate concern for STT/mobile).

---

## File Map

| Path | Responsibility |
|---|---|
| `config/types.go` | (modify) add `Auth` struct + field on `Config`. |
| `cmd/darek/auth.go` | (create) `runAuth` for `darek auth hash <password>` subcommand. |
| `cmd/darek/auth_test.go` | (create) hash round-trip test. |
| `cmd/darek/main.go` | (modify) dispatch `auth` case in subcommand switch. |
| `cmd/darek/serve/auth.go` | (create) `signSession`, `verifyCookie`, `requireAuth` middleware, login/logout handlers, `setSessionCookie`, `clearSessionCookie`, `isPublicPath`, `sanitizeNext`. |
| `cmd/darek/serve/auth_test.go` | (create) sign/verify, middleware, login flow, sanitize tests. |
| `cmd/darek/serve/server.go` | (modify) Server struct gains `authUser`, `authPasswordHash`, `sessionKey`, `sessionTTL`; `New` accepts an `AuthConfig`; `routes()` registers `/login` + `/logout`; `Handler()` wraps mux with `requireAuth`. |
| `cmd/darek/serve/server_test.go` | (modify) update fixtures to pass an `AuthConfig`. |
| `cmd/darek/serve/templates/login.html` | (create) login form template. |
| `cmd/darek/serve/static/style.css` | (modify) add minimal `.auth` / `.login-card` / `.error` styles. |
| `cmd/darek/serve.go` | (modify) `runServe` resolves auth env vars, builds `AuthConfig`, fails fast on missing. |

---

## Task 1 — Config: `Auth` struct

**Files:**
- Modify: `config/types.go`

- [ ] **Step 1: Add struct + field**

In `config/types.go`, add after the `Server` struct (or wherever convenient near top-level Config fields). First check the existing imports — `time` should already be imported.

```go
type Auth struct {
	UsernameEnv     string        `yaml:"username_env"`
	PasswordHashEnv string        `yaml:"password_hash_env"`
	SessionKeyEnv   string        `yaml:"session_key_env"`
	SessionTTL      time.Duration `yaml:"session_ttl"` // optional; default 720h
}
```

In the `Config` struct, append:

```go
Auth Auth `yaml:"auth"`
```

- [ ] **Step 2: Build to verify**

Run: `cd /Users/bklimczak/Projects/darek && go build ./config/...`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add config/types.go
git commit -m "feat(config): add Auth block (username_env, password_hash_env, session_key_env)"
```

---

## Task 2 — `darek auth hash` subcommand (TDD)

**Files:**
- Create: `cmd/darek/auth.go`
- Create: `cmd/darek/auth_test.go`
- Modify: `cmd/darek/main.go`

This command exists so the user can generate a bcrypt hash locally without external tooling: `./darek auth hash mypass` prints the hash on stdout.

- [ ] **Step 1: Add bcrypt dependency**

Run: `cd /Users/bklimczak/Projects/darek && go get golang.org/x/crypto/bcrypt`
Expected: go.mod and go.sum updated. No build output.

- [ ] **Step 2: Write failing test**

Create `cmd/darek/auth_test.go`:

```go
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /Users/bklimczak/Projects/darek && go test ./cmd/darek/ -run TestRunAuth -v`
Expected: FAIL — `runAuth` undefined.

- [ ] **Step 4: Implement**

Create `cmd/darek/auth.go`:

```go
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
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /Users/bklimczak/Projects/darek && go test ./cmd/darek/ -run TestRunAuth -v`
Expected: PASS.

- [ ] **Step 6: Wire into main.go**

In `cmd/darek/main.go`, add a case before `default`:

```go
	case "auth":
		return runAuth(ctx, args, os.Stdout)
```

Update the `default` error string to mention `auth`:

```go
	default:
		return fmt.Errorf("unknown subcommand %q (try: chat, migrate, doctor, calendar, mail, freshrss, todoist, serve, auth)", cmd)
```

- [ ] **Step 7: Build + smoke test**

Run: `cd /Users/bklimczak/Projects/darek && go build ./cmd/darek && ./darek auth hash testpw`
Expected: prints a `$2a$12$...` bcrypt hash to stdout.

- [ ] **Step 8: Commit**

```bash
git add cmd/darek/auth.go cmd/darek/auth_test.go cmd/darek/main.go go.mod go.sum
git commit -m "feat(darek): add 'darek auth hash <password>' subcommand"
```

---

## Task 3 — Sign/verify session cookies (TDD)

**Files:**
- Create: `cmd/darek/serve/auth.go` (sign/verify only)
- Create: `cmd/darek/serve/auth_test.go`

Pure crypto + format. No HTTP yet. The signing key is a `[]byte`; the username being verified must match a configured value (so changing the username invalidates outstanding cookies).

- [ ] **Step 1: Write failing tests**

Create `cmd/darek/serve/auth_test.go`:

```go
package serve

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	testKey   = []byte("0123456789abcdef0123456789abcdef")
	otherKey  = []byte("ffffffffffffffffffffffffffffffff")
	testUser  = "bartek"
	testHash  = []byte("$2a$10$abcdefghijklmnopqrstuv") // shape only; not validated here
)

func newTestAuth(ttl time.Duration) AuthConfig {
	return AuthConfig{
		Username:     testUser,
		PasswordHash: testHash,
		SessionKey:   testKey,
		SessionTTL:   ttl,
	}
}

func TestSignVerify_Roundtrip(t *testing.T) {
	a := newTestAuth(time.Hour)
	tok := a.signSession(testUser, time.Now().Add(time.Hour))
	user, ok := a.verifyCookie(tok)
	require.True(t, ok)
	require.Equal(t, testUser, user)
}

func TestVerify_TamperedSig(t *testing.T) {
	a := newTestAuth(time.Hour)
	tok := a.signSession(testUser, time.Now().Add(time.Hour))
	// flip the last char of the encoded token
	bad := tok[:len(tok)-1] + flipChar(tok[len(tok)-1])
	_, ok := a.verifyCookie(bad)
	require.False(t, ok)
}

func TestVerify_TamperedPayloadUsername(t *testing.T) {
	a := newTestAuth(time.Hour)
	tok := a.signSession(testUser, time.Now().Add(time.Hour))
	// inject a different username by re-signing with a different key:
	// produce a token that LOOKS valid for "alice" but with the real key's sig
	// for "bartek". Easiest: just verify that a token signed for a different
	// user via the correct key still gets rejected when AuthConfig expects bartek.
	tokForAlice := a.signSession("alice", time.Now().Add(time.Hour))
	_, ok := a.verifyCookie(tokForAlice)
	require.False(t, ok)
}

func TestVerify_Expired(t *testing.T) {
	a := newTestAuth(time.Hour)
	tok := a.signSession(testUser, time.Now().Add(-time.Second))
	_, ok := a.verifyCookie(tok)
	require.False(t, ok)
}

func TestVerify_WrongKey(t *testing.T) {
	signer := newTestAuth(time.Hour)
	verifier := signer
	verifier.SessionKey = otherKey
	tok := signer.signSession(testUser, time.Now().Add(time.Hour))
	_, ok := verifier.verifyCookie(tok)
	require.False(t, ok)
}

func TestVerify_Garbage(t *testing.T) {
	a := newTestAuth(time.Hour)
	for _, junk := range []string{"", "notbase64$$$", "YWJj", strings.Repeat("a", 200)} {
		_, ok := a.verifyCookie(junk)
		require.False(t, ok, "junk = %q", junk)
	}
}

// flipChar returns a different rune of the same general class so the result
// stays a valid base64-url char (we want format-valid but signature-invalid).
func flipChar(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/bklimczak/Projects/darek && go test ./cmd/darek/serve/ -run "TestSignVerify|TestVerify" -v`
Expected: FAIL — `AuthConfig`, `signSession`, `verifyCookie` undefined.

- [ ] **Step 3: Create `cmd/darek/serve/auth.go` with sign/verify**

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/bklimczak/Projects/darek && go test ./cmd/darek/serve/ -run "TestSignVerify|TestVerify" -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/darek/serve/auth.go cmd/darek/serve/auth_test.go
git commit -m "feat(serve): add session cookie sign/verify (HMAC-SHA256)"
```

---

## Task 4 — Login/logout handlers + middleware (TDD)

**Files:**
- Modify: `cmd/darek/serve/auth.go`
- Modify: `cmd/darek/serve/auth_test.go`

Adds the HTTP layer: `requireAuth` middleware, `handleLoginGet`, `handleLoginPost`, `handleLogout`, `setSessionCookie`, `clearSessionCookie`, `isPublicPath`, `sanitizeNext`. The Server struct will gain an `auth AuthConfig` field — Task 5 wires that.

For testing this task in isolation, we instantiate a minimal `Server` struct directly and register only the auth handlers. The full server wiring comes in Task 5.

- [ ] **Step 1: Append failing tests**

Append to `cmd/darek/serve/auth_test.go`:

```go
import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Replace the existing import block at the top with the merged set above
// (just add context, io, net/http, net/http/httptest, net/url, golang.org/x/crypto/bcrypt
// to whatever was already imported).

func freshHash(t *testing.T, pw string) []byte {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 4) // low cost in tests
	require.NoError(t, err)
	return h
}

func newAuthedServer(t *testing.T, pw string) *Server {
	t.Helper()
	a := AuthConfig{
		Username:     "bartek",
		PasswordHash: freshHash(t, pw),
		SessionKey:   testKey,
		SessionTTL:   time.Hour,
	}
	tmpl, err := parseTemplates()
	require.NoError(t, err)
	s := &Server{
		mux:  http.NewServeMux(),
		tmpl: tmpl,
		auth: a,
	}
	s.routesAuth()
	// register a sentinel "private" route to test the middleware
	s.mux.HandleFunc("GET /private", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "private-ok")
	})
	return s
}

func TestIsPublicPath(t *testing.T) {
	for _, p := range []string{"/healthz", "/login", "/logout", "/static/style.css", "/static/img/x.png"} {
		require.True(t, isPublicPath(p), p)
	}
	for _, p := range []string{"/", "/all", "/sync", "/links/abc/rating"} {
		require.False(t, isPublicPath(p), p)
	}
}

func TestSanitizeNext(t *testing.T) {
	require.Equal(t, "/", sanitizeNext(""))
	require.Equal(t, "/", sanitizeNext("//evil.com"))
	require.Equal(t, "/", sanitizeNext("https://evil.com"))
	require.Equal(t, "/", sanitizeNext("javascript:alert(1)"))
	require.Equal(t, "/all", sanitizeNext("/all"))
	require.Equal(t, "/links/abc?x=1", sanitizeNext("/links/abc?x=1"))
}

func TestRequireAuth_BypassesPublic(t *testing.T) {
	s := newAuthedServer(t, "pw")
	for _, p := range []string{"/healthz", "/login"} {
		req := httptest.NewRequest("GET", p, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		require.NotEqual(t, http.StatusSeeOther, w.Code, "path %s", p)
	}
}

func TestRequireAuth_RedirectsAnonymousToLogin(t *testing.T) {
	s := newAuthedServer(t, "pw")
	req := httptest.NewRequest("GET", "/private?x=1", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code)
	loc := w.Header().Get("Location")
	require.Contains(t, loc, "/login?next=")
	require.Contains(t, loc, url.QueryEscape("/private?x=1"))
}

func TestRequireAuth_PassesValidCookie(t *testing.T) {
	s := newAuthedServer(t, "pw")
	tok := s.auth.signSession(s.auth.Username, time.Now().Add(time.Hour))
	req := httptest.NewRequest("GET", "/private", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tok})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "private-ok", w.Body.String())
	// Rolling expiry: a refreshed cookie was set on the response
	require.NotEmpty(t, w.Result().Cookies(), "expected refreshed cookie")
}

func TestLoginPost_Success(t *testing.T) {
	s := newAuthedServer(t, "rightpw")
	form := url.Values{"username": {"bartek"}, "password": {"rightpw"}}
	req := httptest.NewRequest("POST", "/login?next=%2Fall", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/all", w.Header().Get("Location"))
	cookies := w.Result().Cookies()
	require.NotEmpty(t, cookies)
	require.Equal(t, sessionCookieName, cookies[0].Name)
	require.NotEmpty(t, cookies[0].Value)
	require.True(t, cookies[0].HttpOnly)
}

func TestLoginPost_BadPassword(t *testing.T) {
	s := newAuthedServer(t, "rightpw")
	form := url.Values{"username": {"bartek"}, "password": {"wrong"}}
	req := httptest.NewRequest("POST", "/login?next=%2Fall", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Contains(t, w.Header().Get("Location"), "error=invalid")
	require.Contains(t, w.Header().Get("Location"), "next=")
	require.Empty(t, w.Result().Cookies())
}

func TestLoginPost_BadUsername(t *testing.T) {
	s := newAuthedServer(t, "rightpw")
	form := url.Values{"username": {"someoneelse"}, "password": {"rightpw"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Contains(t, w.Header().Get("Location"), "error=invalid")
}

func TestLoginPost_OpenRedirectBlocked(t *testing.T) {
	s := newAuthedServer(t, "rightpw")
	form := url.Values{"username": {"bartek"}, "password": {"rightpw"}}
	req := httptest.NewRequest("POST", "/login?next=//evil.com", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, "/", w.Header().Get("Location"))
}

func TestLoginGet_Renders(t *testing.T) {
	s := newAuthedServer(t, "pw")
	req := httptest.NewRequest("GET", "/login?next=%2Fall&error=invalid", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	require.Contains(t, body, "Sign in")
	require.Contains(t, body, "Invalid login or password")
	require.Contains(t, body, `name="username"`)
	require.Contains(t, body, `name="password"`)
}

func TestLogout_ClearsCookie(t *testing.T) {
	s := newAuthedServer(t, "pw")
	req := httptest.NewRequest("POST", "/logout", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/login", w.Header().Get("Location"))
	cookies := w.Result().Cookies()
	require.NotEmpty(t, cookies)
	require.Equal(t, sessionCookieName, cookies[0].Name)
	require.Equal(t, "", cookies[0].Value)
	require.True(t, cookies[0].MaxAge < 0)
}

// keep the unused import linter happy in environments where ctx isn't used elsewhere
var _ = context.Background
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/bklimczak/Projects/darek && go test ./cmd/darek/serve/ -v -run "TestIsPublicPath|TestSanitizeNext|TestRequireAuth|TestLogin|TestLogout"`
Expected: FAIL — `Server.auth`, `routesAuth`, `requireAuth`, `isPublicPath`, `sanitizeNext`, `handleLoginGet/Post`, `handleLogout` undefined. (`Server` itself exists but doesn't have `auth` field yet — Task 5 adds it.)

- [ ] **Step 3: Add `auth` field to Server (minimal change for tests to compile)**

In `cmd/darek/serve/server.go`, add a field to the `Server` struct:

```go
type Server struct {
	store   *links.Store
	tmpl    *template.Template
	mux     *http.ServeMux
	sync    SyncFn
	analyze Analyzer
	auth    AuthConfig
}
```

(Don't change `New` yet — Task 5 does that.)

- [ ] **Step 4: Implement handlers + middleware in `auth.go`**

Append to `cmd/darek/serve/auth.go`:

```go
import (
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Merge with existing import block at the top of the file.

// routesAuth registers /login (GET, POST) and /logout (POST) on s.mux.
// Called from Server.routes (Task 5).
func (s *Server) routesAuth() {
	s.mux.HandleFunc("GET /login", s.handleLoginGet)
	s.mux.HandleFunc("POST /login", s.handleLoginPost)
	s.mux.HandleFunc("POST /logout", s.handleLogout)
}

// requireAuth wraps next; bypasses isPublicPath, otherwise redirects to /login
// when no valid cookie is present. On success, refreshes the cookie's expiry
// (rolling session).
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(sessionCookieName)
		if err == nil {
			if _, ok := s.auth.verifyCookie(c.Value); ok {
				s.setSessionCookie(w) // rolling
				next.ServeHTTP(w, r)
				return
			}
		}
		nextURL := url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, "/login?next="+nextURL, http.StatusSeeOther)
	})
}

func isPublicPath(p string) bool {
	return p == "/healthz" || p == "/login" || p == "/logout" ||
		strings.HasPrefix(p, "/static/")
}

// sanitizeNext keeps the user's intended URL safe to redirect back to.
// Must start with `/` and NOT with `//` (would be a network-path open redirect).
// Schemes like javascript: never start with `/`, so the first check filters them.
func sanitizeNext(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") {
		return "/"
	}
	return s
}

func (s *Server) setSessionCookie(w http.ResponseWriter) {
	exp := time.Now().Add(s.auth.SessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.auth.signSession(s.auth.Username, exp),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.auth.SessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

type loginPageData struct {
	Error bool
	Next  string
}

func (s *Server) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	data := loginPageData{
		Error: r.URL.Query().Get("error") == "invalid",
		Next:  sanitizeNext(r.URL.Query().Get("next")),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "login.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	user := r.FormValue("username")
	pw := r.FormValue("password")
	next := sanitizeNext(r.URL.Query().Get("next"))

	userOK := subtleEqual(user, s.auth.Username)
	pwOK := bcrypt.CompareHashAndPassword(s.auth.PasswordHash, []byte(pw)) == nil

	if !userOK || !pwOK {
		http.Redirect(w, r,
			"/login?error=invalid&next="+url.QueryEscape(next),
			http.StatusSeeOther)
		return
	}
	s.setSessionCookie(w)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// subtleEqual is a constant-time string comparison wrapper.
func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
```

Add `time` to the import block if not already present (it should be from the sign/verify code in Task 3).

- [ ] **Step 5: Make the tests' `Server.Handler()` route through requireAuth**

The tests call `s.Handler()` and expect the middleware in front. `Handler()` currently does:
```go
return otelhttp.NewHandler(s.mux, "darek.serve")
```

Change it to wrap with requireAuth:

```go
func (s *Server) Handler() http.Handler {
	return otelhttp.NewHandler(s.requireAuth(s.mux), "darek.serve")
}
```

- [ ] **Step 6: Add the login template (placeholder; styled in Task 6)**

Create `cmd/darek/serve/templates/login.html`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>darek — login</title>
  <link rel="stylesheet" href="/static/style.css">
</head>
<body class="auth">
  <main class="login-card">
    <h1>darek</h1>
    {{ if .Error }}<p class="error">Invalid login or password.</p>{{ end }}
    <form method="POST" action="/login{{ if .Next }}?next={{ .Next | urlquery }}{{ end }}">
      <label>Username <input type="text" name="username" autofocus required></label>
      <label>Password <input type="password" name="password" required></label>
      <button type="submit">Sign in</button>
    </form>
  </main>
</body>
</html>
```

- [ ] **Step 7: Run all auth tests**

Run: `cd /Users/bklimczak/Projects/darek && go test ./cmd/darek/serve/ -v -run "TestIsPublicPath|TestSanitizeNext|TestRequireAuth|TestLogin|TestLogout|TestSignVerify|TestVerify"`
Expected: every test PASSes.

- [ ] **Step 8: Commit**

```bash
git add cmd/darek/serve/auth.go cmd/darek/serve/auth_test.go cmd/darek/serve/server.go cmd/darek/serve/templates/login.html
git commit -m "feat(serve): add login/logout handlers + requireAuth middleware"
```

---

## Task 5 — Wire `New` and `routes` to use auth

**Files:**
- Modify: `cmd/darek/serve/server.go`
- Modify: `cmd/darek/serve/server_test.go`

`New` must accept `AuthConfig` so production code passes real env-derived values. Existing tests that build `Server` need to be updated to pass an `AuthConfig` (use empty/dummy values; tests that exercise auth use `newAuthedServer` from Task 4).

- [ ] **Step 1: Inspect existing `server_test.go` callers of `New`**

Run: `cd /Users/bklimczak/Projects/darek && grep -n "serve.New\|New(" cmd/darek/serve/server_test.go`
Expected: identify call sites that need an `AuthConfig` argument.

- [ ] **Step 2: Change `New` signature**

In `cmd/darek/serve/server.go`:

```go
func New(store *links.Store, sync SyncFn, analyzer Analyzer, auth AuthConfig) (*Server, error) {
	t, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{store: store, tmpl: t, mux: http.NewServeMux(), sync: sync, analyze: analyzer, auth: auth}
	s.routes()
	return s, nil
}
```

In `routes()`, add the auth routes:

```go
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	staticFS, _ := fs.Sub(StaticFS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	s.routesAuth()

	s.mux.Handle("GET /{$}", s.handleList(true))
	s.mux.Handle("GET /all", s.handleList(false))
	s.mux.HandleFunc("POST /sync", s.handleSync)
	s.mux.HandleFunc("POST /links/new", s.handleNew)
	s.mux.HandleFunc("POST /links/{id}/rating", s.handleRating)
	s.mux.HandleFunc("POST /links/{id}/tags", s.handleTags)
	s.mux.HandleFunc("POST /links/{id}/notes", s.handleNotes)
	s.mux.HandleFunc("POST /links/{id}/kind", s.handleKind)
	s.mux.HandleFunc("POST /links/{id}/analyze", s.handleAnalyze)
}
```

`AuthConfig` is unexported but lives in the same package, so the change is package-internal. External callers (only `runServe` in Task 7) construct it from config in this same module.

- [ ] **Step 3: Make `AuthConfig` constructible from outside the test file**

Add this helper near the bottom of `cmd/darek/serve/auth.go`:

```go
// NewAuthConfig builds an AuthConfig from resolved values. Used by runServe.
// Returns an error if any required value is missing or the session key is too
// short.
func NewAuthConfig(username string, passwordHash, sessionKey []byte, ttl time.Duration) (AuthConfig, error) {
	if username == "" {
		return AuthConfig{}, fmt.Errorf("auth: username required")
	}
	if len(passwordHash) == 0 {
		return AuthConfig{}, fmt.Errorf("auth: password hash required")
	}
	if len(sessionKey) < 32 {
		return AuthConfig{}, fmt.Errorf("auth: session key must be at least 32 bytes (got %d)", len(sessionKey))
	}
	if ttl <= 0 {
		ttl = 720 * time.Hour
	}
	return AuthConfig{
		Username:     username,
		PasswordHash: passwordHash,
		SessionKey:   sessionKey,
		SessionTTL:   ttl,
	}, nil
}
```

- [ ] **Step 4: Update existing `server_test.go` callers of `New`**

The current file has exactly two callers, both `serve.New(nil, nil, nil)`, in `TestServer_Healthz` and `TestServer_StaticCSS`. Both test public-bypass paths (`/healthz`, `/static/style.css`) so no real cookie is needed — they just need `New` to accept a 4th argument.

Replace the entire file with:

```go
package serve_test

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"darek/cmd/darek/serve"
)

func dummyAuth(t *testing.T) serve.AuthConfig {
	t.Helper()
	a, err := serve.NewAuthConfig(
		"test",
		[]byte("placeholder-hash"),
		bytes.Repeat([]byte{0}, 32),
		time.Hour,
	)
	if err != nil {
		t.Fatalf("dummy auth: %v", err)
	}
	return a
}

func TestServer_Healthz(t *testing.T) {
	s, err := serve.New(nil, nil, nil, dummyAuth(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("status %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body %q, want ok", rec.Body.String())
	}
}

func TestServer_StaticCSS(t *testing.T) {
	s, err := serve.New(nil, nil, nil, dummyAuth(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	req := httptest.NewRequest("GET", "/static/style.css", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("status %d, want 200", rec.Code)
	}
}
```

- [ ] **Step 5: Run all serve tests**

Run: `cd /Users/bklimczak/Projects/darek && go test ./cmd/darek/serve/ -v`
Expected: all PASS (or only the skipped-with-message ones not running).

- [ ] **Step 6: Commit**

```bash
git add cmd/darek/serve/server.go cmd/darek/serve/server_test.go cmd/darek/serve/auth.go
git commit -m "feat(serve): require AuthConfig in New, register auth routes"
```

---

## Task 6 — Login page styling

**Files:**
- Modify: `cmd/darek/serve/static/style.css`

Make the login page look like the rest of the app. Minimal additions — card centered on a soft background, native form widgets.

- [ ] **Step 1: Append CSS rules**

Append to `cmd/darek/serve/static/style.css`:

```css
/* ---- Login ---- */
body.auth {
  margin: 0;
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #f5f5f7;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  color: #1d1d1f;
}
.login-card {
  background: #ffffff;
  padding: 32px 28px;
  border: 1px solid #e5e5ea;
  border-radius: 12px;
  width: 100%;
  max-width: 320px;
  box-sizing: border-box;
}
.login-card h1 {
  margin: 0 0 16px 0;
  font-size: 22px;
  font-weight: 600;
  text-align: center;
}
.login-card form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.login-card label {
  display: flex;
  flex-direction: column;
  font-size: 12px;
  color: #6e6e73;
  gap: 4px;
}
.login-card input {
  padding: 8px 10px;
  font-size: 14px;
  border: 1px solid #d2d2d7;
  border-radius: 6px;
  font-family: inherit;
}
.login-card input:focus {
  outline: 2px solid #0071e3;
  outline-offset: -1px;
}
.login-card button {
  margin-top: 4px;
  padding: 10px;
  background: #0071e3;
  color: #ffffff;
  border: none;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 600;
  cursor: pointer;
}
.login-card button:hover { background: #0064c8; }
.login-card .error {
  margin: 0 0 12px 0;
  padding: 8px 10px;
  background: #ffe5e5;
  color: #b00020;
  border-radius: 6px;
  font-size: 13px;
  text-align: center;
}
```

- [ ] **Step 2: Smoke-render**

Run: `cd /Users/bklimczak/Projects/darek && go test ./cmd/darek/serve/ -run TestLoginGet_Renders -v`
Expected: PASS (already covered by Task 4; this just confirms nothing regressed).

- [ ] **Step 3: Commit**

```bash
git add cmd/darek/serve/static/style.css
git commit -m "style(serve): add login card styling"
```

---

## Task 7 — `runServe` resolves auth env vars

**Files:**
- Modify: `cmd/darek/serve.go`

Wire config to `serve.New`. Resolve `cfg.Auth.UsernameEnv`, `PasswordHashEnv`, `SessionKeyEnv` via `config.ResolveSecret`. `SessionKey` is hex-encoded in the env var; decode to bytes. Fail the entire `runServe` if any value is missing or the key is too short — better than booting an open service.

- [ ] **Step 1: Read existing structure to find the `serve.New` call**

Run: `cd /Users/bklimczak/Projects/darek && grep -n "serve.New\|cfg.Auth" cmd/darek/serve.go`
Expected: identify where to insert the auth resolve + where to pass the result.

- [ ] **Step 2: Add resolve block before `serve.New`**

In `cmd/darek/serve.go`, near the top of `runServe` (after `cfg, err := config.Load(cfgPath)`), but BEFORE `serve.New` is called, add:

```go
authUsername, err := config.ResolveSecret("env:" + cfg.Auth.UsernameEnv)
if err != nil {
	return fmt.Errorf("auth username: %w (set Auth.UsernameEnv in config and the env var in secrets)", err)
}
authHash, err := config.ResolveSecret("env:" + cfg.Auth.PasswordHashEnv)
if err != nil {
	return fmt.Errorf("auth password hash: %w (run `darek auth hash <password>` and set %s)", err, cfg.Auth.PasswordHashEnv)
}
sessionKeyHex, err := config.ResolveSecret("env:" + cfg.Auth.SessionKeyEnv)
if err != nil {
	return fmt.Errorf("auth session key: %w (set %s to `openssl rand -hex 32`)", err, cfg.Auth.SessionKeyEnv)
}
sessionKey, err := hex.DecodeString(sessionKeyHex)
if err != nil {
	return fmt.Errorf("auth session key: not valid hex: %w", err)
}
authCfg, err := serve.NewAuthConfig(authUsername, []byte(authHash), sessionKey, cfg.Auth.SessionTTL)
if err != nil {
	return err
}
```

Add `"encoding/hex"` to the import block.

Then update the `serve.New(...)` call to pass `authCfg` as the new last argument.

- [ ] **Step 3: Build to verify**

Run: `cd /Users/bklimczak/Projects/darek && go build ./...`
Expected: clean.

- [ ] **Step 4: Smoke test — fail on missing env**

Run: `cd /Users/bklimczak/Projects/darek && DAREK_AUTH_USERNAME= DAREK_AUTH_PASSWORD_HASH= DAREK_SESSION_KEY= ./darek serve 2>&1 | head -3`
Expected: error message mentioning auth.

(Don't expect this to actually start serve — the test is that it fails fast. If `DAREK_POSTGRES_URL` etc. fail first, that's also fine; the auth check is reached only after config.Load. The commit-time guarantee is the build is clean.)

- [ ] **Step 5: Commit**

```bash
git add cmd/darek/serve.go
git commit -m "feat(serve): resolve auth env vars at startup, fail fast on missing"
```

---

## Task 8 — K8s manifest updates

**Files:**
- Modify: `/Users/bklimczak/Projects/homelab-k8s/values/darek.yaml`

Add the `auth` block to the inline `config:` so the deployed pod knows which env var names to read. The actual values come from Vault → K8s Secret → pod env via the existing `envFrom` plumbing.

This task is in the *homelab-k8s* repo, not the *darek* repo. Same author, but different working tree.

- [ ] **Step 1: Add auth block to the inline config**

In `/Users/bklimczak/Projects/homelab-k8s/values/darek.yaml`, inside the `config: |` block, append (anywhere after the existing top-level entries):

```yaml
  auth:
    username_env: DAREK_AUTH_USERNAME
    password_hash_env: DAREK_AUTH_PASSWORD_HASH
    session_key_env: DAREK_SESSION_KEY
    session_ttl: 720h
```

Also update the top-of-file comment that lists Vault fields to include the three new ones (`DAREK_AUTH_USERNAME`, `DAREK_AUTH_PASSWORD_HASH`, `DAREK_SESSION_KEY`).

- [ ] **Step 2: Re-render the chart**

Run: `cd /Users/bklimczak/Projects/homelab-k8s && helm template darek ./helm/darek -f values/darek.yaml > /tmp/darek-render.yaml && grep -A3 'auth:' /tmp/darek-render.yaml | head -10`
Expected: the rendered ConfigMap contains the new `auth:` block.

- [ ] **Step 3: Commit**

```bash
cd /Users/bklimczak/Projects/homelab-k8s
git add values/darek.yaml
git commit -m "feat(darek): wire auth env vars in deployed config

Add auth block referencing DAREK_AUTH_USERNAME, DAREK_AUTH_PASSWORD_HASH,
and DAREK_SESSION_KEY. Vault key 'darek' must include these three fields
before next deploy."
```

---

## Task 9 — Final verification

- [ ] **Step 1: Run all tests in the darek repo**

Run: `cd /Users/bklimczak/Projects/darek && make test`
Expected: all packages pass.

- [ ] **Step 2: Lint**

Run: `cd /Users/bklimczak/Projects/darek && make lint`
Expected: clean.

- [ ] **Step 3: Integration tests**

Run: `cd /Users/bklimczak/Projects/darek && make test-integration`
Expected: pass.

- [ ] **Step 4: Build the production image**

Run:
```bash
cd /Users/bklimczak/Projects/darek
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t bartlomiejklimczak/darek:v0.2.0 \
  --push .
```
Expected: multi-arch manifest pushed.

- [ ] **Step 5: Manual operator checklist**

Document this in the implementation summary; not automated.

1. Generate hash and session key locally:
   ```bash
   ./darek auth hash MyPassword123
   openssl rand -hex 32
   ```
2. Add three fields to Vault under `secret/data/darek`:
   - `DAREK_AUTH_USERNAME=bartek`
   - `DAREK_AUTH_PASSWORD_HASH=<bcrypt hash from step 1>`
   - `DAREK_SESSION_KEY=<hex from openssl>`
3. Bump `image.tag` in `values/darek.yaml` to `v0.2.0`.
4. `terraform apply -target=helm_release.darek`.
5. Wait for pod ready: `kubectl -n darek get pods -w`.
6. Verify auth works:
   - `curl -I https://darek.klimczak.xyz/` → 303 with `Location: /login?next=...`
   - Visit https://darek.klimczak.xyz in browser → login form.
   - Submit correct credentials → land on RSS inbox.
   - Submit wrong password → red banner "Invalid login or password".
   - Visit a deep link without cookie → bounce to /login, then after login go to deep link.
   - `POST /logout` (via curl with cookie) → cookie cleared, next request bounces to /login.
