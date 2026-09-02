package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) ListSettings(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.Pool.Query(ctx, `SELECT setting_key,value FROM settings ORDER BY setting_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := map[string]json.RawMessage{}
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = json.RawMessage(value)
	}
	return settings, rows.Err()
}

func (s *Store) GetSetting(ctx context.Context, key string, target any) error {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key=$1`, key).Scan(&raw)
	if err != nil {
		return dbNotFound(err)
	}
	return json.Unmarshal(raw, target)
}

func (s *Store) SetSetting(ctx context.Context, key string, value any, userID int64) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO settings(setting_key,value,updated_by) VALUES($1,$2,$3) ON CONFLICT(setting_key) DO UPDATE SET value=EXCLUDED.value,updated_by=EXCLUDED.updated_by,updated_at=now()`, key, raw, userID)
	return err
}

func (s *Store) SetSecret(ctx context.Context, key, value string, userID int64) error {
	encrypted, err := s.Cipher.Encrypt([]byte(value), "secret:"+key)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO secrets(secret_key,encrypted_value,updated_by) VALUES($1,$2,$3) ON CONFLICT(secret_key) DO UPDATE SET encrypted_value=EXCLUDED.encrypted_value,version=secrets.version+1,updated_by=EXCLUDED.updated_by,updated_at=now()`, key, encrypted, userID)
	return err
}

func (s *Store) GetSecret(ctx context.Context, key string) (string, error) {
	var encrypted []byte
	err := s.Pool.QueryRow(ctx, `SELECT encrypted_value FROM secrets WHERE secret_key=$1`, key).Scan(&encrypted)
	if err != nil {
		return "", dbNotFound(err)
	}
	plain, err := s.Cipher.Decrypt(encrypted, "secret:"+key)
	return string(plain), err
}

func (s *Store) SecretStatus(ctx context.Context) (map[string]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT secret_key,version,updated_at FROM secrets ORDER BY secret_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]map[string]any{}
	for rows.Next() {
		var key string
		var version int
		var updated time.Time
		if err := rows.Scan(&key, &version, &updated); err != nil {
			return nil, err
		}
		result[key] = map[string]any{"configured": true, "version": version, "updated_at": updated}
	}
	return result, rows.Err()
}

func (s *Store) ApprovalEnabled(ctx context.Context) bool {
	cfg, err := s.GetWorkflowConfig(ctx)
	return err == nil && cfg.ApprovalEnabled
}

func (s *Store) CreateResource(ctx context.Context, kind string, input ResourceWrite, actorID int64) (Resource, error) {
	if input.Status == "" {
		input.Status = "active"
	}
	if len(input.Data) == 0 {
		input.Data = json.RawMessage(`{}`)
	}
	var r Resource
	err := s.Pool.QueryRow(ctx, `INSERT INTO resources(kind,name,status,owner_user_id,data,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,$6) RETURNING id,created_at,updated_at`, kind, input.Name, input.Status, input.OwnerUserID, input.Data, actorID).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	r.Kind, r.Name, r.Status, r.OwnerUserID, r.Data = kind, input.Name, input.Status, input.OwnerUserID, input.Data
	r.CreatedBy, r.UpdatedBy = &actorID, &actorID
	return r, nil
}

func (s *Store) GetResource(ctx context.Context, kind string, id int64) (Resource, error) {
	var r Resource
	var data []byte
	err := s.Pool.QueryRow(ctx, `SELECT id,kind,name,status,owner_user_id,data,created_by,updated_by,created_at,updated_at FROM resources WHERE kind=$1 AND id=$2`, kind, id).Scan(&r.ID, &r.Kind, &r.Name, &r.Status, &r.OwnerUserID, &data, &r.CreatedBy, &r.UpdatedBy, &r.CreatedAt, &r.UpdatedAt)
	r.Data = json.RawMessage(data)
	return r, dbNotFound(err)
}

