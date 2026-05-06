# Darek — iCal URL from secret env var (design)

**Date:** 2026-05-06
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 1. Goal

Allow the `url` field on iCal calendar entries to be resolved from an environment variable instead of being hardcoded in `~/.darek/config.yaml` (or in the Helm values file). Today the Todoist iCal feed URL contains a secret token in its query string and must be committed in plaintext alongside the rest of the config; this lets it move into Vault / the existing `darek-env` Secret.

## 2. Scope

### In

- New `URLEnv string \`yaml:"url_env"\`` field on `CalendarSrc`.
- New helper `config.ResolveICalURL(c CalendarSrc) (string, error)` that returns the URL whether it came from `url` or `url_env`, with input validation.
- Two call-site changes (`cmd/darek/chat.go` and `cmd/darek/daily_digest.go`) — replace the bare `c.URL` with the helper inside the existing `case "ical"` branch.
- README addition documenting `url_env`.
- Helm values change in `homelab-k8s/values/darek.yaml` to use `url_env: DAREK_TODOIST_ICAL_URL`. Vault entry add is the user's manual step (out of repo scope).

### Out (deferred — YAGNI)

- Splitting URL components (`base_url` + `token_env`). The full URL via one env var matches how `postgres.url_env` already works and avoids per-provider URL composition logic.
- Generic "any string field can be `*_env`" plumbing. Targeted at the iCal URL, where the actual need is.
- Google calendar URL secrets — Google calendars use the OAuth client ID/secret pair already stored as env-resolved fields (`client_id_env`, `client_secret_env`). No URL secret to add.

## 3. Config + resolution

### Schema

```go
type CalendarSrc struct {
    Kind            string `yaml:"kind"`
    Nickname        string `yaml:"nickname"`
    URL             string `yaml:"url"`              // literal URL (for non-secret feeds)
    URLEnv          string `yaml:"url_env"`          // env var holding the full URL (for secret feeds)
    CalendarID      string `yaml:"calendar_id"`
    ClientIDEnv     string `yaml:"client_id_env"`
    ClientSecretEnv string `yaml:"client_secret_env"`
}
```

`URL` and `URLEnv` are mutually exclusive — exactly one is required when `Kind == "ical"`. Both-set or neither-set is a configuration error.

### Helper

A new file `config/calendar.go` holds:

```go
package config

import (
    "errors"
    "fmt"
)

// ResolveICalURL returns the iCal feed URL for the given calendar source.
// Use only when c.Kind == "ical".
//
// Exactly one of c.URL / c.URLEnv must be set:
//   - c.URL  → returned verbatim.
//   - c.URLEnv → resolved via ResolveSecret("env:" + c.URLEnv).
//
// Returns an error wrapping the calendar nickname so the caller can re-wrap
// with consistent context.
func ResolveICalURL(c CalendarSrc) (string, error) {
    if c.URL != "" && c.URLEnv != "" {
        return "", errors.New("set exactly one of url / url_env")
    }
    if c.URL == "" && c.URLEnv == "" {
        return "", errors.New("set exactly one of url / url_env")
    }
    if c.URL != "" {
        return c.URL, nil
    }
    val, err := ResolveSecret("env:" + c.URLEnv)
    if err != nil {
        return "", fmt.Errorf("url_env %s: %w", c.URLEnv, err)
    }
    if val == "" {
        return "", fmt.Errorf("url_env %s: empty", c.URLEnv)
    }
    return val, nil
}
```

The helper deliberately does not validate URL syntax — `url.Parse` happens later in the iCal client. The job here is just config plumbing.

### Call sites

Both files have an identical-shaped block:

```go
for _, c := range cfg.Calendars {
    switch c.Kind {
    case "ical":
        if err := srcs.Add(ical.New(c.Nickname, c.URL)); err != nil {
            return fmt.Errorf("calendar %s: %w", c.Nickname, err)
        }
    case "google":
        // ...
    }
}
```

Become:

```go
for _, c := range cfg.Calendars {
    switch c.Kind {
    case "ical":
        u, err := config.ResolveICalURL(c)
        if err != nil {
            return fmt.Errorf("calendar %s: %w", c.Nickname, err)
        }
        if err := srcs.Add(ical.New(c.Nickname, u)); err != nil {
            return fmt.Errorf("calendar %s: %w", c.Nickname, err)
        }
    case "google":
        // ...
    }
}
```

Two files: `cmd/darek/chat.go` and `cmd/darek/daily_digest.go`. The same block exists in both because each command builds its own calendar sources independently — that duplication isn't fixed here, just propagated.

## 4. Behavior matrix

