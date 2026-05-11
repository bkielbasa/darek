CREATE TABLE executions (
    id           uuid PRIMARY KEY,
    trace_id     text NOT NULL,
    span_id      text NOT NULL UNIQUE,
    kind         text NOT NULL,
    name         text NOT NULL,
    started_at   timestamptz NOT NULL,
    ended_at     timestamptz NOT NULL,
    duration_ms  bigint NOT NULL,
    status       text NOT NULL,
    error        text,
    attributes   jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX executions_started_at_idx ON executions (started_at DESC);
CREATE INDEX executions_kind_idx       ON executions (kind, started_at DESC);

CREATE TABLE execution_steps (
    id             uuid PRIMARY KEY,
    execution_id   uuid NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    parent_span_id text,
    span_id        text NOT NULL,
    name           text NOT NULL,
    started_at     timestamptz NOT NULL,
    ended_at       timestamptz NOT NULL,
    duration_ms    bigint NOT NULL,
    status         text NOT NULL,
    error          text,
    attributes     jsonb NOT NULL DEFAULT '{}'::jsonb,
    events         jsonb NOT NULL DEFAULT '[]'::jsonb
);
CREATE INDEX execution_steps_execution_id_idx ON execution_steps (execution_id, started_at);
