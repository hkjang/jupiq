package store

import (
	"context"
	"errors"
	"time"
)

const userDetailCurrentServerCondition = `(s.status='running' OR s.status='starting' OR s.status LIKE 'pending_%')`

// UserHubIdentity is the safe, metadata-only view of one username as observed
// by a JupyterHub. The provider's raw response is deliberately not exposed.
type UserHubIdentity struct {
	ID             int64      `json:"id"`
	HubID          int64      `json:"hub_id"`
	HubName        string     `json:"hub_name"`
	Network        string     `json:"network"`
	HubStatus      string     `json:"hub_status"`
	HubVersion     string     `json:"hub_version"`
	Username       string     `json:"username"`
	DisplayName    string     `json:"display_name"`
	Department     string     `json:"department"`
	Admin          bool       `json:"admin"`
	Active         bool       `json:"active"`
	LastActivityAt *time.Time `json:"last_activity_at"`
	SyncedAt       time.Time  `json:"synced_at"`
}

// UserServerSummary excludes the provider raw document and contains no
// notebook source, cell output, prompt, or response body.
type UserServerSummary struct {
	ID             int64      `json:"id"`
	SessionID      int64      `json:"session_id"`
	HubID          int64      `json:"hub_id"`
	HubName        string     `json:"hub_name"`
	Network        string     `json:"network"`
	Username       string     `json:"username"`
	ServerName     string     `json:"server_name"`
	Status         string     `json:"status"`
	StartedAt      *time.Time `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at"`
	LastActivityAt *time.Time `json:"last_activity_at"`
	URL            string     `json:"url"`
	NodeName       string     `json:"node_name"`
	PodName        string     `json:"pod_name"`
	Image          string     `json:"image"`
	CPUCores       *float64   `json:"cpu_cores"`
	MemoryBytes    *int64     `json:"memory_bytes"`
	GPUCount       *int       `json:"gpu_count"`
	SyncedAt       time.Time  `json:"synced_at"`
}

type UserTimelineEvent struct {
	ID            string    `json:"id"`
	Category      string    `json:"category"`
	Action        string    `json:"action"`
	ActorUsername string    `json:"actor_username"`
	ResourceType  string    `json:"resource_type"`
	ResourceID    string    `json:"resource_id"`
	HubID         *int64    `json:"hub_id,omitempty"`
	HubName       string    `json:"hub_name,omitempty"`
	ServerID      *int64    `json:"server_id,omitempty"`
	Result        string    `json:"result"`
	Reason        string    `json:"reason,omitempty"`
	OccurredAt    time.Time `json:"occurred_at"`
}

type UserDetailPage struct {
	Page  int `json:"page"`
	Limit int `json:"limit"`
	Total int `json:"total"`
}

type UserServerHistory struct {
	Current      []UserServerSummary `json:"current"`
	CurrentTotal int                 `json:"current_total"`
	History      []UserServerSummary `json:"history"`
	Meta         UserDetailPage      `json:"meta"`
}

type UserTimeline struct {
	Items []UserTimelineEvent `json:"items"`
	Meta  UserDetailPage      `json:"meta"`
}

