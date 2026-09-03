-- The bootstrap break-glass role is the only immutable role. Repair upgrades
-- from builds that allowed its wildcard permission or system flag to drift.
UPDATE roles
SET permissions = '["*"]'::jsonb,
    system = true,
    updated_at = now()
WHERE role_key = 'super_admin'
  AND (permissions <> '["*"]'::jsonb OR system = false);
