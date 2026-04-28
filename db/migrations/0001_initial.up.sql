CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- array_to_string is only STABLE; wrap it so the generated column expression is IMMUTABLE.
CREATE OR REPLACE FUNCTION immutable_array_to_string(arr text[], sep text)
RETURNS text LANGUAGE sql IMMUTABLE AS $$
    SELECT array_to_string(arr, sep)
$$;

CREATE TABLE notes (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    body        text NOT NULL,
    tags        text[] NOT NULL DEFAULT '{}',
    source      text NOT NULL DEFAULT 'user',
    search      tsvector GENERATED ALWAYS AS (
        to_tsvector('simple'::regconfig, coalesce(body, '') || ' ' || coalesce(immutable_array_to_string(tags, ' '), ''))
    ) STORED
);
CREATE INDEX notes_search_gin ON notes USING gin(search);
CREATE INDEX notes_tags_gin   ON notes USING gin(tags);

CREATE TABLE turns (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    started_at    timestamptz NOT NULL DEFAULT now(),
    ended_at      timestamptz,
    user_input    text NOT NULL,
    final_output  text,
    trace_id      text,
    iterations    integer NOT NULL DEFAULT 0,
    input_tokens  integer NOT NULL DEFAULT 0,
    output_tokens integer NOT NULL DEFAULT 0,
    cost_usd      numeric(10,6) NOT NULL DEFAULT 0
);
CREATE INDEX turns_started_at ON turns (started_at DESC);

CREATE TABLE messages (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    turn_id     uuid NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    ord         integer NOT NULL,
    role        text NOT NULL,
    content     text,
    tool_name   text,
    tool_args   jsonb,
    tool_result text,
    UNIQUE (turn_id, ord)
);
CREATE INDEX messages_turn_id ON messages (turn_id);
