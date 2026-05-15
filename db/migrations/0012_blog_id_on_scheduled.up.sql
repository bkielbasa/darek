-- Multi-blog support. Every campaign now belongs to a named blog (the id from
-- blog_marketing.feeds[].id in config); first-run / backfill is detected
-- per-blog so adding a new blog to the config later doesn't spawn campaigns
-- for its existing back-catalog of posts.
ALTER TABLE blog_posts_scheduled ADD COLUMN blog_id text NOT NULL;

CREATE INDEX blog_posts_scheduled_blog_id ON blog_posts_scheduled (blog_id);
