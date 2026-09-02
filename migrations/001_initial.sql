CREATE TABLE IF NOT EXISTS schema_migrations (
    name text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
    id bigserial PRIMARY KEY,
    username text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    email text NOT NULL DEFAULT '',
    department text NOT NULL DEFAULT '',
    password_hash text,
    auth_source text NOT NULL DEFAULT 'local',
    external_subject text,
    active boolean NOT NULL DEFAULT true,
    last_login_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS users_username_lower_uq ON users (lower(username));
CREATE UNIQUE INDEX IF NOT EXISTS users_external_subject_uq ON users (external_subject) WHERE external_subject IS NOT NULL;

CREATE TABLE IF NOT EXISTS roles (
    id bigserial PRIMARY KEY,
    role_key text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    permissions jsonb NOT NULL DEFAULT '[]'::jsonb,
    system boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_roles (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id bigint NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE IF NOT EXISTS sessions (
    jti text PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    ip_address text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions(user_id, expires_at);

CREATE TABLE IF NOT EXISTS api_keys (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL,
    prefix text NOT NULL,
    secret_hash bytea NOT NULL UNIQUE,
    scopes jsonb NOT NULL DEFAULT '[]'::jsonb,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked','rotated')),
    rotated_from_id bigint REFERENCES api_keys(id),
    last_used_at timestamptz,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);
CREATE INDEX IF NOT EXISTS api_keys_user_idx ON api_keys(user_id, status);

CREATE TABLE IF NOT EXISTS settings (
    setting_key text PRIMARY KEY,
    value jsonb NOT NULL,
    description text NOT NULL DEFAULT '',
    updated_by bigint REFERENCES users(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS secrets (
    secret_key text PRIMARY KEY,
    encrypted_value bytea NOT NULL,
    version integer NOT NULL DEFAULT 1,
    updated_by bigint REFERENCES users(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS hubs (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    base_url text NOT NULL,
    network text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    verify_tls boolean NOT NULL DEFAULT true,
    collect_interval_seconds integer NOT NULL DEFAULT 60 CHECK (collect_interval_seconds BETWEEN 30 AND 86400),
    api_token_encrypted bytea,
    status text NOT NULL DEFAULT 'unknown',
    version text NOT NULL DEFAULT '',
    last_seen_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS managed_users (
    id bigserial PRIMARY KEY,
    hub_id bigint NOT NULL REFERENCES hubs(id) ON DELETE CASCADE,
    username text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    department text NOT NULL DEFAULT '',
    admin boolean NOT NULL DEFAULT false,
    active boolean NOT NULL DEFAULT true,
    last_activity_at timestamptz,
    raw jsonb NOT NULL DEFAULT '{}'::jsonb,
    synced_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(hub_id, username)
);

CREATE TABLE IF NOT EXISTS servers (
    id bigserial PRIMARY KEY,
    hub_id bigint NOT NULL REFERENCES hubs(id) ON DELETE CASCADE,
    managed_user_id bigint REFERENCES managed_users(id) ON DELETE SET NULL,
    username text NOT NULL,
    server_name text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'unknown',
    started_at timestamptz,
    last_activity_at timestamptz,
    url text NOT NULL DEFAULT '',
    node_name text NOT NULL DEFAULT '',
    pod_name text NOT NULL DEFAULT '',
    image text NOT NULL DEFAULT '',
    cpu_cores double precision,
    memory_bytes bigint,
    gpu_count integer,
    raw jsonb NOT NULL DEFAULT '{}'::jsonb,
    synced_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(hub_id, username, server_name)
);
CREATE INDEX IF NOT EXISTS servers_status_idx ON servers(status, hub_id);

CREATE TABLE IF NOT EXISTS resources (
    id bigserial PRIMARY KEY,
    kind text NOT NULL,
    name text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    owner_user_id bigint REFERENCES users(id) ON DELETE SET NULL,
    data jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by bigint REFERENCES users(id) ON DELETE SET NULL,
    updated_by bigint REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS resources_kind_idx ON resources(kind, updated_at DESC);

CREATE TABLE IF NOT EXISTS metric_samples (
    id bigserial PRIMARY KEY,
    source text NOT NULL,
    metric_name text NOT NULL,
    labels jsonb NOT NULL DEFAULT '{}'::jsonb,
    value double precision NOT NULL,
    sampled_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS metric_samples_query_idx ON metric_samples(metric_name, sampled_at DESC);

CREATE TABLE IF NOT EXISTS audit_logs (
    id bigserial PRIMARY KEY,
    actor_user_id bigint REFERENCES users(id) ON DELETE SET NULL,
    actor_username text NOT NULL DEFAULT '',
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL DEFAULT '',
    before_value jsonb,
    after_value jsonb,
    ip_address text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    result text NOT NULL DEFAULT 'success',
    reason text NOT NULL DEFAULT '',
    request_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_logs_created_idx ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS audit_logs_resource_idx ON audit_logs(resource_type, resource_id);

CREATE TABLE IF NOT EXISTS ai_usage (
    id bigserial PRIMARY KEY,
    user_id bigint REFERENCES users(id) ON DELETE SET NULL,
    provider text NOT NULL,
    model text NOT NULL,
    input_tokens integer,
    output_tokens integer,
    status text NOT NULL,
    latency_ms bigint,
    request_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
