ALTER TABLE links ADD COLUMN kind text NOT NULL DEFAULT 'article';
ALTER TABLE links ADD COLUMN feed text;
ALTER TABLE links ADD CONSTRAINT links_kind_check
    CHECK (kind IN ('article','video','tweet','podcast','other'));
CREATE INDEX links_kind ON links (kind);
CREATE INDEX links_feed ON links (feed) WHERE feed IS NOT NULL;