func (s *Store) ListResources(ctx context.Context, kind string, page, pageSize int, status, search string, ownerID *int64) ([]Resource, Page, error) {
	page, pageSize, offset := pageBounds(page, pageSize)
	pattern := "%" + search + "%"
	var total int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM resources WHERE kind=$1 AND ($2='' OR status=$2) AND ($3='' OR name ILIKE $4) AND ($5::bigint IS NULL OR owner_user_id=$5)`, kind, status, search, pattern, ownerID).Scan(&total)
	if err != nil {
		return nil, Page{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,kind,name,status,owner_user_id,data,created_by,updated_by,created_at,updated_at FROM resources WHERE kind=$1 AND ($2='' OR status=$2) AND ($3='' OR name ILIKE $4) AND ($5::bigint IS NULL OR owner_user_id=$5) ORDER BY updated_at DESC LIMIT $6 OFFSET $7`, kind, status, search, pattern, ownerID, pageSize, offset)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()
	items := []Resource{}
	for rows.Next() {
		var r Resource
		var data []byte
		if err := rows.Scan(&r.ID, &r.Kind, &r.Name, &r.Status, &r.OwnerUserID, &data, &r.CreatedBy, &r.UpdatedBy, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, Page{}, err
		}
		r.Data = json.RawMessage(data)
		items = append(items, r)
	}
	return items, Page{Page: page, PageSize: pageSize, Total: total}, rows.Err()
}

func (s *Store) UpdateResource(ctx context.Context, kind string, id int64, input ResourceWrite, actorID int64) (Resource, error) {
	if input.Status == "" {
		input.Status = "active"
	}
	if len(input.Data) == 0 {
		input.Data = json.RawMessage(`{}`)
	}
	result, err := s.Pool.Exec(ctx, `UPDATE resources SET name=$3,status=$4,owner_user_id=$5,data=$6,updated_by=$7,updated_at=now() WHERE kind=$1 AND id=$2`, kind, id, input.Name, input.Status, input.OwnerUserID, input.Data, actorID)
	if err != nil {
		return Resource{}, err
	}
	if result.RowsAffected() == 0 {
		return Resource{}, ErrNotFound
	}
	return s.GetResource(ctx, kind, id)
}

