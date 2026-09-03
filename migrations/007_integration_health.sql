CREATE TABLE IF NOT EXISTS integration_health (
    integration_type text PRIMARY KEY,
    status text NOT NULL DEFAULT 'unknown',
    version text NOT NULL DEFAULT '',
    latency_ms bigint,
    last_attempt_at timestamptz,
    last_success_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    consecutive_failures integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);
