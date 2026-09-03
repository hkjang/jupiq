ALTER TABLE metric_samples
    ADD COLUMN IF NOT EXISTS metric_kind text NOT NULL DEFAULT 'generic';

-- Classify both conventional metric names and administrator-defined aliases.
-- The latter are recovered from the saved Prometheus query text so disabling
-- GPU monitoring also hides historical samples collected under an innocuous
-- alias such as accelerator_utilization.
UPDATE metric_samples
SET metric_kind = 'gpu'
WHERE metric_name ~* '(gpu|vram|dcgm|cuda|nvidia)'
   OR labels ?| ARRAY['gpu','gpu_uuid']
   OR metric_name IN (
       SELECT query.key
       FROM settings setting
       CROSS JOIN LATERAL jsonb_each_text(
           CASE WHEN jsonb_typeof(setting.value->'queries') = 'object'
                THEN setting.value->'queries' ELSE '{}'::jsonb END
       ) query
       WHERE setting.setting_key = 'prometheus'
         AND query.value ~* '(gpu|vram|dcgm|cuda|nvidia)'
   );

CREATE INDEX IF NOT EXISTS metric_samples_kind_time_idx
    ON metric_samples(metric_kind, sampled_at DESC);