func (s *Store) DeleteResource(ctx context.Context, kind string, id int64) error {
	result, err := s.Pool.Exec(ctx, `DELETE FROM resources WHERE kind=$1 AND id=$2`, kind, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Dashboard(ctx context.Context) (map[string]any, error) {
	result := map[string]any{"sampled_at": time.Now().UTC()}
	queries := map[string]string{
		"hubs":           `SELECT count(*) FROM hubs WHERE enabled`,
		"degraded_hubs":  `SELECT count(*) FROM hubs WHERE enabled AND status NOT IN ('healthy','ok')`,
		"users":          `SELECT count(*) FROM managed_users WHERE active`,
		"active_users":   `SELECT count(DISTINCT (hub_id,username)) FROM servers WHERE status='running'`,
		"servers":        `SELECT count(*) FROM servers WHERE status='running'`,
		"gpu_users":      `SELECT count(DISTINCT (hub_id,username)) FROM servers WHERE status='running' AND COALESCE(gpu_count,0)>0`,
		"incidents_open": `SELECT count(*) FROM resources WHERE kind='incident' AND status NOT IN ('resolved','closed')`,
		"approvals_open": `SELECT count(*) FROM resources WHERE kind='approval' AND status IN ('pending_review','pending','executing')`,
	}
	for key, query := range queries {
		var count int64
		if err := s.Pool.QueryRow(ctx, query).Scan(&count); err != nil {
			return nil, err
		}
		result[key] = count
	}
	var freshness *time.Time
	_ = s.Pool.QueryRow(ctx, `SELECT max(ts) FROM (SELECT max(synced_at) ts FROM servers UNION ALL SELECT max(sampled_at) FROM metric_samples) x`).Scan(&freshness)
	result["data_freshness"] = freshness
	result["stale"] = freshness == nil || time.Since(*freshness) > 5*time.Minute
	return result, nil
}

func (s *Store) LiveSnapshot(ctx context.Context) (map[string]any, error) {
	base, err := s.Dashboard(ctx)
	if err != nil {
		return nil, err
	}
	var features struct {
		GPUMonitoring      bool `json:"gpu_monitoring"`
		LLMUsageMonitoring bool `json:"llm_usage_monitoring"`
	}
	_ = s.GetSetting(ctx, "features", &features)
	base["feature_enabled"] = map[string]bool{"gpu_monitoring": features.GPUMonitoring, "llm_usage_monitoring": features.LLMUsageMonitoring}
	rows, err := s.Pool.Query(ctx, `SELECT h.id,h.name,h.network,h.collect_interval_seconds,s.username,COALESCE(u.department,''),s.server_name,s.status,s.started_at,s.last_activity_at,s.cpu_cores,s.memory_bytes,s.gpu_count,s.raw,s.synced_at FROM servers s JOIN hubs h ON h.id=s.hub_id LEFT JOIN managed_users u ON u.id=s.managed_user_id WHERE s.status='running' ORDER BY h.name,s.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := []map[string]any{}
	freshSessions := []map[string]any{}
	freshUsers := map[string]bool{}
	aggregates := map[string]map[string]float64{}
	now := time.Now().UTC()
	for rows.Next() {
		var hubID int64
		var hub, network, username, department, serverName, status string
		var collectInterval int
		var started, activity *time.Time
		var cpu *float64
		var memory *int64
		var gpu *int
		var raw []byte
		var synced time.Time
		if err := rows.Scan(&hubID, &hub, &network, &collectInterval, &username, &department, &serverName, &status, &started, &activity, &cpu, &memory, &gpu, &raw, &synced); err != nil {
			return nil, err
		}
		var detail map[string]any
		_ = json.Unmarshal(raw, &detail)
		project, _ := detail["project"].(string)
		vram, gpuUtil, gpuValue := float64(0), float64(0), float64(0)
		if features.GPUMonitoring {
			vram = number(detail["vram_bytes"])
			gpuUtil = number(detail["gpu_utilization"])
			gpuValue = float64(derefInt(gpu))
		}
		cpuValue, memoryValue := derefFloat(cpu), float64(derefInt64(memory))
		runtimeSeconds := float64(0)
		if started != nil {
			runtimeSeconds = now.Sub(*started).Seconds()
		}
		idle := activity != nil && now.Sub(*activity) > 2*time.Hour && cpuValue < .03 && (!features.GPUMonitoring || gpuUtil < .03)
		sessionStale := sessionSnapshotStale(now, synced, collectInterval)
		session := map[string]any{"hub_id": hubID, "hub": hub, "network": network, "department": department, "project": project, "username": username, "server_name": serverName, "status": status, "started_at": started, "last_activity_at": activity, "runtime_seconds": runtimeSeconds, "cpu_cores": cpuValue, "memory_bytes": memoryValue, "idle_candidate": idle, "sampled_at": synced, "stale": sessionStale, "data_freshness": synced}
		if features.GPUMonitoring {
			session["gpu_count"], session["gpu_utilization"], session["vram_bytes"], session["waste_candidate"] = gpuValue, gpuUtil, vram, idle && gpuValue > 0
		}
		sessions = append(sessions, session)
		if sessionStale {
			continue
		}
		freshSessions = append(freshSessions, session)
		freshUsers[username] = true
		for _, key := range []string{"all", "hub:" + hub, "network:" + network, "department:" + department, "project:" + project} {
			if aggregates[key] == nil {
				aggregates[key] = map[string]float64{}
			}
			aggregates[key]["servers"]++
			aggregates[key]["cpu_cores"] += cpuValue
			aggregates[key]["memory_bytes"] += memoryValue
			if features.GPUMonitoring {
				aggregates[key]["gpu_count"] += gpuValue
				aggregates[key]["vram_bytes"] += vram
			}
		}
	}
	summary := map[string]any{"active_users": len(freshUsers), "running_servers": len(freshSessions), "cpu_usage": aggregates["all"]["cpu_cores"], "memory_usage": aggregates["all"]["memory_bytes"], "idle_sessions": countBool(freshSessions, "idle_candidate"), "long_running_sessions": countRuntime(freshSessions, 24*time.Hour)}
	if features.GPUMonitoring {
		summary["gpu_usage"], summary["vram_usage"] = aggregates["all"]["gpu_count"], aggregates["all"]["vram_bytes"]
	}
	base["summary"] = summary
	base["active_users"], base["servers"] = len(freshUsers), len(freshSessions)
	base["sessions"] = sessions
	base["live_users"] = sessions
	base["aggregates"] = aggregates
	base["usage_trend"] = []any{}
	base["top_users"] = topLiveUsers(freshSessions)
	if features.GPUMonitoring {
		base["gpu_waste"] = filterBool(freshSessions, "waste_candidate")
	} else {
		base["gpu_waste"] = []any{}
	}
	base["filters"] = map[string]any{"group_by": []string{"hub", "network", "department", "project"}}
	hubs, _ := s.ListHubs(ctx)
	base["hubs"] = hubs
	if features.LLMUsageMonitoring {
		llm, _ := s.LLMUsage(ctx, now.Add(-time.Hour), now, "user")
		base["llm_usage"] = llm
	} else {
		base["llm_usage"] = map[string]any{"feature_enabled": false, "data": []any{}}
	}
	base["sampled_at"] = now
	return base, rows.Err()
}

func sessionSnapshotStale(now, syncedAt time.Time, collectIntervalSeconds int) bool {
	window := 5 * time.Minute
	if configured := 2 * time.Duration(collectIntervalSeconds) * time.Second; configured > window {
		window = configured
	}
	return syncedAt.IsZero() || now.Sub(syncedAt) > window
}

func countBool(items []map[string]any, key string) int {
	count := 0
	for _, item := range items {
		if v, _ := item[key].(bool); v {
			count++
		}
	}
	return count
}

func countRuntime(items []map[string]any, threshold time.Duration) int {
	count := 0
	for _, item := range items {
		if v, _ := item["runtime_seconds"].(float64); v >= threshold.Seconds() {
			count++
		}
	}
	return count
}

func filterBool(items []map[string]any, key string) []map[string]any {
	result := []map[string]any{}
	for _, item := range items {
		if v, _ := item[key].(bool); v {
			result = append(result, item)
		}
	}
	return result
}

func topLiveUsers(items []map[string]any) []map[string]any {
	byUser := map[string]map[string]any{}
	for _, item := range items {
		username, _ := item["username"].(string)
		if byUser[username] == nil {
			byUser[username] = map[string]any{"username": username, "servers": 0, "cpu_cores": float64(0), "memory_bytes": float64(0)}
		}
		byUser[username]["servers"] = byUser[username]["servers"].(int) + 1
		byUser[username]["cpu_cores"] = byUser[username]["cpu_cores"].(float64) + number(item["cpu_cores"])
		byUser[username]["memory_bytes"] = byUser[username]["memory_bytes"].(float64) + number(item["memory_bytes"])
	}
	result := make([]map[string]any, 0, len(byUser))
	for _, item := range byUser {
		result = append(result, item)
	}
	return result
}

func number(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func derefFloat(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func (s *Store) Usage(ctx context.Context, from, to time.Time, granularity, groupBy string) (map[string]any, error) {
	allowedGranularity := map[string]string{"hour": "hour", "day": "day", "week": "week", "month": "month"}
	unit, ok := allowedGranularity[granularity]
	if !ok {
		unit = "day"
	}
	allowedGroup := map[string]string{"user": "labels->>'username'", "hub": "labels->>'hub'", "network": "labels->>'network'", "department": "labels->>'department'", "project": "labels->>'project'"}
	groupExpr, ok := allowedGroup[groupBy]
	if !ok {
		groupBy, groupExpr = "user", "labels->>'username'"
	}
	// Server runtime is calculated from server intervals below. Excluding runtime
	// counters here prevents the same usage from appearing twice in the trend.
	query := `SELECT date_trunc('` + unit + `',sampled_at) bucket,COALESCE(` + groupExpr + `,'unknown') grouping,metric_name,avg(value),max(value) FROM metric_samples WHERE sampled_at BETWEEN $1 AND $2 AND metric_name NOT IN ('runtime_seconds','server_running_seconds') GROUP BY 1,2,3 ORDER BY 1,2`
	rows, err := s.Pool.Query(ctx, query, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	trend := []map[string]any{}
	for rows.Next() {
		var bucket time.Time
		var group, metric string
		var avg, max float64
		if err := rows.Scan(&bucket, &group, &metric, &avg, &max); err != nil {
			return nil, err
		}
		trend = append(trend, map[string]any{"bucket": bucket, "group": group, "metric": metric, "average": avg, "peak": max})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	serverGroupExpr := map[string]string{"user": "s.username", "hub": "h.name", "network": "h.network", "department": "COALESCE(u.department,'unknown')", "project": "COALESCE(s.raw->>'project','unknown')"}[groupBy]
	intervalStep := map[string]string{"hour": "1 hour", "day": "1 day", "week": "1 week", "month": "1 month"}[unit]
	serverTrendSQL := `WITH buckets AS (
		SELECT bucket, bucket+interval '` + intervalStep + `' bucket_end
		FROM generate_series(date_trunc('` + unit + `',$1::timestamptz),date_trunc('` + unit + `',$2::timestamptz),interval '` + intervalStep + `') bucket
	), server_intervals AS (
		SELECT s.id,COALESCE(` + serverGroupExpr + `,'unknown') grouping,COALESCE(s.started_at,s.synced_at) original_start,
		       GREATEST(COALESCE(s.started_at,s.synced_at),$1::timestamptz) active_from,
		       LEAST(COALESCE(CASE WHEN s.status='running' THEN $2::timestamptz END,s.last_activity_at,s.synced_at),$2::timestamptz) active_to
		FROM servers s JOIN hubs h ON h.id=s.hub_id LEFT JOIN managed_users u ON u.id=s.managed_user_id
		WHERE COALESCE(s.started_at,s.synced_at)<$2::timestamptz
		  AND COALESCE(CASE WHEN s.status='running' THEN $2::timestamptz END,s.last_activity_at,s.synced_at)>$1::timestamptz
	)
	SELECT b.bucket,si.grouping,
	       count(*) FILTER(WHERE si.original_start>=GREATEST(b.bucket,$1::timestamptz) AND si.original_start<LEAST(b.bucket_end,$2::timestamptz)),
	       COALESCE(sum(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(si.active_to,b.bucket_end,$2::timestamptz)-GREATEST(si.active_from,b.bucket,$1::timestamptz))))),0)::double precision
	FROM buckets b JOIN server_intervals si ON si.active_from<LEAST(b.bucket_end,$2::timestamptz) AND si.active_to>GREATEST(b.bucket,$1::timestamptz)
	GROUP BY b.bucket,si.grouping ORDER BY b.bucket,si.grouping`
	serverRows, err := s.Pool.Query(ctx, serverTrendSQL, from, to)
	if err != nil {
		return nil, err
	}
	for serverRows.Next() {
		var bucket time.Time
		var group string
		var starts int64
		var runtime float64
		if err := serverRows.Scan(&bucket, &group, &starts, &runtime); err != nil {
			serverRows.Close()
			return nil, err
		}
		trend = append(trend, map[string]any{"bucket": bucket, "group": group, "metric": "server_starts", "average": float64(starts), "peak": float64(starts)}, map[string]any{"bucket": bucket, "group": group, "metric": "runtime_seconds", "average": runtime, "peak": runtime})
	}
	if err := serverRows.Err(); err != nil {
		serverRows.Close()
		return nil, err
	}
	serverRows.Close()
	if groupBy == "user" {
		loginRows, err := s.Pool.Query(ctx, `SELECT date_trunc('`+unit+`',created_at),COALESCE(NULLIF(actor_username,''),'unknown'),count(*) FROM audit_logs WHERE action IN ('auth.login','auth.oidc') AND result='success' AND created_at BETWEEN $1 AND $2 GROUP BY 1,2 ORDER BY 1,2`, from, to)
		if err != nil {
			return nil, err
		}
		for loginRows.Next() {
			var bucket time.Time
			var group string
			var count int64
			if err := loginRows.Scan(&bucket, &group, &count); err != nil {
				loginRows.Close()
				return nil, err
			}
			trend = append(trend, map[string]any{"bucket": bucket, "group": group, "metric": "login_count", "average": float64(count), "peak": float64(count)})
		}
		if err := loginRows.Err(); err != nil {
			loginRows.Close()
			return nil, err
		}
		loginRows.Close()
	}
	result := map[string]any{"from": from, "to": to, "granularity": unit, "group_by": groupBy, "trend": trend, "top_users": []any{}, "dau": 0, "wau": 0, "mau": 0}
	for label, since := range map[string]time.Time{"dau": to.Add(-24 * time.Hour), "wau": to.Add(-7 * 24 * time.Hour), "mau": to.Add(-30 * 24 * time.Hour)} {
		var count int
		if err := s.Pool.QueryRow(ctx, usageActiveCountSQL, since, to).Scan(&count); err != nil {
			return nil, err
		}
		result[label] = count
	}
	topRows, err := s.Pool.Query(ctx, usageTopUsersSQL, from, to)
	if err != nil {
		return nil, err
	}
	top := []map[string]any{}
	for topRows.Next() {
		var username string
		var servers, logins int64
		var runtime, cpuAvg, cpuPeak, memoryAvg, memoryPeak, gpuAvg, gpuPeak float64
		if err := topRows.Scan(&username, &servers, &logins, &runtime, &cpuAvg, &cpuPeak, &memoryAvg, &memoryPeak, &gpuAvg, &gpuPeak); err != nil {
			topRows.Close()
			return nil, err
		}
		top = append(top, map[string]any{"username": username, "servers": servers, "login_count": logins, "runtime_seconds": runtime, "cpu_average": cpuAvg, "cpu_peak": cpuPeak, "memory_average": memoryAvg, "memory_peak": memoryPeak, "gpu_average": gpuAvg, "gpu_peak": gpuPeak})
	}
	if err := topRows.Err(); err != nil {
		topRows.Close()
		return nil, err
	}
	topRows.Close()
	result["top_users"] = top
	var freshness *time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT max(ts) FROM (SELECT max(sampled_at) ts FROM metric_samples UNION ALL SELECT max(created_at) FROM audit_logs WHERE action IN ('auth.login','auth.oidc') AND result='success' UNION ALL SELECT max(synced_at) FROM servers) freshness`).Scan(&freshness); err != nil {
		return nil, err
	}
	result["data_freshness"] = freshness
	result["stale"] = freshness == nil || time.Since(*freshness) > 5*time.Minute
	return result, nil
}

