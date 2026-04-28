CREATE TABLE mail_accounts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    nickname     text UNIQUE NOT NULL,
    email        text NOT NULL,
    imap_host    text NOT NULL,
    imap_port    integer NOT NULL,
    imap_tls     boolean NOT NULL DEFAULT true,
    smtp_host    text,
    smtp_port    integer,
    smtp_tls     boolean,
    username     text NOT NULL,
    secret_ref   text NOT NULL
);

CREATE TABLE mail_folders (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   uuid NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
    name         text NOT NULL,
    uidvalidity  bigint NOT NULL DEFAULT 0,
    last_uid     bigint NOT NULL DEFAULT 0,
    last_sync_at timestamptz,
    UNIQUE (account_id, name)
);

CREATE TABLE mail_messages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      uuid NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
    folder_id       uuid NOT NULL REFERENCES mail_folders(id) ON DELETE CASCADE,
    imap_uid        bigint NOT NULL,
    message_id      text,
    in_reply_to     text,
    "references"    text[] NOT NULL DEFAULT '{}',
    thread_key      text,
    from_addr       text,
    to_addrs        text[] NOT NULL DEFAULT '{}',
    cc_addrs        text[] NOT NULL DEFAULT '{}',
    subject         text,
    date            timestamptz,
    flags           text[] NOT NULL DEFAULT '{}',
    snippet         text,
    has_attachments boolean NOT NULL DEFAULT false,
    deleted_at      timestamptz,
    search          tsvector GENERATED ALWAYS AS (
        to_tsvector('simple'::regconfig,
            coalesce(subject,'') || ' ' ||
            coalesce(snippet,'') || ' ' ||
            coalesce(from_addr,'') || ' ' ||
            immutable_array_to_string(to_addrs, ' ') || ' ' ||
            immutable_array_to_string(cc_addrs, ' ')
        )
    ) STORED,
    UNIQUE (folder_id, imap_uid)
);
CREATE INDEX mail_messages_search_gin ON mail_messages USING gin(search);
CREATE INDEX mail_messages_account_date ON mail_messages (account_id, date DESC);
CREATE INDEX mail_messages_message_id ON mail_messages (message_id);

CREATE TABLE mail_attachments_meta (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id   uuid NOT NULL REFERENCES mail_messages(id) ON DELETE CASCADE,
    filename     text,
    content_type text,
    size_bytes   bigint NOT NULL DEFAULT 0,
    imap_part_id text NOT NULL
);

CREATE TABLE mail_pending_sends (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    account_id   uuid NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
    to_addrs     text[] NOT NULL,
    subject      text,
    body         text,
    attachments  jsonb,
    status       text NOT NULL DEFAULT 'pending'
);
