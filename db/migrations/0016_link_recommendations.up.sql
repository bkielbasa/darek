-- Link recommendations: track which links still need an embedding, index the
-- vectors for fast cosine search, and log every suggestion so we never repeat.
ALTER TABLE links ADD COLUMN embedded_at timestamptz;

CREATE INDEX links_embedding_hnsw
    ON links USING hnsw (embedding vector_cosine_ops);

CREATE INDEX links_analyzed_pending
    ON links (kind, created_at DESC)
    WHERE analyzed_at IS NULL;

CREATE TABLE link_recommendations (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id    uuid NOT NULL REFERENCES links(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    score      double precision NOT NULL DEFAULT 0,
    reason     text NOT NULL DEFAULT '',
    surface    text NOT NULL DEFAULT 'digest',
    model      text NOT NULL DEFAULT '',
    UNIQUE (link_id)
);

CREATE INDEX link_recommendations_recent
    ON link_recommendations (created_at DESC);
