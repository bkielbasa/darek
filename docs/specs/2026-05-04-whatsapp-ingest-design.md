# Darek — WhatsApp connection + group message ingest (design)

**Date:** 2026-05-04
**Status:** approved (awaiting implementation plan)
**Author:** brainstormed with Claude

## 0. Position in the larger plan

This is **sub-project A** of three. The full WhatsApp feature ("read groups, summarize them, respond from the agent") is decomposed into:

- **A. Connection + ingest** *(this spec)* — pair via QR, persist a long-running connection inside `darek serve`, store opted-in group messages in Postgres.
- **B. Summarize tool** *(separate spec, after A lands)* — `whatsapp.summarize_group(group, since?)` agent tool over stored messages.
- **C. Send tool** *(separate spec, after A and B)* — `whatsapp.send(group, text)` with the same `y/N` confirmation pattern as `mail.send`.

A is the prerequisite for B and C and produces shippable value on its own (a queryable message log).

## 1. Goal

Add a WhatsApp connection layer to `darek serve`. The user pairs their phone once via a QR code in the inbox web UI, picks which groups they want tracked, and from then on every text message in those groups (plus media-as-placeholder) lands in a Postgres table. No agent tool yet, no automatic replies, no media bytes.

## 2. Scope

### In

- New `tools/whatsapp/` package wrapping `go.mau.fi/whatsmeow` — connection lifecycle, pairing state, message-event handler, store writes.
- New HTTP routes in `cmd/darek/serve/`: `GET /whatsapp` (state-conditional UI), `GET /whatsapp/qr.png`, `POST /whatsapp/groups/{jid}/toggle`, `POST /whatsapp/groups/refresh`, `POST /whatsapp/unpair`. All protected by the existing `requireAuth` middleware.
- Two new Postgres tables (`whatsapp_groups`, `whatsapp_messages`) via migration `0006_whatsapp.up.sql`.
- `whatsmeow` session/device data persisted to a SQLite file at `~/.darek/whatsapp/store.db`. We treat this as opaque library state; we never write to it directly.
- One new config block (`whatsapp.enabled`, `whatsapp.store_path`).
- Two new metrics: `darek.whatsapp.messages_ingested{kind, outcome}` counter, `darek.whatsapp.connected` gauge.

### Out (deferred — separate plans or YAGNI)

- Agent tools (sub-projects B and C).
- Direct messages (1:1 chats). Groups only.
- Media payload download / transcription (only `[image]` / `[voice 12s]` placeholders are stored).
- Reactions, edits, deletes, message-reply context.
- Historical backfill — only forward-from-now ingestion.
- Per-group retention / pruning.
- Multiple WhatsApp accounts.
- At-rest encryption of stored messages.

## 3. Architecture

```
cmd/darek/serve.go ──► whatsapp.NewManager(...) ──► goroutine running m.Run(ctx)
                                                       │
                                                       ▼
                                          whatsmeow.Client ── socket ── WhatsApp
                                                       │
                                          events.Message handler
                                                       │
                                                       ▼
                                          whatsapp.Store (Postgres)
```

Pieces:

- **`tools/whatsapp/manager.go`** — owns the `*whatsmeow.Client`, the SQLite session store, and the event subscription. Exposes `Run(ctx)`, `Close()`, `PairingState()`, `Groups(ctx)`, `RefreshGroups(ctx)`, `SetIngestEnabled(jid, on)`, `Unpair(ctx)`, `IsConnected()`.
- **`tools/whatsapp/store.go`** — Postgres CRUD over the new tables. Pure functions over a `*pgxpool.Pool` (or wrapped equivalent), mirrors the layout of `tools/mail/store.go`.
- **`tools/whatsapp/pairing.go`** — small state machine around the QR code so the UI can render a sensible status. Pulled out of `manager.go` to keep that file focused.
- **`cmd/darek/serve/whatsapp.go`** — HTTP handlers + template wiring. Same pattern as the existing inbox handlers.
- **`cmd/darek/serve/templates/whatsapp.html`** + a small partial for a single group row.

The HTTP layer never imports `whatsmeow` directly. It talks to `*whatsapp.Manager` only, through the methods listed above. That keeps the third-party API surface contained.

## 4. Configuration

YAML block in `~/.darek/config.yaml`:

```yaml
whatsapp:
  enabled: true
  store_path: ~/.darek/whatsapp/store.db   # whatsmeow session/device sqlite (auto-created)
```

