-- Role assignments created before this migration remain global for backwards
-- compatibility. New restricted assignments are expressed as one or more
-- Hub/department clauses attached to the user-role binding.
ALTER TABLE user_roles
    ADD COLUMN IF NOT EXISTS scope_mode text NOT NULL DEFAULT 'global';

ALTER TABLE user_roles
    DROP CONSTRAINT IF EXISTS user_roles_scope_mode_check;

ALTER TABLE user_roles
    ADD CONSTRAINT user_roles_scope_mode_check
    CHECK (scope_mode IN ('global', 'restricted'));

CREATE TABLE IF NOT EXISTS user_role_scope_clauses (
    user_id bigint NOT NULL,
    role_id bigint NOT NULL,
    scope_type text NOT NULL CHECK (scope_type IN ('hub', 'department')),
    scope_value text NOT NULL CHECK (length(btrim(scope_value)) BETWEEN 1 AND 256),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id, scope_type, scope_value),
    FOREIGN KEY (user_id, role_id)
        REFERENCES user_roles(user_id, role_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS user_role_scope_clauses_lookup_idx
    ON user_role_scope_clauses(scope_type, scope_value, user_id, role_id);