const usageActiveCountSQL = `WITH activity AS (
 SELECT labels->>'username' username,sampled_at occurred_at FROM metric_samples WHERE NULLIF(labels->>'username','') IS NOT NULL
 UNION ALL SELECT actor_username,created_at FROM audit_logs WHERE action IN ('auth.login','auth.oidc') AND result='success' AND actor_username<>''
 UNION ALL SELECT username,last_activity_at FROM managed_users WHERE last_activity_at IS NOT NULL
 UNION ALL SELECT username,COALESCE(last_activity_at,started_at,synced_at) FROM servers
) SELECT count(DISTINCT username) FROM activity WHERE occurred_at BETWEEN $1 AND $2`

const usageTopUsersSQL = `WITH samples AS (
 SELECT labels->>'username' username,
	count(*) FILTER(WHERE metric_name IN ('runtime_seconds','server_running_seconds')) runtime_sample_count,
  COALESCE(sum(value) FILTER(WHERE metric_name IN ('runtime_seconds','server_running_seconds')),0) runtime_seconds,
  COALESCE(avg(value) FILTER(WHERE metric_name IN ('cpu','cpu_cores','cpu_usage')),0) cpu_average,
  COALESCE(max(value) FILTER(WHERE metric_name IN ('cpu','cpu_cores','cpu_usage')),0) cpu_peak,
  COALESCE(avg(value) FILTER(WHERE metric_name IN ('memory','memory_bytes','memory_usage')),0) memory_average,
  COALESCE(max(value) FILTER(WHERE metric_name IN ('memory','memory_bytes','memory_usage')),0) memory_peak,
  COALESCE(avg(value) FILTER(WHERE metric_name IN ('gpu','gpu_utilization')),0) gpu_average,
  COALESCE(max(value) FILTER(WHERE metric_name IN ('gpu','gpu_utilization')),0) gpu_peak
 FROM metric_samples WHERE sampled_at BETWEEN $1 AND $2 AND NULLIF(labels->>'username','') IS NOT NULL GROUP BY 1
), server_usage AS (
 SELECT username,count(*) FILTER(WHERE status='running') servers,
  COALESCE(sum(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(COALESCE(CASE WHEN status='running' THEN $2::timestamptz END,last_activity_at,synced_at),$2::timestamptz)-GREATEST(COALESCE(started_at,synced_at),$1::timestamptz))))),0) runtime_seconds
 FROM servers WHERE COALESCE(started_at,synced_at)<=$2::timestamptz AND COALESCE(CASE WHEN status='running' THEN $2::timestamptz END,last_activity_at,synced_at)>=$1::timestamptz GROUP BY username
), logins AS (SELECT actor_username username,count(*) login_count FROM audit_logs WHERE action IN ('auth.login','auth.oidc') AND result='success' AND created_at BETWEEN $1 AND $2 AND actor_username<>'' GROUP BY actor_username),
 names AS (SELECT username FROM samples UNION SELECT username FROM server_usage UNION SELECT username FROM logins)
 SELECT names.username,COALESCE(server_usage.servers,0),COALESCE(logins.login_count,0),CASE WHEN COALESCE(samples.runtime_sample_count,0)>0 THEN samples.runtime_seconds ELSE COALESCE(server_usage.runtime_seconds,0) END,COALESCE(samples.cpu_average,0),COALESCE(samples.cpu_peak,0),COALESCE(samples.memory_average,0),COALESCE(samples.memory_peak,0),COALESCE(samples.gpu_average,0),COALESCE(samples.gpu_peak,0)
 FROM names LEFT JOIN samples USING(username) LEFT JOIN server_usage USING(username) LEFT JOIN logins USING(username) ORDER BY 4 DESC,3 DESC LIMIT 20`

