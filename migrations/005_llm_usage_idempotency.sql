ALTER TABLE llm_usage_samples
    ADD COLUMN IF NOT EXISTS metric_name text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS window_start timestamptz,
    ADD COLUMN IF NOT EXISTS dimension_fingerprint text NOT NULL DEFAULT '';

-- Historical v1.0 rows did not retain the source metric identity. Give them a
-- stable unique legacy identity without attempting an unsafe retroactive
-- merge. All new samples use a one-minute window and dimension fingerprint.
UPDATE llm_usage_samples
SET window_start = date_trunc('minute', sampled_at),
    dimension_fingerprint = 'legacy-' || id::text
WHERE window_start IS NULL OR dimension_fingerprint = '';

ALTER TABLE llm_usage_samples
    ALTER COLUMN window_start SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS llm_usage_samples_idempotency_uq
    ON llm_usage_samples(source, metric_name, window_start, dimension_fingerprint);