type UserUsagePeriod struct {
	From             time.Time `json:"from"`
	To               time.Time `json:"to"`
	SampleCount      int64     `json:"sample_count"`
	LoginCount       int64     `json:"login_count"`
	ServerCount      int64     `json:"server_count"`
	ServerStartCount int64     `json:"server_start_count"`
	CurrentServers   int64     `json:"current_servers"`
	RuntimeSeconds   float64   `json:"runtime_seconds"`
	CPUAverage       float64   `json:"cpu_average"`
	CPUPeak          float64   `json:"cpu_peak"`
	MemoryAverage    float64   `json:"memory_average"`
	MemoryPeak       float64   `json:"memory_peak"`
	GPUAverage       float64   `json:"gpu_average,omitempty"`
	GPUPeak          float64   `json:"gpu_peak,omitempty"`
	// Consumption, integrated from the durable hourly rollup rather than
	// averaged from raw samples. The averages above describe how hard the pods
	// worked; these describe how much was used, and they remain correct once
	// the raw samples have aged out.
	CPUCoreHours   float64    `json:"cpu_core_hours"`
	MemoryGBHours  float64    `json:"memory_gb_hours"`
	GPUHours       float64    `json:"gpu_hours,omitempty"`
	ObservedRatio  float64    `json:"observed_ratio"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

type UserDetail struct {
	Username       string                     `json:"username"`
	User           *User                      `json:"user"`
	Hubs           []UserHubIdentity          `json:"hubs"`
	Servers        UserServerHistory          `json:"servers"`
	Timeline       UserTimeline               `json:"timeline"`
	Usage          map[string]UserUsagePeriod `json:"usage"`
	LLMUsage       map[string]any             `json:"llm_usage"`
	FeatureEnabled map[string]bool            `json:"feature_enabled"`
	DataPolicy     map[string]bool            `json:"data_policy"`
}

type UserDetailOptions struct {
	Page       int
	Limit      int
	IncludeLLM bool
	Now        time.Time
}

func (s *Store) GetUserDetail(ctx context.Context, username string, options UserDetailOptions) (UserDetail, error) {
	page, limit, _ := pageBounds(options.Page, options.Limit)
	if limit > 100 {
		limit = 100
	}
	now := options.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}

	detail := UserDetail{
		Username: username,
		Hubs:     []UserHubIdentity{},
		Servers: UserServerHistory{
			Current: []UserServerSummary{},
			History: []UserServerSummary{},
			Meta:    UserDetailPage{Page: page, Limit: limit},
		},
		Timeline: UserTimeline{Items: []UserTimelineEvent{}, Meta: UserDetailPage{Page: page, Limit: limit}},
		Usage:    map[string]UserUsagePeriod{},
		DataPolicy: map[string]bool{
			"metadata_only":    true,
			"prompt_collected": false,
			"code_collected":   false,
		},
	}

	var centralID *int64
	credential, err := s.GetUserByUsername(ctx, username)
	if err == nil {
		user := credential.User
		detail.User = &user
		id := user.ID
		centralID = &id
	} else if !errors.Is(err, ErrNotFound) {
		return UserDetail{}, err
	}

	detail.Hubs, err = s.userHubIdentities(ctx, username)
	if err != nil {
		return UserDetail{}, err
	}
	detail.Servers.Current, detail.Servers.CurrentTotal, err = s.userServers(ctx, username, true, 1, 200)
	if err != nil {
		return UserDetail{}, err
	}
	detail.Servers.History, detail.Servers.Meta.Total, err = s.userServers(ctx, username, false, page, limit)
	if err != nil {
		return UserDetail{}, err
	}
	if detail.User == nil && len(detail.Hubs) == 0 && detail.Servers.CurrentTotal == 0 && detail.Servers.Meta.Total == 0 {
		return UserDetail{}, ErrNotFound
	}

	detail.Timeline.Items, detail.Timeline.Meta.Total, err = s.userTimeline(ctx, username, centralID, page, limit)
	if err != nil {
		return UserDetail{}, err
	}
	var features map[string]bool
	_ = s.GetSetting(ctx, "features", &features)
	detail.Usage, err = s.userUsagePeriods(ctx, username, now, features["gpu_monitoring"])
	if err != nil {
		return UserDetail{}, err
	}
	detail.FeatureEnabled = map[string]bool{
		"gpu_monitoring":       features["gpu_monitoring"],
		"llm_usage_monitoring": features["llm_usage_monitoring"],
	}
	if !features["gpu_monitoring"] {
		for index := range detail.Servers.Current {
			detail.Servers.Current[index].GPUCount = nil
		}
		for index := range detail.Servers.History {
			detail.Servers.History[index].GPUCount = nil
		}
		for key, period := range detail.Usage {
			period.GPUAverage, period.GPUPeak = 0, 0
			detail.Usage[key] = period
		}
	}
	detail.LLMUsage, err = s.userLLMMetadata(ctx, username, now.Add(-30*24*time.Hour), now, page, limit, options.IncludeLLM, features["llm_usage_monitoring"])
	if err != nil {
		return UserDetail{}, err
	}
	return detail, nil
}

func (s *Store) userHubIdentities(ctx context.Context, username string) ([]UserHubIdentity, error) {
	rows, err := s.Pool.Query(ctx, `SELECT u.id,u.hub_id,h.name,h.network,h.status,h.version,u.username,u.display_name,u.department,u.admin,u.active,u.last_activity_at,u.synced_at
		FROM managed_users u JOIN hubs h ON h.id=u.hub_id
		WHERE lower(u.username)=lower($1) ORDER BY h.name,u.id LIMIT 200`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []UserHubIdentity{}
	for rows.Next() {
		var item UserHubIdentity
		if err := rows.Scan(&item.ID, &item.HubID, &item.HubName, &item.Network, &item.HubStatus, &item.HubVersion, &item.Username, &item.DisplayName, &item.Department, &item.Admin, &item.Active, &item.LastActivityAt, &item.SyncedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) userServers(ctx context.Context, username string, current bool, page, limit int) ([]UserServerSummary, int, error) {
	offset := (page - 1) * limit
	condition := `ss.ended_at IS NOT NULL`
	statusExpression := `'stopped'`
	if current {
		condition = `ss.ended_at IS NULL`
		statusExpression = `COALESCE(s.status,'unknown')`
	}
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM server_sessions ss WHERE lower(ss.username)=lower($1) AND `+condition, username).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT COALESCE(s.id,0),ss.id,ss.hub_id,h.name,h.network,ss.username,ss.server_name,`+statusExpression+`,ss.started_at,ss.ended_at,ss.last_activity_at,
		COALESCE(s.url,''),COALESCE(s.node_name,''),COALESCE(s.pod_name,''),COALESCE(s.image,''),s.cpu_cores,s.memory_bytes,s.gpu_count,COALESCE(ss.ended_at,s.synced_at,ss.created_at)
		FROM server_sessions ss JOIN hubs h ON h.id=ss.hub_id LEFT JOIN servers s ON s.id=ss.server_id
		WHERE lower(ss.username)=lower($1) AND `+condition+` ORDER BY COALESCE(ss.ended_at,ss.started_at) DESC,ss.id DESC LIMIT $2 OFFSET $3`, username, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []UserServerSummary{}
	for rows.Next() {
		var item UserServerSummary
		if err := rows.Scan(&item.ID, &item.SessionID, &item.HubID, &item.HubName, &item.Network, &item.Username, &item.ServerName, &item.Status, &item.StartedAt, &item.EndedAt, &item.LastActivityAt, &item.URL, &item.NodeName, &item.PodName, &item.Image, &item.CPUCores, &item.MemoryBytes, &item.GPUCount, &item.SyncedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (s *Store) userTimeline(ctx context.Context, username string, centralID *int64, page, limit int) ([]UserTimelineEvent, int, error) {
	offset := (page - 1) * limit
	rows, err := s.Pool.Query(ctx, `WITH owned_servers AS (
		SELECT s.id,s.hub_id,h.name hub_name FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE lower(s.username)=lower($1)
	), owned_identities AS (
		SELECT u.id FROM managed_users u WHERE lower(u.username)=lower($1)
	), events AS (
		SELECT 'audit:'||a.id::text id,'audit' category,a.action,a.actor_username,a.resource_type,a.resource_id,
		       os.hub_id,COALESCE(os.hub_name,'') hub_name,os.id server_id,a.result,a.reason,a.created_at occurred_at
		FROM audit_logs a LEFT JOIN owned_servers os ON a.resource_type='server' AND a.resource_id=os.id::text
		WHERE lower(a.actor_username)=lower($1)
		   OR ($2::bigint IS NOT NULL AND a.actor_user_id=$2::bigint)
		   OR (a.resource_type='user' AND (lower(a.resource_id)=lower($1) OR ($2::bigint IS NOT NULL AND a.resource_id=($2::bigint)::text)))
		   OR (a.resource_type='managed_user' AND a.resource_id IN (SELECT id::text FROM owned_identities))
		   OR (a.resource_type='server' AND os.id IS NOT NULL)
		UNION ALL
		SELECT 'server-start:'||ss.id::text,'server','server.started','', 'server',COALESCE(ss.server_id::text,''),ss.hub_id,h.name,ss.server_id,
		       CASE WHEN ss.ended_at IS NULL THEN 'running' ELSE 'stopped' END,'',ss.started_at
		FROM server_sessions ss JOIN hubs h ON h.id=ss.hub_id WHERE lower(ss.username)=lower($1)
		UNION ALL
		SELECT 'server-stop:'||ss.id::text,'server','server.stopped','', 'server',COALESCE(ss.server_id::text,''),ss.hub_id,h.name,ss.server_id,'stopped','',ss.ended_at
		FROM server_sessions ss JOIN hubs h ON h.id=ss.hub_id WHERE lower(ss.username)=lower($1) AND ss.ended_at IS NOT NULL
		UNION ALL
		SELECT 'server-activity:'||ss.id::text,'activity','server.activity','', 'server',COALESCE(ss.server_id::text,''),ss.hub_id,h.name,ss.server_id,
		       CASE WHEN ss.ended_at IS NULL THEN 'running' ELSE 'stopped' END,'',ss.last_activity_at
		FROM server_sessions ss JOIN hubs h ON h.id=ss.hub_id WHERE lower(ss.username)=lower($1) AND ss.last_activity_at IS NOT NULL
		UNION ALL
		SELECT 'hub-activity:'||u.id::text,'activity','hub.activity','', 'managed_user',u.id::text,u.hub_id,h.name,NULL::bigint,
		       CASE WHEN u.active THEN 'active' ELSE 'inactive' END,'',u.last_activity_at
		FROM managed_users u JOIN hubs h ON h.id=u.hub_id WHERE lower(u.username)=lower($1) AND u.last_activity_at IS NOT NULL
	), totals AS (
		SELECT count(*) total FROM events
	)
	SELECT page.id,page.category,page.action,page.actor_username,page.resource_type,page.resource_id,page.hub_id,page.hub_name,page.server_id,page.result,page.reason,page.occurred_at,totals.total
	FROM totals LEFT JOIN LATERAL (
		SELECT * FROM events ORDER BY occurred_at DESC,id DESC LIMIT $3 OFFSET $4
	) page ON true
	ORDER BY page.occurred_at DESC,page.id DESC`, username, centralID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []UserTimelineEvent{}
	total := 0
	for rows.Next() {
		var id, category, action, actor, resourceType, resourceID, hubName, result, reason *string
		var hubID, serverID *int64
		var occurredAt *time.Time
		if err := rows.Scan(&id, &category, &action, &actor, &resourceType, &resourceID, &hubID, &hubName, &serverID, &result, &reason, &occurredAt, &total); err != nil {
			return nil, 0, err
		}
		if id != nil && occurredAt != nil {
			items = append(items, UserTimelineEvent{
				ID: stringValue(id), Category: stringValue(category), Action: stringValue(action), ActorUsername: stringValue(actor),
				ResourceType: stringValue(resourceType), ResourceID: stringValue(resourceID), HubID: hubID, HubName: stringValue(hubName),
				ServerID: serverID, Result: stringValue(result), Reason: stringValue(reason), OccurredAt: *occurredAt,
			})
		}
	}
	return items, total, rows.Err()
}

func (s *Store) userUsagePeriods(ctx context.Context, username string, now time.Time, includeGPU bool) (map[string]UserUsagePeriod, error) {
	rows, err := s.Pool.Query(ctx, `WITH periods(label,from_ts,sort_order) AS (
		VALUES ('day',$2::timestamptz,1),('week',$3::timestamptz,2),('month',$4::timestamptz,3)
	), metrics AS (
		SELECT p.label,count(m.id) sample_count,
		       count(m.id) FILTER(WHERE m.metric_name IN ('runtime_seconds','server_running_seconds')) runtime_sample_count,
		       COALESCE(sum(m.value) FILTER(WHERE m.metric_name IN ('runtime_seconds','server_running_seconds')),0)::double precision metric_runtime,
		       COALESCE(avg(m.value) FILTER(WHERE m.metric_name IN ('cpu','cpu_cores','cpu_usage')),0)::double precision cpu_average,
		       COALESCE(max(m.value) FILTER(WHERE m.metric_name IN ('cpu','cpu_cores','cpu_usage')),0)::double precision cpu_peak,
		       COALESCE(avg(m.value) FILTER(WHERE m.metric_name IN ('memory','memory_bytes','memory_usage')),0)::double precision memory_average,
		       COALESCE(max(m.value) FILTER(WHERE m.metric_name IN ('memory','memory_bytes','memory_usage')),0)::double precision memory_peak,
		       COALESCE(avg(m.value) FILTER(WHERE m.metric_name IN ('gpu','gpu_utilization')),0)::double precision gpu_average,
		       COALESCE(max(m.value) FILTER(WHERE m.metric_name IN ('gpu','gpu_utilization')),0)::double precision gpu_peak,
		       max(m.sampled_at) last_activity_at
		FROM periods p LEFT JOIN metric_samples m ON lower(m.labels->>'username')=lower($1) AND m.sampled_at BETWEEN p.from_ts AND $5
		 AND ($6 OR m.metric_kind<>'gpu')
		GROUP BY p.label
		), server_usage AS (
			SELECT p.label,count(ss.id) server_count,
			       count(ss.id) FILTER(WHERE ss.started_at BETWEEN p.from_ts AND $5) server_start_count,
			       count(ss.id) FILTER(WHERE ss.ended_at IS NULL) current_servers,
			       COALESCE(sum(CASE WHEN ss.id IS NULL THEN 0 ELSE GREATEST(0,EXTRACT(EPOCH FROM (
			         LEAST(COALESCE(ss.ended_at,$5::timestamptz),$5::timestamptz)-GREATEST(ss.started_at,p.from_ts)))) END),0)::double precision server_runtime,
			       max(COALESCE(ss.last_activity_at,ss.ended_at,ss.started_at)) last_activity_at
			FROM periods p LEFT JOIN server_sessions ss ON lower(ss.username)=lower($1)
			 AND ss.started_at<=$5 AND COALESCE(ss.ended_at,$5::timestamptz)>=p.from_ts
			GROUP BY p.label
	), consumption AS (
		SELECT p.label,
		       COALESCE(sum(r.cpu_core_seconds),0)::double precision cpu_core_seconds,
		       COALESCE(sum(r.memory_byte_seconds),0)::double precision memory_byte_seconds,
		       COALESCE(sum(r.gpu_seconds),0)::double precision gpu_seconds,
		       COALESCE(sum(r.covered_seconds),0)::double precision covered_seconds,
		       COALESCE(sum(r.runtime_seconds),0)::double precision rollup_runtime
		FROM periods p LEFT JOIN resource_usage_hourly r
		  ON lower(r.username)=lower($1) AND r.bucket >= p.from_ts AND r.bucket < $5
		GROUP BY p.label
	), logins AS (
		SELECT p.label,count(a.id) login_count,max(a.created_at) last_activity_at
		FROM periods p LEFT JOIN audit_logs a ON lower(a.actor_username)=lower($1)
		 AND a.action IN ('auth.login','auth.oidc') AND a.result='success' AND a.created_at BETWEEN p.from_ts AND $5
		GROUP BY p.label
	)
	SELECT p.label,p.from_ts,$5::timestamptz,m.sample_count,l.login_count,s.server_count,s.server_start_count,s.current_servers,
	       CASE WHEN m.runtime_sample_count>0 THEN m.metric_runtime ELSE s.server_runtime END,m.cpu_average,m.cpu_peak,m.memory_average,m.memory_peak,m.gpu_average,m.gpu_peak,
	       GREATEST(m.last_activity_at,s.last_activity_at,l.last_activity_at),
	       c.cpu_core_seconds,c.memory_byte_seconds,c.gpu_seconds,c.covered_seconds,c.rollup_runtime
	FROM periods p JOIN metrics m USING(label) JOIN server_usage s USING(label) JOIN logins l USING(label) JOIN consumption c USING(label)
		ORDER BY p.sort_order`, username, now.Add(-24*time.Hour), now.Add(-7*24*time.Hour), now.Add(-30*24*time.Hour), now, includeGPU)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]UserUsagePeriod{}
	for rows.Next() {
		var label string
		var period UserUsagePeriod
		var cpuSeconds, memorySeconds, gpuSeconds, coveredSeconds, rollupRuntime float64
		if err := rows.Scan(&label, &period.From, &period.To, &period.SampleCount, &period.LoginCount, &period.ServerCount, &period.ServerStartCount, &period.CurrentServers, &period.RuntimeSeconds, &period.CPUAverage, &period.CPUPeak, &period.MemoryAverage, &period.MemoryPeak, &period.GPUAverage, &period.GPUPeak, &period.LastActivityAt, &cpuSeconds, &memorySeconds, &gpuSeconds, &coveredSeconds, &rollupRuntime); err != nil {
			return nil, err
		}
		period.CPUCoreHours = cpuSeconds / 3600
		period.MemoryGBHours = memorySeconds / 3600 / (1 << 30)
		if includeGPU {
			period.GPUHours = gpuSeconds / 3600
		}
		period.ObservedRatio = observedRatio(coveredSeconds, rollupRuntime)
		result[label] = period
	}
	return result, rows.Err()
}

func (s *Store) userLLMMetadata(ctx context.Context, username string, from, to time.Time, page, limit int, include, featureEnabled bool) (map[string]any, error) {
	base := map[string]any{
		"included":        include,
		"feature_enabled": featureEnabled,
		"metadata_only":   true,
		"data":            []map[string]any{},
		"breakdown":       []map[string]any{},
		"usage_trend":     []map[string]any{},
		"meta":            UserDetailPage{Page: page, Limit: limit, Total: 0},
	}
	if !include || !featureEnabled {
		return base, nil
	}

	var groups, calls, success, failures int64
	var inputTokens, outputTokens, totalTokens *int64
	var p95, estimatedCost *float64
	var freshness *time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(calls),0),COALESCE(sum(success_count),0),COALESCE(sum(error_count),0),
		 sum(input_tokens),sum(output_tokens),sum(total_tokens),max(latency_p95_ms),sum(estimated_cost)::double precision,max(sampled_at)
		 FROM (SELECT pod_name,hub_name,model,sum(calls) calls,sum(success_count) success_count,sum(error_count) error_count,
		       sum(input_tokens) input_tokens,sum(output_tokens) output_tokens,sum(total_tokens) total_tokens,max(latency_p95_ms) latency_p95_ms,
		       sum(estimated_cost) estimated_cost,max(sampled_at) sampled_at
		       FROM llm_usage_samples WHERE lower(username)=lower($1) AND sampled_at BETWEEN $2 AND $3 GROUP BY pod_name,hub_name,model) grouped`, username, from, to).Scan(&groups, &calls, &success, &failures, &inputTokens, &outputTokens, &totalTokens, &p95, &estimatedCost, &freshness); err != nil {
		return nil, err
	}

	offset := (page - 1) * limit
	rows, err := s.Pool.Query(ctx, `SELECT pod_name,hub_name,model,sum(calls),sum(success_count),sum(error_count),max(latency_p50_ms),max(latency_p95_ms),
		sum(input_tokens),sum(output_tokens),sum(total_tokens),sum(bytes),sum(estimated_cost)::double precision,max(sampled_at)
		FROM llm_usage_samples WHERE lower(username)=lower($1) AND sampled_at BETWEEN $2 AND $3
		GROUP BY pod_name,hub_name,model ORDER BY sum(calls) DESC,pod_name,model LIMIT $4 OFFSET $5`, username, from, to, limit, offset)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for rows.Next() {
		var pod, hub, model string
		var itemCalls, itemSuccess, itemFailures int64
		var p50Value, p95Value *float64
		var input, output, total, bytes *int64
		var cost *float64
		var sampled time.Time
		if err := rows.Scan(&pod, &hub, &model, &itemCalls, &itemSuccess, &itemFailures, &p50Value, &p95Value, &input, &output, &total, &bytes, &cost, &sampled); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, map[string]any{
			"username": username, "pod_name": pod, "hub_name": hub, "model": model,
			"calls": itemCalls, "success": itemSuccess, "errors": itemFailures, "success_rate": observedSuccessRate(itemSuccess, itemFailures), "status_observed_calls": itemSuccess + itemFailures,
			"latency_p50_ms": p50Value, "latency_p95_ms": p95Value,
			"input_tokens": input, "output_tokens": output, "total_tokens": total, "bytes": bytes,
			"estimated_cost": cost, "sampled_at": sampled,
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	trendRows, err := s.Pool.Query(ctx, `SELECT date_trunc('day',sampled_at),sum(calls),sum(success_count),sum(error_count),sum(total_tokens),sum(estimated_cost)::double precision
		FROM llm_usage_samples WHERE lower(username)=lower($1) AND sampled_at BETWEEN $2 AND $3 GROUP BY 1 ORDER BY 1`, username, from, to)
	if err != nil {
		return nil, err
	}
	trend := []map[string]any{}
	for trendRows.Next() {
		var bucket time.Time
		var trendCalls, trendSuccess, trendFailures int64
		var tokens *int64
		var cost *float64
		if err := trendRows.Scan(&bucket, &trendCalls, &trendSuccess, &trendFailures, &tokens, &cost); err != nil {
			trendRows.Close()
			return nil, err
		}
		trend = append(trend, map[string]any{"bucket": bucket, "calls": trendCalls, "success": trendSuccess, "errors": trendFailures, "total_tokens": tokens, "estimated_cost": cost})
	}
	if err := trendRows.Err(); err != nil {
		trendRows.Close()
		return nil, err
	}
	trendRows.Close()

	base["from"], base["to"] = from, to
	base["data"], base["top_callers"], base["breakdown"], base["usage_trend"] = items, items, buildLLMBreakdown(items), trend
	base["summary"] = map[string]any{
		"calls": calls, "success": success, "errors": failures, "success_rate": observedSuccessRate(success, failures), "status_observed_calls": success + failures,
		"input_tokens": inputTokens, "output_tokens": outputTokens, "total_tokens": totalTokens, // gitleaks:allow -- metric field names, not credentials
		"latency_p95_ms": p95, "estimated_cost": estimatedCost,
	}
	base["meta"] = UserDetailPage{Page: page, Limit: limit, Total: int(groups)}
	base["data_freshness"], base["stale"] = freshness, s.llmUsageStale(ctx, freshness)
	return base, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
