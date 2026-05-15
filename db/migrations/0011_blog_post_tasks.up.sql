-- Reverse-sync substrate: replace the positional text[] of task IDs on
-- blog_posts_scheduled with a normalised table where every Todoist task
-- carries its (platform, cadence) tag explicitly. Enables reverse-lookup
-- by todoist_id (needed by the future regenerate-label scanner).
ALTER TABLE blog_posts_scheduled DROP COLUMN todoist_task_ids;

CREATE TABLE blog_post_tasks (
    canonical_url text        NOT NULL REFERENCES blog_posts_scheduled(canonical_url) ON DELETE CASCADE,
    platform      text        NOT NULL,
    cadence       text        NOT NULL,
    todoist_id    text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (canonical_url, platform, cadence)
);

CREATE UNIQUE INDEX blog_post_tasks_todoist_id ON blog_post_tasks (todoist_id);
