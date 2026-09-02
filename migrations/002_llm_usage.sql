CREATE TABLE IF NOT EXISTS llm_usage_samples (
    id bigserial PRIMARY KEY,
    source text NOT NULL,
    username text,
    pod_name text NOT NULL DEFAULT '',
    hub_name text NOT NULL DEFAULT '',
    model text NOT NULL DEFAULT '',
    calls bigint NOT NULL DEFAULT 0,
    success_count bigint NOT NULL DEFAULT 0,
    error_count bigint NOT NULL DEFAULT 0,
    latency_p50_ms double precision,
    latency_p95_ms double precision,
    input_tokens bigint,
    output_tokens bigint,
    total_tokens bigint,
    bytes bigint,
    estimated_cost numeric(18,6),
    sampled_at timestamptz NOT NULL,
    labels jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS llm_usage_samples_time_idx ON llm_usage_samples(sampled_at DESC);
CREATE INDEX IF NOT EXISTS llm_usage_samples_user_idx ON llm_usage_samples(username,sampled_at DESC);
