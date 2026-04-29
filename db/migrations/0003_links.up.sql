CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE links (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    url         text NOT NULL,
    title       text,
    rating      smallint,
    tags        text[] NOT NULL DEFAULT '{}',
    notes       text,
    source      text NOT NULL DEFAULT 'user',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    embedding   vector(1536),
    search      tsvector GENERATED ALWAYS AS (
        to_tsvector('simple'::regconfig,
            coalesce(title, '') || ' ' ||
            coalesce(notes, '') || ' ' ||
            immutable_array_to_string(tags, ' ') || ' ' ||
            url
        )
    ) STORED,
    CONSTRAINT links_rating_range CHECK (rating IS NULL OR (rating >= 1 AND rating <= 5))
);

CREATE UNIQUE INDEX links_url ON links (url);
CREATE INDEX links_search_gin ON links USING gin(search);
CREATE INDEX links_tags_gin ON links USING gin(tags);
CREATE INDEX links_rating_desc ON links (rating DESC NULLS LAST, created_at DESC);
