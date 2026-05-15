-- Persist enough of the source RSS entry to regenerate a single draft from
-- scratch without re-fetching the feed (which may no longer carry old posts).
-- Columns are nullable; rows scheduled before this migration return a clear
-- "no entry meta captured" error from regenerate until they are re-scheduled.
ALTER TABLE blog_posts_scheduled
    ADD COLUMN entry_url     text,
    ADD COLUMN entry_title   text,
    ADD COLUMN entry_summary text;
