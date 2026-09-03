-- Existing v1.0 databases did not persist verify_tls in the AI settings
-- default. aiConfig's zero value would therefore disable certificate
-- verification. Preserve explicit administrator choices and repair only rows
-- where the setting is absent.
UPDATE settings
SET value = jsonb_set(
        jsonb_set(
            CASE WHEN jsonb_typeof(value) = 'object' THEN value
                 ELSE '{"enabled":false,"provider":"openai-compatible","base_url":"","model":"","max_tokens":4096}'::jsonb
            END,
            '{verify_tls}',
            CASE WHEN jsonb_typeof(value->'verify_tls') = 'boolean' THEN value->'verify_tls' ELSE 'true'::jsonb END,
            true
        ),
        '{streaming}',
        'true'::jsonb,
        true
    ),
    updated_at = now()
WHERE setting_key = 'ai';