No env vars — pairing happens via QR, not a token. If `enabled: false` (or the block is absent), the WhatsApp subsystem is not constructed at all and the `/whatsapp` route returns 404. This makes the feature easy to flip off if Meta starts to crack down on the user's account.

## 5. Database schema

Migration `db/migrations/0006_whatsapp.up.sql`:

```sql
CREATE TABLE whatsapp_groups (
    jid             text PRIMARY KEY,
    name            text NOT NULL,
    ingest_enabled  boolean NOT NULL DEFAULT false,
    last_synced_at  timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE whatsapp_messages (
    id           text PRIMARY KEY,
    group_jid    text NOT NULL REFERENCES whatsapp_groups(jid) ON DELETE CASCADE,
    sender_jid   text NOT NULL,
    sender_name  text NOT NULL,
    kind         text NOT NULL,
    body         text NOT NULL,
    sent_at      timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_whatsapp_messages_group_sent ON whatsapp_messages(group_jid, sent_at DESC);
```

- `id` is whatsmeow's stanza ID — globally unique enough for our purposes. Insertion uses `ON CONFLICT (id) DO NOTHING` so reconnect-driven duplicate deliveries are silently absorbed.
- `kind` is a soft enum (`'text' | 'image' | 'voice' | 'video' | 'document' | 'sticker' | 'audio' | 'other'`). Kept as `text` rather than a Postgres enum so adding a new kind doesn't require a migration.
- `body` is the placeholder string for non-text messages (e.g. `"[image]"`, `"[image] caption goes here"`, `"[voice 12s]"`). Sufficient for sub-project B's summary prompt.
- The `(group_jid, sent_at DESC)` index makes "give me the last 200 messages from this group" cheap, which is the access pattern sub-project B will use.

`UpsertGroup` semantics: on conflict, update `name` and `last_synced_at` but **preserve** `ingest_enabled`. A user opt-in must not be silently undone by a metadata refresh.

`SetIngestEnabled(jid, on)` is a one-line UPDATE, only touches `ingest_enabled`.

There is no `down` migration in scope; the project's existing migrate setup handles that uniformly.

## 6. Manager API

```go
package whatsapp

type Manager struct { /* unexported */ }

type Options struct {
    StorePath string          // SQLite path for whatsmeow's session
    Pool      *pgxpool.Pool   // existing darek pool
    Logger    waLog.Logger    // optional; falls back to a stderr-prefixed logger
}

func NewManager(opts Options) (*Manager, error)

// Run blocks until ctx is canceled. It connects to whatsmeow, registers the
// event handler, and orchestrates the QR pairing flow on first run.
func (m *Manager) Run(ctx context.Context) error

// Close tears down the whatsmeow connection. Safe to call after Run returns.
func (m *Manager) Close() error

// PairingState returns the current state for the UI.
type PairingState struct {
    Paired       bool
    Connected    bool
    QRCode       string   // empty if Paired or pre-connect
    QRRotatedAt  time.Time
    DeviceName   string   // e.g. "Bart's iPhone" — only when Paired
    PhoneE164    string   // best-effort, may be ""
}
func (m *Manager) PairingState() PairingState

// Groups returns the live groups list (from whatsmeow's contact store), with
// our ingest_enabled and message-count overlay.
type Group struct {
    JID            string
    Name           string
    IngestEnabled  bool
    MessageCount   int
    LastMessageAt  *time.Time
}

// The HTTP handler converts each Group to a view-model with a precomputed
// "5m" / "3h" / "2d" string in LastMessageAtRel using the existing relTime()
// helper from handlers.go (see Link inbox view for the pattern).
func (m *Manager) Groups(ctx context.Context) ([]Group, error)

// RefreshGroups asks whatsmeow for the current participant list and upserts
// our whatsapp_groups table. Existing ingest_enabled flags are preserved.
func (m *Manager) RefreshGroups(ctx context.Context) error

// SetIngestEnabled flips a group's flag without touching anything else.
func (m *Manager) SetIngestEnabled(ctx context.Context, jid string, on bool) error

// Unpair logs the device out of whatsmeow's store and deletes the SQLite file.
// Postgres data (whatsapp_messages, whatsapp_groups) is preserved.
func (m *Manager) Unpair(ctx context.Context) error
```

## 7. Message ingestion behavior

Single event handler:

```go
client.AddEventHandler(func(evt any) {
    msg, ok := evt.(*events.Message)
    if !ok { return }
    if msg.Info.Chat.Server != types.GroupServer { return }   // groups only
    m.handleGroupMessage(ctx, msg)
})
```

