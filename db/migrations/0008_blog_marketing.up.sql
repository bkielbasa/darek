CREATE TABLE blog_posts_scheduled (
    canonical_url    text        PRIMARY KEY,
    published_at     timestamptz NOT NULL,
    scheduled_at     timestamptz,
    todoist_task_ids text[],
    created_at       timestamptz NOT NULL DEFAULT now()
);

-- Helps Count() and "is this our first poll?" stay fast as the table grows.
CREATE INDEX blog_posts_scheduled_created_at ON blog_posts_scheduled (created_at DESC);
