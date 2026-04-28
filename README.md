# darek

A Go personal-assistant CLI. Talks to OpenAI, remembers things in Postgres, fully observable.
First plan ships **memory only** — calendars, Todoist, mail land in subsequent plans.

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

## Layout

```
cmd/darek/      CLI entry
agent/          tool-calling loop
llm/            OpenAI wrapper + cost calc
tools/          tool interface + registry
tools/calendar/ CalendarSource interface + Google + iCal sources
tools/todoist/  Todoist REST client + tools
tools/mail/     MailAccount interface, IMAP sync, mail tools
memory/         Postgres-backed notes + recall/save tools
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

## Calendars

Calendars are read-only. Add sources to `~/.darek/config.yaml`:

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

## Todoist

Set `todoist.token_env` in `~/.darek/config.yaml`:

```yaml
todoist:
  token_env: DAREK_TODOIST_TOKEN
```

Get a token from <https://todoist.com/app/settings/integrations/developer>. Add it to `~/.darek/secrets.env`. Tools enabled: `todoist.list_tasks`, `todoist.create_task`, `todoist.complete_task`, `todoist.update_task`.

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

Tools enabled in chat: `mail.search`, `mail.get_body`, `mail.get_attachment`. Sending mail is in Plan 5.

## Roadmap

- Plan 2: Calendars (Google + iCal)
- Plan 3: Todoist (read + write)
- Plan 4: Mail receive (IMAP sync, search, body/attachment fetch)
- Plan 5: Mail send (confirm-before-send, IMAP APPEND to Sent)
