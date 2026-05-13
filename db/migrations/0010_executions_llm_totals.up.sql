ALTER TABLE executions
  ADD COLUMN total_tokens_in     bigint        NOT NULL DEFAULT 0,
  ADD COLUMN total_tokens_out    bigint        NOT NULL DEFAULT 0,
  ADD COLUMN total_tokens_cached bigint        NOT NULL DEFAULT 0,
  ADD COLUMN total_cost_usd      numeric(12,6) NOT NULL DEFAULT 0;
