-- Auto-poster substrate. posted_at + posted_url track which tasks have
-- actually been published to the target platform; the orchestrator uses
-- posted_at as the idempotency boundary so a Todoist CompleteTask retry
-- doesn't republish the post.
ALTER TABLE blog_post_tasks
    ADD COLUMN posted_at  timestamptz,
    ADD COLUMN posted_url text;