| `url` | `url_env` | env var present | result |
|---|---|---|---|
| set | empty | n/a | returns `url` verbatim |
| empty | set | yes, non-empty | returns env var value |
| empty | set | yes, empty string | error `url_env <NAME>: empty` |
| empty | set | no (unset) | error `url_env <NAME>: env var "..." is empty` (from `ResolveSecret`) |
| set | set | n/a | error `set exactly one of url / url_env` |
| empty | empty | n/a | error `set exactly one of url / url_env` |

The empty-env-var case is treated as missing config, not "intentional empty". An iCal calendar with no URL is meaningless.

## 5. Testing

Unit tests in `config/calendar_test.go`:

- `ResolveICalURL_URLOnly` — `URL` set, `URLEnv` empty → returns URL.
- `ResolveICalURL_EnvOnly_Set` — env var present and non-empty → returns its value (use `t.Setenv`).
- `ResolveICalURL_EnvOnly_Missing` — env var unset → error mentions the env-var name.
- `ResolveICalURL_EnvOnly_Empty` — env var set to empty string → error mentions `empty`.
- `ResolveICalURL_BothSet` → error.
- `ResolveICalURL_NeitherSet` → error.

Six tests, all in-process, no testcontainer, no network. Each is ~5 lines.

No changes needed in existing calendar / digest tests — they pass `URL: "https://..."` directly to `ical.New`, which is unchanged. The new helper is invoked only at the config boundary (the cmd/* files); those files don't have unit tests today and adding them is out of scope.

## 6. Documentation

### `README.md`

In the existing `## Calendars` section, the YAML example currently shows:

```yaml
calendars:
  - kind: ical
    nickname: family
    url: https://calendar.example.com/feed.ics
```

Add an alternate showing `url_env`:

```yaml
calendars:
  - kind: ical
    nickname: todoist
    url_env: DAREK_TODOIST_ICAL_URL   # full URL resolved from env at startup
```

One sentence above the example: "Use `url_env` instead of `url` when the URL contains a secret token (e.g. a Todoist iCal feed): the env var name lives in config, the value lives in your secrets store."

### `homelab-k8s/values/darek.yaml`

Replace the existing committed-secret URL:

```yaml
calendars:
  - kind: ical
    nickname: Bartek (todoist)
    url: https://ext.todoist.com/export/ical/todoist?user_id=...&ical_token=...
```

With:

```yaml
calendars:
  - kind: ical
    nickname: Bartek (todoist)
    url_env: DAREK_TODOIST_ICAL_URL
```

The `DAREK_TODOIST_ICAL_URL` Vault entry is added by the user manually (Vault management lives outside this repo). The README addition above mentions this.

## 7. Migration & deployment

1. Land the code change. Rebuild image, bump tag, push.
2. Add `DAREK_TODOIST_ICAL_URL` to Vault path `secret/data/darek` with the full Todoist iCal URL as value.
3. Update `homelab-k8s/values/darek.yaml` per §6 — switch the Bartek (todoist) entry to `url_env`.
4. `helm upgrade darek ./helm/darek -f values/darek.yaml`.
5. Both the `serve` Deployment and the `daily-digest` CronJob pick up the new value via the existing external-secrets-managed `darek-env` Secret on next pod restart / next CronJob fire.

The change is non-breaking — existing `url:` entries (e.g. any public iCal feed) keep working.

Old image versions (pre-fix) ignore `url_env` (unknown YAML field), see an empty `c.URL`, and pass the empty string to `ical.New`. The iCal client will fail at fetch time with a network error. To avoid a brief broken state during the rolling upgrade, the order matters: deploy the new image FIRST, then change the values file in a SECOND `helm upgrade`. Or do it in a single `helm upgrade` with `image.tag` and `config` both updated — the new pod's startup uses the new config.

## 8. Risks

- **Empty env var at startup.** Caught by §4 row 4 — startup fails fast with a clear error rather than silently calling `ical.New("nickname", "")`.
- **Both fields set.** Caught by §4 row 5 — explicit error rather than "URL wins" or "URLEnv wins" silently.
- **YAML parser tolerates the new field.** `gopkg.in/yaml.v3` (the project's loader) accepts unknown-tagged fields by default; old binaries ignore `url_env` rather than erroring. Documented above as the deploy ordering caveat.
- **Vault rotation.** When the user rotates the iCal token in Todoist's UI, they update the Vault entry. external-secrets propagates to the `darek-env` Secret. The serve pod doesn't re-read env vars at runtime, so a pod restart is required to pick up rotation. Same posture as every other secret in this project.

## 9. Out of scope (recap)

- URL component splitting (`base_url` + `token_env`).
- Generic `any_field_env` reflection plumbing.
- A separate Google-calendar URL field (Google secrets are already env-resolved via `client_id_env` / `client_secret_env`).
- Read-after-write reload of env vars (would require a SIGHUP or filesystem watcher; YAGNI for a homelab single-pod deployment).
