-- Keep only operational JupyterHub fields. Historical raw documents may hold
-- user_options, environment values, annotations or projected secret metadata.
UPDATE managed_users
SET raw = jsonb_strip_nulls(jsonb_build_object(
    'roles', CASE WHEN jsonb_typeof(raw->'roles')='array' THEN raw->'roles' END,
    'groups', CASE WHEN jsonb_typeof(raw->'groups')='array' THEN raw->'groups' END,
    'pending', CASE WHEN jsonb_typeof(raw->'pending')='string' THEN raw->'pending' END
));

UPDATE servers
SET raw = COALESCE((
    SELECT jsonb_object_agg(entry.key,entry.value)
    FROM jsonb_each(CASE WHEN jsonb_typeof(servers.raw) = 'object' THEN servers.raw ELSE '{}'::jsonb END) entry
    WHERE entry.key = ANY(ARRAY[
        'project','profile','resource_profile','ready','pending','progress',
        'resource_sampled_at','cpu_sampled_at','memory_sampled_at','gpu_sampled_at',
        'gpu_utilization','vram_bytes'
    ])
), '{}'::jsonb);

UPDATE metric_samples
SET labels = COALESCE((
    SELECT jsonb_object_agg(entry.key,entry.value)
    FROM jsonb_each(CASE WHEN jsonb_typeof(metric_samples.labels) = 'object' THEN metric_samples.labels ELSE '{}'::jsonb END) entry
    WHERE entry.key = ANY(ARRAY[
        'pod','pod_name','hub','hub_name','network','network_name','username','user',
        'department','project','node','node_name','namespace','container','device','gpu','gpu_uuid',
        'model','status','code','path','route','method'
    ])
), '{}'::jsonb);

UPDATE llm_usage_samples
SET labels = COALESCE((
    SELECT jsonb_object_agg(entry.key,entry.value)
    FROM jsonb_each(CASE WHEN jsonb_typeof(llm_usage_samples.labels) = 'object' THEN llm_usage_samples.labels ELSE '{}'::jsonb END) entry
    WHERE entry.key = ANY(ARRAY['pod','pod_name','path','route','status','model','hub','network'])
), '{}'::jsonb);
