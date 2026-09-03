-- Remove controls that were previously persisted but never consumed by the
-- runtime. Keeping them would imply that service branding, locale, timezone,
-- pagination, or a global Hub cadence changed when they did not.
UPDATE settings
SET value = CASE WHEN jsonb_typeof(value) = 'object' THEN value
        - 'service_name'
        - 'locale'
        - 'timezone'
        - 'collection_interval_seconds'
        - 'page_size'
        - 'hourly_retention_days'
    ELSE '{"raw_retention_days":30}'::jsonb END,
    updated_at = now()
WHERE setting_key = 'system';

-- Hub connection data and its encrypted token live in the hubs table. This
-- obsolete generic setting was not read by the collector.
DELETE FROM settings WHERE setting_key = 'jupyterhub';
DELETE FROM secrets WHERE secret_key = 'jupyterhub.api_token';

-- timeout_seconds was displayed for Prometheus but both collection and
-- preflight use bounded code-level timeouts.
UPDATE settings
SET value = CASE WHEN jsonb_typeof(value) = 'object' THEN value - 'timeout_seconds'
                 ELSE '{"enabled":false,"base_url":"","verify_tls":true}'::jsonb END,
    updated_at = now()
WHERE setting_key = 'prometheus';

-- These legacy flags were not wired to delivery or emergency-account state.
-- Bootstrap access is controlled by the required deployment credentials.
UPDATE settings
SET value = CASE WHEN jsonb_typeof(value) = 'object' THEN value - 'portal_enabled' - 'email_enabled'
                 ELSE '{"webhook_enabled":false,"base_url":"","events":[]}'::jsonb END,
    updated_at = now()
WHERE setting_key = 'notifications';

UPDATE settings
SET value = CASE WHEN jsonb_typeof(value) = 'object' THEN value - 'break_glass_enabled' - 'break_glass_minutes' - 'dangerous_action_reason' - 'key_roles'
                 ELSE '{"key_rotation_days":90,"key_max_lifetime_days":365,"key_permissions":[]}'::jsonb END,
    updated_at = now()
WHERE setting_key = 'security';

-- v1.0 accepted any JSON value for an allowed setting key. Normalize scalar
-- legacy values so one malformed administrator submission cannot leave an
-- integration endpoint unusable after the upgrade. Object values are kept and
-- can be reviewed in the now strictly validated administrator form.
UPDATE settings SET value = '{"approval_enabled":false,"manager_review_enabled":false,"require_reason":false,"request_types":[]}'::jsonb, updated_at = now()
WHERE setting_key = 'workflow' AND jsonb_typeof(value) IS DISTINCT FROM 'object';

UPDATE settings SET value = '{"enabled":false,"issuer_url":"","client_id":"","redirect_url":"","scopes":["openid","profile","email"],"username_claim":"preferred_username","auto_create_users":true,"verify_tls":true}'::jsonb, updated_at = now()
WHERE setting_key = 'auth.oidc' AND jsonb_typeof(value) IS DISTINCT FROM 'object';

UPDATE settings SET value = '{"enabled":false,"base_url":"","verify_tls":true}'::jsonb, updated_at = now()
WHERE setting_key = 'kubernetes' AND jsonb_typeof(value) IS DISTINCT FROM 'object';

UPDATE settings SET value = '{"gpu_monitoring":false,"llm_usage_monitoring":false}'::jsonb, updated_at = now()
WHERE setting_key = 'features' AND jsonb_typeof(value) IS DISTINCT FROM 'object';

UPDATE settings SET value = '{"source":"prometheus","pod_username_regex":"^jupyter-(?P<username>[a-zA-Z0-9._-]+)","path_matcher":"/v1/chat/completions","label_mappings":{},"promql":{"calls":"sum(increase(http_requests_total{path=\"/v1/chat/completions\"}[1m])) by (pod,path,status,model,hub)"},"input_cost_per_million":0,"output_cost_per_million":0,"stale_seconds":300,"retention_days":30}'::jsonb, updated_at = now()
WHERE setting_key = 'llm_usage' AND jsonb_typeof(value) IS DISTINCT FROM 'object';

-- Historical rows came only from candidate-form preflight tests and therefore
-- did not represent the persisted provider's operational health.
DELETE FROM integration_health;
