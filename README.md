# darek

A Go personal-assistant CLI. Talks to OpenAI, remembers things in Postgres, integrates with Google Calendar / iCal feeds / Todoist / FreshRSS / IMAP+SMTP, and is fully observable (OTEL traces + Prom metrics).

The agent is invoked via `./darek "your prompt"` and can call any of the tools below. Most data sources also have direct CLI subcommands so they can be driven from cron without an LLM in the loop.

## Quickstart

```bash
# 1. Spin up Postgres + observability stack
make up
make obs-up

# 2. Configure
mkdir -p ~/.darek
cp config/testdata/config.example.yaml ~/.darek/config.yaml
cat > ~/.darek/secrets.env <<'EOF'
DAREK_OPENAI_API_KEY=sk-...
DAREK_POSTGRES_URL=postgres://darek:darek@localhost:5432/darek?sslmode=disable
EOF
chmod 600 ~/.darek/secrets.env

# 3. Build & migrate
make build
set -a; source ~/.darek/secrets.env; set +a
./darek migrate

# 4. Health check
./darek doctor

# 5. Talk to it
./darek "remember I'm tracking a Berlin trip in May"
./darek "what trips am I tracking?"
```

Open Grafana at <http://localhost:3000> (anonymous admin) and look at the `darek` folder.
Open Jaeger at <http://localhost:16686>.

## Subcommand reference

| Command | What it does |
|---|---|
| `darek` *(or `darek chat`)* | Default. Send a prompt to the agent; it picks tools as needed. |
| `darek "<prompt>"` | One-shot agent run. |
| `darek migrate` | Apply embedded SQL migrations to Postgres. |
| `darek doctor` | Health check (Postgres, OTEL, OpenAI, configured sources). |
| `darek blog sync` | Poll the configured blog RSS feed; for new posts, create 9 marketing tasks in Todoist (X / Mastodon / LinkedIn × launch / +2w / +3mo). |
| `darek serve` | HTTP server on `127.0.0.1:7777` (RSS inbox UI + background pollers). |
| `darek calendar refresh-token <nickname>` | Run the Google OAuth flow for one configured Google calendar. |
| `darek calendar daily-digest` | Send a 3-day calendar digest email (today + 2 days). |
| `darek mail sync [--account=<nickname>]` | Pull new envelopes from IMAP into Postgres. |
| `darek freshrss sync` | Pull unread FreshRSS articles into the local link store. |
| `darek todoist sync` | Import any #Inbox tasks containing URLs and complete them in Todoist. |

## Agent tools

These are the tools the agent can call inside `darek chat`. Each is gated on relevant config being present.

**Memory**
- `memory.save(text, tags?)` — write a note.
- `memory.recall(query)` — search prior notes.

**Links (taste graph)**
- `links.save(url, rating?, tags?, notes?)` — save/update by URL; tags merge by default.
- `links.search(query?, min_rating?, tags?, since?)` — full-text + filters.
- `links.similar(text)` — finds your rated links most similar to a piece of text.

**Calendar**
- `calendar.list_events(from?, to?, calendar?)` — list events; empty `calendar` = all sources.
- `calendar.create_event(calendar, summary, start, end?, all_day?, description?, location?, attendees?, send_invites?)` — create an event on a writable (Google) calendar. Returns the new event's UID.
- `calendar.update_event(calendar, uid, …)` — PATCH semantics: only fields present are changed. `attendees: []` clears the list; `attendees` absent leaves it. Operates on the instance UID returned by `list_events`.
- `calendar.delete_event(calendar, uid, send_invites?)` — delete by UID. iCal sources are read-only and return a `read-only` error.

**Todoist**: `todoist.list_tasks`, `todoist.create_task`, `todoist.complete_task`, `todoist.update_task`.

**FreshRSS**: `freshrss.list_articles`, `freshrss.get_article`, `freshrss.mark`.

**Mail**: `mail.search`, `mail.get_body`, `mail.get_attachment`, `mail.send` (`send` prompts on stderr for `y/N` confirmation).

