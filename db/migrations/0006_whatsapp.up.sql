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
