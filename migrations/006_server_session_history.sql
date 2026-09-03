CREATE TABLE IF NOT EXISTS server_sessions (
    id bigserial PRIMARY KEY,
    server_id bigint REFERENCES servers(id) ON DELETE SET NULL,
    hub_id bigint NOT NULL REFERENCES hubs(id) ON DELETE CASCADE,
    username text NOT NULL,
    server_name text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    last_activity_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (ended_at IS NULL OR ended_at >= started_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS server_sessions_active_server_uq
    ON server_sessions(server_id) WHERE server_id IS NOT NULL AND ended_at IS NULL;
CREATE INDEX IF NOT EXISTS server_sessions_time_idx
    ON server_sessions(started_at, ended_at);
CREATE INDEX IF NOT EXISTS server_sessions_user_time_idx
    ON server_sessions(username, started_at DESC);

INSERT INTO server_sessions(server_id,hub_id,username,server_name,started_at,ended_at,last_activity_at)
SELECT id,hub_id,username,server_name,COALESCE(started_at,synced_at),
	       CASE WHEN status='running' OR status='starting' OR status LIKE 'pending_%' THEN NULL ELSE GREATEST(synced_at,COALESCE(started_at,synced_at)) END,last_activity_at
FROM servers
ON CONFLICT (server_id) WHERE server_id IS NOT NULL AND ended_at IS NULL DO NOTHING;
