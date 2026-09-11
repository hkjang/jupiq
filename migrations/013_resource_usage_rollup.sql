-- Point-in-time samples answer "how much CPU is this pod using right now".
-- They cannot answer "how much did this user consume", because an average over
-- samples is a rate: one core held for a day and one core held for five minutes
-- both average 1.0. Consumption is the integral of the rate over time, so it is
-- accumulated here as core-seconds and byte-seconds, per hour and per user.
--
-- The rollup is also what makes the history durable. Raw samples are deleted on
-- the system.raw_retention_days window (30 days by default); these buckets keep
-- their own, much longer window, so a quarterly or yearly consumption question
-- survives the prune that removed the samples it was derived from.
CREATE TABLE IF NOT EXISTS resource_usage_hourly (
    bucket timestamptz NOT NULL,
    username text NOT NULL,
    -- Hub names are not unique across the install, so the name is the key and
    -- the id is informational. Deliberately no foreign key: consumption history
    -- is accounting data and must outlive the hub row it refers to.
    hub_name text NOT NULL DEFAULT '',
    hub_id bigint,
    network text NOT NULL DEFAULT '',
    department text NOT NULL DEFAULT '',
    -- Integrals. Seconds are the storage unit so summing across buckets stays
    -- exact; the API converts to core-hours and GB-hours for display.
    cpu_core_seconds double precision NOT NULL DEFAULT 0,
    memory_byte_seconds double precision NOT NULL DEFAULT 0,
    gpu_seconds double precision NOT NULL DEFAULT 0,
    -- Peaks within the bucket, for the capacity question an integral cannot
    -- answer: how large a single pod must we still be able to fit.
    cpu_peak_cores double precision NOT NULL DEFAULT 0,
    memory_peak_bytes double precision NOT NULL DEFAULT 0,
    -- Pod-seconds that samples actually covered, comparable against
    -- runtime_seconds below. A bucket whose collector was down for most of the
    -- hour must not be read as describing the whole hour, so how much of it was
    -- observed travels with the figure instead of being inferred later.
    covered_seconds double precision NOT NULL DEFAULT 0,
    sample_count integer NOT NULL DEFAULT 0,
    -- Wall-clock pod-seconds from server_sessions, authoritative and available
    -- even when Prometheus was unreachable for the whole bucket.
    runtime_seconds double precision NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (bucket, username, hub_name)
);

CREATE INDEX IF NOT EXISTS resource_usage_hourly_time_idx
    ON resource_usage_hourly(bucket DESC);
CREATE INDEX IF NOT EXISTS resource_usage_hourly_user_time_idx
    ON resource_usage_hourly(username, bucket DESC);

-- Marks how far the rollup has advanced, so a restart resumes instead of
-- rebuilding every bucket and so the prune can refuse to delete raw samples the
-- rollup has not consumed yet.
CREATE TABLE IF NOT EXISTS resource_usage_rollup_state (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    rolled_up_through timestamptz NOT NULL DEFAULT to_timestamp(0),
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO resource_usage_rollup_state(id) VALUES (true) ON CONFLICT (id) DO NOTHING;

-- Consumption history outlives the raw samples it was derived from.
UPDATE settings
SET value = jsonb_set(value, '{usage_retention_days}', '365'::jsonb, true),
    updated_at = now()
WHERE setting_key = 'system'
  AND jsonb_typeof(value) = 'object'
  AND NOT value ? 'usage_retention_days';