In `handleGroupMessage`:

1. Lookup `whatsapp_groups.ingest_enabled` for `msg.Info.Chat.String()`. If the group isn't in our table yet, insert with `ingest_enabled=false` and **drop** the message — the user must opt in via the UI before any data is stored.
2. If `ingest_enabled=false`, drop silently.
3. Decode `(kind, body)` via `decodeMessage(*events.Message) (string, string)`:
   - `Conversation` or `ExtendedTextMessage` → `("text", text)`
   - `ImageMessage` → `("image", "[image]" + maybeCaption)`
   - `VideoMessage` → `("video", "[video]" + maybeCaption)`
   - `AudioMessage{PTT: true}` → `("voice", fmt.Sprintf("[voice %ds]", durSec))`
   - `AudioMessage{PTT: false}` → `("audio", "[audio]")`
   - `DocumentMessage` → `("document", "[document: " + filename + "]")`
   - `StickerMessage` → `("sticker", "[sticker]")`
   - anything else → `("other", "[unsupported message type]")`
4. `INSERT ... ON CONFLICT (id) DO NOTHING`.
5. Increment `darek.whatsapp.messages_ingested{kind, outcome="ok"}` (or `"error"` on insert failure, `"skipped"` for opt-out drops if we ever want that signal — start without; YAGNI).

Edge cases — explicitly dropped:
- Direct messages (`s.whatsapp.net` server, not group).
- Reactions, edits, deletes — whatsmeow surfaces these as separate event types we don't subscribe to.
- View-once / disappearing → treated as their underlying type. The disappearance is not honored on our side; the row stays.
- System messages (group join/leave, name change, etc.).
- Reply-quoted context — the `body` is just the reply text; the quoted message is not preserved.

## 8. UI

`GET /whatsapp` — single template, two states.

**State A (not paired, or pairing in progress):**

```html
<h1>WhatsApp pairing</h1>
<p>Open WhatsApp on your phone → Settings → Linked devices →
   Link a device, and scan this code:</p>

<img class="qr" src="/whatsapp/qr.png"
     hx-get="/whatsapp/qr.png" hx-trigger="every 20s" hx-swap="outerHTML">

<p class="hint">refreshes every 20 seconds</p>
```

`GET /whatsapp/qr.png`:
- If `PairingState().Paired`, returns 204 with header `HX-Redirect: /whatsapp` so HTMX swaps the whole page to state B.
- Otherwise renders the current QR string as a 256×256 PNG via `github.com/skip2/go-qrcode`.

**State B (paired):**

```html
<h1>WhatsApp</h1>
<p>Connected as <b>{{.DeviceName}}</b> {{if .PhoneE164}}({{.PhoneE164}}){{end}}
   <form action="/whatsapp/unpair" method="post" class="inline">
     <button type="submit" class="danger">Unpair</button>
   </form>
</p>

<h2>Groups ({{len .Groups}}) <form action="/whatsapp/groups/refresh" method="post" class="inline">
  <button type="submit">Refresh</button>
</form></h2>

<table>
  {{range .Groups}}{{template "_whatsapp_group_row.html" .}}{{end}}
</table>
```

Each row partial:

```html
<tr id="wa-row-{{.JID}}">
  <td>
    <form hx-post="/whatsapp/groups/{{.JID}}/toggle"
          hx-target="#wa-row-{{.JID}}" hx-swap="outerHTML">
      <input type="checkbox" name="enabled" value="1"
             {{if .IngestEnabled}}checked{{end}}
             hx-trigger="change" hx-include="closest form">
    </form>
  </td>
  <td>{{.Name}}</td>
  <td class="meta">
    {{if .IngestEnabled}}
      {{.MessageCount}} stored
      {{if .LastMessageAtRel}}— last {{.LastMessageAtRel}} ago{{end}}
    {{else}}—{{end}}
  </td>
</tr>
```

Handlers:

- `POST /whatsapp/groups/{jid}/toggle` — calls `SetIngestEnabled` with `enabled` form value (treated as boolean). Returns the swapped row partial. Note: HTMX includes the form value, so an unchecked box doesn't post `enabled` and we treat absence as `false`.
- `POST /whatsapp/groups/refresh` — calls `RefreshGroups`, returns the entire groups table re-rendered (full state-B page is fine for v1; HTMX out-of-band swaps are overkill).
- `POST /whatsapp/unpair` — calls `Unpair`, redirects to `/whatsapp` (which now renders state A).