**YouTube**
- `youtube.transcript(url, lang?)` — fetch a YouTube video's transcript as plain text. `lang` is optional (e.g. `"es"`); default prefers manual English, then auto-generated English, then the first available track. Returns title, channel, duration, then the transcript. Errors with `"no captions available"` when the video has no captions, or `"video not accessible..."` for private/removed/region-locked videos.

## Layout

```
cmd/darek/      CLI entry
agent/          tool-calling loop
llm/            OpenAI wrapper + cost calc
tools/          tool interface + registry
tools/calendar/ CalendarSource interface + Google + iCal sources
tools/todoist/  Todoist REST client + tools
tools/freshrss/ GReader-protocol RSS client + tools
tools/mail/     MailAccount interface, IMAP sync, mail tools
memory/         Postgres-backed notes + recall/save tools
links/          taste-graph store + save/search/similar tools
obs/            OTEL setup, metrics, redactor, slog
db/             pgx pool + embedded migrations
config/         YAML loader + secret resolver
otel/           collector, prom, grafana provisioning
```

## Make targets

- `make build` — build the CLI
- `make test` — unit tests
- `make test-integration` — run with `-tags=integration` (needs Docker)
- `make up` / `make down` — Postgres
- `make obs-up` / `make obs-down` — OTEL Collector + Jaeger + Prom + Grafana

## Links (taste graph)

Save URLs you've read with a 1–5 rating, tags, and notes. The agent uses past ratings to reason about whether you'd like new content.

Tools:
- `links.save(url, rating?, tags?, notes?)` — saves or updates by URL. Tags merge by default.
- `links.search(query?, min_rating?, tags?, since?)` — full-text + filters.
- `links.similar(text)` — finds your rated links most similar to a piece of text; the agent reads the returned ratings and notes to decide.

Example:

```
./darek "save https://research.swtch.com/gomm — Go memory model, rated 5, tags go,concurrency, notes core reading"
./darek "I'm reading 'Concurrency Patterns in Distributed Systems' — would I like it?"
```

## Calendars

Two source kinds: `ical` (read-only HTTP iCal feeds) and `google` (read + write via OAuth).

```yaml
calendars:
  - kind: ical
    nickname: family
    url: https://calendar.example.com/feed.ics
  - kind: google
    nickname: personal
    calendar_id: primary  # or a specific calendar id
    client_id_env: DAREK_GCAL_CLIENT_ID
    client_secret_env: DAREK_GCAL_CLIENT_SECRET
```

For Google calendars, run the OAuth flow once per nickname:

```bash
./darek calendar refresh-token personal
```

The CLI prints an auth URL; visit it, paste back the code, the token is saved to `~/.darek/oauth/<nickname>.json`.

The OAuth scope is `CalendarEventsScope` (read + create/update/delete events). Existing tokens minted with the older read-only scope must re-run `refresh-token` once to gain write access.

### Daily digest email

Sends an HTML+plaintext digest of all events from all configured calendars for **today + the next 2 days** (3 calendar days, local timezone) to a single recipient via one of your configured mail accounts.

```yaml
calendar_digest:
  to: bart@example.com           # required
  from_account: personal         # required: nickname from mail.accounts
  subject: "Calendar — {{date}}" # optional; default "Calendar — <YYYY-MM-DD>"
                                 # the {{date}} token expands to the window start (ISO).
```

Run it ad-hoc, or hook it up to cron / launchd:

```bash
./darek calendar daily-digest
```

The email renders as a card per day (rounded corners, calendar-name pills colored by hash, "Today" badge on the first card) with a plaintext fallback. Each event line shows time, calendar, summary, and optional location. Empty days print "Nothing scheduled" rather than being hidden, so a silent miss is debuggable.

When WhatsApp is configured (see the WhatsApp section below) and at least one group is opted in, this email also includes a per-group summary of recent WhatsApp activity below the calendar.

