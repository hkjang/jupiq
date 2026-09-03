-- The singular profile:* namespace is reserved for a user's own personal page
-- and API keys. Environment profile catalog administration uses profiles:*.
-- Existing custom roles keep their original personal permissions and gain the
-- matching catalog permission; API-key scopes are intentionally not changed.
UPDATE roles
SET permissions = permissions
    || CASE WHEN permissions ? 'profile:read' AND NOT permissions ? 'profiles:read'
            THEN '["profiles:read"]'::jsonb ELSE '[]'::jsonb END
    || CASE WHEN permissions ? 'profile:write' AND NOT permissions ? 'profiles:write'
            THEN '["profiles:write"]'::jsonb ELSE '[]'::jsonb END,
    updated_at = now()
WHERE role_key <> 'user'
  AND (permissions ? 'profile:read' OR permissions ? 'profile:write');

-- Automatically provisioned OIDC users must not see the organization-wide
-- multi-Hub dashboard. Personal usage remains available through the profile.
UPDATE roles
SET permissions = (permissions - 'dashboard:read' - 'dashboard:*'),
    updated_at = now()
WHERE role_key = 'user';