CSS additions in `style.css` (~15 lines): center the `.qr` image, group-row checkbox alignment, `.danger` button styling. Reuses existing `.inline` and `.meta` classes from the inbox.

## 9. Lifecycle integration in `darek serve`

In `cmd/darek/serve.go`, after the existing analyzer wiring:

```go
var waManager *whatsapp.Manager
if cfg.WhatsApp.Enabled {
    var err error
    waManager, err = whatsapp.NewManager(whatsapp.Options{
        StorePath: cfg.WhatsApp.StorePath,
        Pool:      pool,
    })
    if err != nil {
        return fmt.Errorf("whatsapp manager: %w", err)
    }
    go func() {
        if err := waManager.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
            fmt.Fprintf(os.Stderr, "whatsapp: %v\n", err)
        }
    }()
}
```

`serve.New` accepts an additional `*whatsapp.Manager` argument (nil-safe). Handlers register only if the manager is non-nil; otherwise `/whatsapp` returns 404.

On `ctx` cancellation (Ctrl-C), `Run` returns and the deferred `waManager.Close()` cleans up.

## 10. Observability

Two new instruments (`obs/metrics.go`):

```go
WhatsAppMessages metric.Int64Counter   // darek.whatsapp.messages_ingested
                                        // labels: kind, outcome
WhatsAppConnected metric.Int64Gauge     // darek.whatsapp.connected (0|1)
```

Cardinality: `kind` is one of the 8 fixed values from §7; `outcome` is `ok` or `error`. Bounded.

The manager updates `WhatsAppConnected` whenever the underlying socket connects/disconnects (whatsmeow emits `events.Connected` / `events.Disconnected`). It does not record a histogram of per-message latency — adds complexity for marginal value.

No new `dep` entry in `obs/cardinality_test.go` since whatsmeow's network calls don't go through `obs.Dep`.

## 11. Testing

Unit (`go test ./tools/whatsapp/`, no integration tag — pure-Go):
- `decodeMessage` table tests covering each `kind` from §7.
- `pairing.go` state machine: pending → qr-rotated → paired → connected.
- `Group` view-model assembly (mock store).

Integration (`go test -tags integration ./tools/whatsapp/`):
- `Store` CRUD against a pg testcontainer (mirrors `links/store_test.go` pattern). Specifically tests:
  - `UpsertGroup` preserves existing `ingest_enabled`.
  - `InsertMessage` is idempotent on duplicate `id`.
  - `RecentMessages` ordering and limit.
  - Foreign-key cascade: deleting a group removes its messages.

HTTP handler tests (`cmd/darek/serve/whatsapp_test.go`, no integration tag):
- `GET /whatsapp` with a stub manager → renders the right state.
- `POST /whatsapp/groups/{jid}/toggle` flips and returns the partial.
- `requireAuth` redirects unauth'd to `/login` (regression).

No live whatsmeow tests in CI. Manual smoke procedure documented in the plan's Task 9 (start serve, scan QR, toggle a group, send a phone-side message, confirm a row).

## 12. Risks

- **Account ban.** Read-only ingest in a personal account is the lowest-risk WhatsApp automation pattern. Mitigations: low message volume (zero outbound in v1), pin a known-good `whatsmeow` version, easy off-switch via config.
- **Whatsmeow protocol breakage.** WhatsApp updates the multi-device protocol periodically. Reconnect failures are surfaced in the UI ("connection down — see logs"). The `darek serve` process keeps running so the rest of darek isn't affected.
- **SQLite + Postgres dual storage.** Pragmatic — keeps whatsmeow's many internal tables out of our app schema. Re-pairing rebuilds the SQLite if it's corrupted.
- **Volume.** Bounded by user opt-in. Index supports >100K messages comfortably.
- **Spoofable sender names.** Both `sender_jid` (the phone number, stable) and `sender_name` (push name, user-controlled) are stored. The future summarize tool can decide which to surface.
- **No backfill.** Documented; a future plan can add whatsmeow's history-sync flow.
- **Cleartext storage.** Postgres rows are unencrypted. Existing DB-level access controls apply.

## 13. Out of scope (recap)

- `whatsapp.summarize_group` agent tool → sub-project B.
- `whatsapp.send` agent tool → sub-project C.
- DMs, media payloads, reactions, edits, deletes, replies-with-context.
- Historical backfill.
- Retention / pruning.
- Multi-account.
- At-rest encryption.