Exits non-zero with a clear stderr message if `to` or `from_account` is missing or `from_account` doesn't match a configured `mail.accounts[].nickname`.

## Todoist

Set `todoist` in `~/.darek/config.yaml`:

```yaml
todoist:
  token_env: DAREK_TODOIST_TOKEN
  sync_interval: 15m       # how often `darek serve` polls #Inbox; 0 disables
```

Get a token from <https://todoist.com/app/settings/integrations/developer>. Add it to `~/.darek/secrets.env`. Tools enabled in chat: `todoist.list_tasks`, `todoist.create_task`, `todoist.complete_task`, `todoist.update_task`.

### Todoist #Inbox link import

Tasks in #Inbox containing a URL are imported into the local link store and the task is completed in Todoist. Tasks without URLs are left alone. Labels merge into the link's tags.

For cron-driven sync without the server:

```bash
./darek todoist sync
```

`darek serve` polls Todoist on the configured `sync_interval` cadence (parallel to FreshRSS).

## Blog → Marketing Todoist

When the user publishes a new post on their blog, `darek` schedules a 9-task social-promotion campaign in the Todoist `Marketing` project: one task per platform (X, Mastodon, LinkedIn) at three cadences (publication day, +14 days, +90 days). Each task contains an LLM-drafted, ready-to-send post tailored to its platform and cadence.

### Configure

```yaml
blog_marketing:
  feed_url: https://blog.example.com/feed.xml
  project_name: Marketing
  sync_interval: 15m       # 0 disables the in-server loop
  post_time: "09:00"       # local time-of-day for every task
  timezone: Europe/Warsaw  # optional; default = system local
```

The Todoist project name is resolved at runtime against the Todoist projects API. Six labels are auto-created on first scheduled post: `x`, `mastodon`, `linkedin`, `launch`, `reshare-2w`, `resurface-3mo`.

### First-run safety

The very first poll inserts every existing feed entry into a local state table with no Todoist tasks created. Only posts that appear after the first poll spawn the 9-task series. Clearing the `blog_posts_scheduled` table re-triggers the same backfill on the next run.

### Run

```bash
./darek blog sync         # one-shot, cron-friendly
```

`darek serve` polls the feed every `sync_interval` (parallel to FreshRSS / Todoist).

### Failure modes

If any of the 9 task creations fails mid-series, already-created tasks are deleted (best-effort) and the post is **not** marked scheduled — next poll retries fresh. If the LLM call fails, the post is skipped for this poll and retried next time.

## FreshRSS

Set `freshrss` in `~/.darek/config.yaml`:

```yaml
freshrss:
  base_url: https://rss.example.com
  username: alice
  password_env: DAREK_FRESHRSS_PASSWORD
  sync_interval: 15m       # how often `darek serve` polls; 0 disables the in-server loop
```

Use a FreshRSS **API password** (Settings → Profile → "API password"), not your account password. Tools enabled in chat: `freshrss.list_articles`, `freshrss.get_article`, `freshrss.mark`.

### RSS inbox + web UI

`darek serve` runs a local HTTP UI at `127.0.0.1:7777` (configurable via `server.bind`) for browsing imported FreshRSS articles, rating them, and adding tags/notes. The server also polls FreshRSS every `sync_interval` and marks articles read in FreshRSS once imported.

For cron-driven sync without the server:

```bash
./darek freshrss sync
```

URL canonicalization (strip `utm_*`, `fbclid`, etc.) deduplicates the same article reaching darek through multiple sources. Each link is auto-classified (`article` / `video` / `tweet` / `podcast`) by URL heuristics; you can override the kind from the UI.

Each row has an **analyze** button that asks OpenAI to summarize the link and propose tags. Click it; the row updates in place. Tags merge into existing tags; the proposed summary overwrites whatever the source provided. Re-clicking refreshes both. The button is hidden if `openai.api_key_env` is unset.

