CREATE TABLE memories (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    kind         text NOT NULL,
    summary      text NOT NULL,
    details      text NOT NULL DEFAULT '',
    people       text[] NOT NULL DEFAULT '{}',
    groups       text[] NOT NULL DEFAULT '{}',
    due_at       timestamptz,
    status       text NOT NULL DEFAULT 'active',
    dedupe_key   text NOT NULL,
    source       text NOT NULL DEFAULT 'digest',
    source_refs  text[] NOT NULL DEFAULT '{}',
    search       tsvector GENERATED ALWAYS AS (
        to_tsvector('simple'::regconfig,
            summary || ' ' || details || ' ' ||
            immutable_array_to_string(people, ' ') || ' ' ||
            immutable_array_to_string(groups, ' '))
    ) STORED
);

CREATE UNIQUE INDEX memories_dedupe_key_uniq ON memories (dedupe_key);
CREATE INDEX memories_search_gin  ON memories USING gin(search);
CREATE INDEX memories_people_gin  ON memories USING gin(people);
CREATE INDEX memories_active_due  ON memories (due_at) WHERE status = 'active';
CREATE INDEX memories_kind_status ON memories (kind, status, last_seen_at DESC);

ALTER TABLE whatsapp_messages
    ADD COLUMN memories_extracted_at timestamptz;

CREATE INDEX idx_whatsapp_messages_unextracted
    ON whatsapp_messages (group_jid, sent_at)
    WHERE memories_extracted_at IS NULL;