func (s *Store) DeleteSecret(ctx context.Context, key string) error {
	result, err := s.Pool.Exec(ctx, `DELETE FROM secrets WHERE secret_key=$1`, key)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) LLMUsage(ctx context.Context, from, to time.Time, groupBy string) (map[string]any, error) {
	var features struct {
		LLMUsageMonitoring bool `json:"llm_usage_monitoring"`
	}
	_ = s.GetSetting(ctx, "features", &features)
	if !features.LLMUsageMonitoring {
		return map[string]any{"feature_enabled": false, "data": []any{}, "top_callers": []any{}, "stale": false}, nil
	}
	if groupBy == "detail" {
		rows, err := s.Pool.Query(ctx, `SELECT COALESCE(username,'unknown'),pod_name,hub_name,model,sum(calls),sum(success_count),sum(error_count),max(latency_p50_ms),max(latency_p95_ms),sum(input_tokens),sum(output_tokens),sum(total_tokens),sum(bytes),sum(estimated_cost),max(sampled_at) FROM llm_usage_samples WHERE sampled_at BETWEEN $1 AND $2 GROUP BY 1,2,3,4 ORDER BY sum(calls) DESC`, from, to)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := []map[string]any{}
		var freshness *time.Time
		for rows.Next() {
			var username, pod, hub, model string
			var calls, success, fail int64
			var p50, p95 *float64
			var input, output, total, byteCount *int64
			var cost *float64
			var sampled time.Time
			if err := rows.Scan(&username, &pod, &hub, &model, &calls, &success, &fail, &p50, &p95, &input, &output, &total, &byteCount, &cost, &sampled); err != nil {
				return nil, err
			}
			rate := float64(0)
			if calls > 0 {
				rate = float64(success) / float64(calls)
			}
			items = append(items, map[string]any{"username": username, "pod_name": pod, "hub_name": hub, "model": model, "calls": calls, "success": success, "errors": fail, "success_rate": rate, "latency_p50_ms": p50, "latency_p95_ms": p95, "input_tokens": input, "output_tokens": output, "total_tokens": total, "bytes": byteCount, "estimated_cost": cost, "sampled_at": sampled})
			if freshness == nil || sampled.After(*freshness) {
				t := sampled
				freshness = &t
			}
		}
		trendRows, trendErr := s.Pool.Query(ctx, `SELECT date_trunc('hour',sampled_at),sum(calls),sum(total_tokens),sum(estimated_cost) FROM llm_usage_samples WHERE sampled_at BETWEEN $1 AND $2 GROUP BY 1 ORDER BY 1`, from, to)
		trend := []map[string]any{}
		if trendErr == nil {
			for trendRows.Next() {
				var bucket time.Time
				var calls int64
				var tokens *int64
				var cost *float64
				if trendRows.Scan(&bucket, &calls, &tokens, &cost) == nil {
					trend = append(trend, map[string]any{"bucket": bucket, "calls": calls, "total_tokens": tokens, "estimated_cost": cost})
				}
			}
			trendRows.Close()
		}
		return map[string]any{"feature_enabled": true, "from": from, "to": to, "group_by": "detail", "data": items, "top_callers": items, "summary": summarizeLLMItems(items), "breakdown": buildLLMBreakdown(items), "usage_trend": trend, "data_freshness": freshness, "stale": s.llmUsageStale(ctx, freshness)}, rows.Err()
	}
	groupColumns := map[string]string{"user": "COALESCE(username,'unknown')", "pod": "pod_name", "model": "model"}
	expr, ok := groupColumns[groupBy]
	if !ok {
		groupBy, expr = "user", groupColumns["user"]
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+expr+` grouping,sum(calls),sum(success_count),sum(error_count),sum(input_tokens),sum(output_tokens),sum(total_tokens),sum(bytes),sum(estimated_cost),max(sampled_at) FROM llm_usage_samples WHERE sampled_at BETWEEN $1 AND $2 GROUP BY 1 ORDER BY sum(calls) DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	var freshness *time.Time
	for rows.Next() {
		var group string
		var calls, success, fail int64
		var input, output, total, bytes *int64
		var cost any
		var sampled time.Time
		if err := rows.Scan(&group, &calls, &success, &fail, &input, &output, &total, &bytes, &cost, &sampled); err != nil {
			return nil, err
		}
		if freshness == nil || sampled.After(*freshness) {
			t := sampled
			freshness = &t
		}
		items = append(items, map[string]any{"group": group, "calls": calls, "success": success, "errors": fail, "input_tokens": input, "output_tokens": output, "total_tokens": total, "bytes": bytes, "estimated_cost": cost, "sampled_at": sampled})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	detailView, err := s.LLMUsage(ctx, from, to, "detail")
	if err != nil {
		return nil, err
	}
	return map[string]any{"feature_enabled": true, "from": from, "to": to, "group_by": groupBy, "data": items, "top_callers": items, "summary": detailView["summary"], "breakdown": detailView["breakdown"], "usage_trend": detailView["usage_trend"], "data_freshness": detailView["data_freshness"], "stale": detailView["stale"]}, nil
}

func (s *Store) llmUsageStale(ctx context.Context, freshness *time.Time) bool {
	if freshness == nil {
		return true
	}
	var cfg struct {
		StaleSeconds int `json:"stale_seconds"`
	}
	_ = s.GetSetting(ctx, "llm_usage", &cfg)
	if cfg.StaleSeconds < 30 || cfg.StaleSeconds > 86400 {
		cfg.StaleSeconds = 300
	}
	return time.Since(*freshness) > time.Duration(cfg.StaleSeconds)*time.Second
}

func summarizeLLMItems(items []map[string]any) map[string]any {
	var calls, success, failures int64
	var input, output, total, byteCount int64
	var cost, p50, p95 float64
	hasInput, hasOutput, hasTotal, hasBytes := false, false, false, false
	hasCost, hasP50, hasP95 := false, false, false
	for _, item := range items {
		calls += int64(anyNumber(item["calls"]))
		success += int64(anyNumber(item["success"]))
		failures += int64(anyNumber(item["errors"]))
		if value, ok := optionalNumber(item["input_tokens"]); ok {
			input += int64(value)
			hasInput = true
		}
		if value, ok := optionalNumber(item["output_tokens"]); ok {
			output += int64(value)
			hasOutput = true
		}
		if value, ok := optionalNumber(item["total_tokens"]); ok {
			total += int64(value)
			hasTotal = true
		}
		if value, ok := optionalNumber(item["bytes"]); ok {
			byteCount += int64(value)
			hasBytes = true
		}
		if value, ok := optionalNumber(item["estimated_cost"]); ok {
			cost += value
			hasCost = true
		}
		if value, ok := optionalNumber(item["latency_p50_ms"]); ok {
			if !hasP50 || value > p50 {
				p50 = value
			}
			hasP50 = true
		}
		if value, ok := optionalNumber(item["latency_p95_ms"]); ok {
			if !hasP95 || value > p95 {
				p95 = value
			}
			hasP95 = true
		}
	}
	summary := map[string]any{"calls": calls, "success": success, "errors": failures, "success_rate": float64(0), "input_tokens": nil, "output_tokens": nil, "total_tokens": nil, "bytes": nil, "estimated_cost": nil, "latency_p50_ms": nil, "latency_p95_ms": nil}
	if calls > 0 {
		summary["success_rate"] = float64(success) / float64(calls)
	}
	if hasInput {
		summary["input_tokens"] = input
	}
	if hasOutput {
		summary["output_tokens"] = output
	}
	if hasTotal {
		summary["total_tokens"] = total
	}
	if hasBytes {
		summary["bytes"] = byteCount
	}
	if hasCost {
		summary["estimated_cost"] = cost
	}
	if hasP50 {
		summary["latency_p50_ms"] = p50
	}
	if hasP95 {
		summary["latency_p95_ms"] = p95
	}
	return summary
}

func buildLLMBreakdown(items []map[string]any) []map[string]any {
	users := map[string]map[string][]map[string]any{}
	for _, item := range items {
		username, _ := item["username"].(string)
		pod, _ := item["pod_name"].(string)
		if users[username] == nil {
			users[username] = map[string][]map[string]any{}
		}
		users[username][pod] = append(users[username][pod], item)
	}
	userNames := make([]string, 0, len(users))
	for name := range users {
		userNames = append(userNames, name)
	}
	sort.Strings(userNames)
	result := make([]map[string]any, 0, len(userNames))
	for _, username := range userNames {
		podsMap := users[username]
		podNames := make([]string, 0, len(podsMap))
		all := []map[string]any{}
		for pod, models := range podsMap {
			podNames = append(podNames, pod)
			all = append(all, models...)
		}
		sort.Strings(podNames)
		pods := make([]map[string]any, 0, len(podNames))
		for _, pod := range podNames {
			models := podsMap[pod]
			sort.SliceStable(models, func(i, j int) bool {
				left, _ := models[i]["model"].(string)
				right, _ := models[j]["model"].(string)
				return left < right
			})
			pods = append(pods, map[string]any{"pod_name": pod, "summary": summarizeLLMItems(models), "models": models})
		}
		result = append(result, map[string]any{"username": username, "summary": summarizeLLMItems(all), "pods": pods})
	}
	return result
}

func anyNumber(value any) float64 { number, _ := optionalNumber(value); return number }
func optionalNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case *float64:
		if v != nil {
			return *v, true
		}
	case *int64:
		if v != nil {
			return float64(*v), true
		}
	case json.Number:
		number, err := v.Float64()
		return number, err == nil
	}
	return 0, false
}

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) }