When `darek serve` (or the standalone `darek freshrss sync` / `darek todoist sync` cron commands) ingests a new YouTube video URL, it automatically fetches the transcript and runs the analyze step against the transcript instead of the YouTube description. Summary + tags are stored on the link without manual interaction. Failures (no captions, region-locked, OpenAI unavailable) are logged but don't abort sync; the row stays ingested with no `analyzed_at` set so you can click Analyze later.

## Mail

Mail uses a hybrid sync model: envelopes (subject, from, date, snippet) are cached in Postgres, bodies and attachments are fetched live from IMAP on demand.

### Configure

```yaml
mail:
  attachments_dir: ~/.darek/attachments
  attachment_ttl_days: 30
  accounts:
    - nickname: personal
      email: me@example.com
      imap: { host: imap.fastmail.com, port: 993, tls: true }
      smtp: { host: smtp.fastmail.com, port: 465, tls: true }
      username: me@example.com
      secret_env: DAREK_MAIL_PERSONAL
      sync_folders: [INBOX]
```

Add the IMAP password (an app-specific password, NOT your account password) to `~/.darek/secrets.env`.

### Sync

Periodic sync is invoked manually (cron suggested):

```bash
./darek mail sync                   # sync all accounts
./darek mail sync --account=personal
```

Tools enabled in chat: `mail.search`, `mail.get_body`, `mail.get_attachment`, `mail.send`. Sending prompts you to confirm (`y/N`) on stderr; the message is sent via SMTP and appended to your Sent folder via IMAP.

## WhatsApp

WhatsApp integration uses the unofficial multi-device protocol via [whatsmeow](https://github.com/tulir/whatsmeow). **This violates WhatsApp's Terms of Service.** A read-only personal-account ingest carries some risk of your number being banned by Meta. You opt in by enabling the feature in config; you can disable it instantly by flipping the flag.

### Configure

```yaml
whatsapp:
  enabled: true
  store_path: ~/.darek/whatsapp/store.db   # whatsmeow session SQLite (auto-created)
```

### Pair

Start `darek serve`, open <http://127.0.0.1:7777/whatsapp>, scan the QR code from your phone (WhatsApp → Settings → Linked devices → Link a device). Once paired, the page swaps to a list of your groups.

### Pick groups

Each group has a checkbox. Toggle the ones you want tracked. Messages start landing in `whatsapp_messages` immediately for opted-in groups. Click *Refresh from WhatsApp* to pick up newly-joined groups. Click *Unpair* to log out and wipe the local session (Postgres data is preserved).

### What's stored

Text messages go in verbatim. Media (images, voice, video, documents, stickers) become short placeholders like `[image]`, `[voice 12s]`, `[document: report.pdf]` — sufficient for future summarization, no media payloads downloaded. Reactions, edits, deletes, and reply-quoted context are dropped. Direct messages are not ingested at all (groups only).

### Daily digest summary

When the daily-digest cron runs (`darek calendar daily-digest`) and at least one WhatsApp group is opted-in, the email gains a "WhatsApp" section after the calendar one. Each section lists one short LLM-generated summary per group, covering messages received since the previous digest. Only groups with new messages appear; if no group has activity since the last run, the WhatsApp section is omitted. The lookback is capped at 7 days — if `darek serve` was offline longer than that, older messages are not summarized.

The chat-side `whatsapp.summarize_group` agent tool and the `whatsapp.send` tool are separate plans; they would build on the same `Summarizer` and `Store`.

## Roadmap

All MVP plans (foundations, calendars, todoist, mail receive, mail send) plus calendar write tools and the daily-digest email have shipped. Future work:

- CalDAV / Outlook calendar sources behind the existing `CalendarSource` / `WritableCalendarSource` interfaces
- Series-level recurring edits and recurrence rules (`RRULE`) on calendar create/update
- Mail HTML body rendering and a "deep search" tool that fetches bodies for top-K candidates
- LLM-augmented digest variant that summarizes the day rather than listing it
- ActualBudget integration (deferred from MVP)
