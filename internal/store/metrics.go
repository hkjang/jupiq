package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
)

func (s *Store) SaveLLMMetricPoints(ctx context.Context, metricName, podPattern, pathMatcher string, points []integration.MetricPoint) error {
	re, err := integration.CompilePodUsernamePattern(podPattern)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, point := range points {
		pod := point.Labels["pod"]
		if pod == "" {
			pod = point.Labels["pod_name"]
		}
		username, ok := integration.UsernameFromPod(re, pod)
		if !ok {
			continue
		}
		path := point.Labels["path"]
		if path == "" {
			path = point.Labels["route"]
		}
		if pathMatcher != "" && path != "" && path != pathMatcher {
			continue
		}
		var calls, successCount, errorCount int64
		var latencyP50, latencyP95 *float64
		var inputTokens, outputTokens, totalTokens, byteCount *int64
		var estimatedCost *float64
		switch metricName {
		case "calls", "request_count", "requests":
			calls = int64(point.Value)
			status := point.Labels["status"]
			if strings.HasPrefix(status, "2") || status == "success" {
				successCount = calls
			} else if status != "" {
				errorCount = calls
			}
		case "latency_p50_ms", "p50_latency_ms":
			v := point.Value
			latencyP50 = &v
		case "latency_p95_ms", "p95_latency_ms":
			v := point.Value
			latencyP95 = &v
		case "input_tokens":
			v := int64(point.Value)
			inputTokens = &v
		case "output_tokens":
			v := int64(point.Value)
			outputTokens = &v
		case "total_tokens":
			v := int64(point.Value)
			totalTokens = &v
		case "bytes":
			v := int64(point.Value)
			byteCount = &v
		case "estimated_cost":
			v := point.Value
			estimatedCost = &v
		default:
			continue
		}
		labels, _ := json.Marshal(llmSafeLabels(point.Labels))
		_, err = tx.Exec(ctx, `INSERT INTO llm_usage_samples(source,username,pod_name,hub_name,model,calls,success_count,error_count,latency_p50_ms,latency_p95_ms,input_tokens,output_tokens,total_tokens,bytes,estimated_cost,sampled_at,labels) VALUES('prometheus',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, username, pod, point.Labels["hub"], point.Labels["model"], calls, successCount, errorCount, latencyP50, latencyP95, inputTokens, outputTokens, totalTokens, byteCount, estimatedCost, point.Timestamp, labels)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func llmSafeLabels(labels map[string]string) map[string]string {
	safe := make(map[string]string, 8)
	for _, key := range []string{"pod", "pod_name", "path", "route", "status", "model", "hub", "network"} {
		if value := strings.TrimSpace(labels[key]); value != "" {
			safe[key] = value
		}
	}
	return safe
}

func (s *Store) SaveMetricPoints(ctx context.Context, source, name string, points []integration.MetricPoint) error {
	if len(points) == 0 {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, point := range points {
		if point.Labels == nil {
			point.Labels = map[string]string{}
		}
		pod := point.Labels["pod"]
		if pod == "" {
			pod = point.Labels["pod_name"]
		}
		if pod != "" {
			var username, hub, network, department, project string
			_ = tx.QueryRow(ctx, `SELECT s.username,h.name,h.network,COALESCE(u.department,''),COALESCE(s.raw->>'project','') FROM servers s JOIN hubs h ON h.id=s.hub_id LEFT JOIN managed_users u ON u.id=s.managed_user_id WHERE s.pod_name=$1 ORDER BY (s.status='running') DESC,s.synced_at DESC LIMIT 1`, pod).Scan(&username, &hub, &network, &department, &project)
			for key, value := range map[string]string{"username": username, "hub": hub, "network": network, "department": department, "project": project} {
				if point.Labels[key] == "" && value != "" {
					point.Labels[key] = value
				}
			}
			switch name {
			case "cpu_cores":
				_, _ = tx.Exec(ctx, `UPDATE servers SET cpu_cores=$2,raw=jsonb_set(raw,'{resource_sampled_at}',to_jsonb($3::timestamptz),true) WHERE pod_name=$1`, pod, point.Value, point.Timestamp)
			case "memory_bytes":
				_, _ = tx.Exec(ctx, `UPDATE servers SET memory_bytes=$2,raw=jsonb_set(raw,'{resource_sampled_at}',to_jsonb($3::timestamptz),true) WHERE pod_name=$1`, pod, int64(point.Value), point.Timestamp)
			case "gpu_utilization", "vram_bytes":
				_, _ = tx.Exec(ctx, `UPDATE servers SET raw=jsonb_set(jsonb_set(raw,$2::text[],to_jsonb($3::double precision),true),'{resource_sampled_at}',to_jsonb($4::timestamptz),true) WHERE pod_name=$1`, pod, []string{name}, point.Value, point.Timestamp)
			}
		}
		labels, _ := json.Marshal(point.Labels)
		if _, err := tx.Exec(ctx, `INSERT INTO metric_samples(source,metric_name,labels,value,sampled_at) VALUES($1,$2,$3,$4,$5)`, source, name, labels, point.Value, point.Timestamp); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) UpdatePods(ctx context.Context, pods []integration.Pod, podPatterns ...string) error {
	var podPattern string
	if len(podPatterns) > 0 {
		podPattern = podPatterns[0]
	}
	re, _ := integration.CompilePodUsernamePattern(podPattern)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, pod := range pods {
		result, err := tx.Exec(ctx, `UPDATE servers SET node_name=$2,raw=jsonb_set(raw,'{kubernetes}', $3::jsonb,true) WHERE pod_name=$1`, pod.Name, pod.Node, normalizeJSON(pod))
		if err != nil {
			return err
		}
		if result.RowsAffected() > 0 {
			continue
		}
		username := firstLabel(pod.Labels, "hub.jupyter.org/username", "jupyterhub-user", "username", "user")
		if username == "" && re != nil {
			username, _ = integration.UsernameFromPod(re, pod.Name)
		}
		if username == "" {
			continue
		}
		hubLabel := firstLabel(pod.Labels, "hub", "hub_name", "jupyterhub")
		var serverID int64
		if hubLabel != "" {
			_ = tx.QueryRow(ctx, `SELECT s.id FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE s.status='running' AND s.username=$1 AND (h.name=$2 OR h.network=$2) ORDER BY s.synced_at DESC LIMIT 1`, username, hubLabel).Scan(&serverID)
		} else {
			var matches int
			_ = tx.QueryRow(ctx, `SELECT count(*),COALESCE(min(id),0) FROM servers WHERE status='running' AND username=$1`, username).Scan(&matches, &serverID)
			if matches != 1 {
				serverID = 0
			}
		}
		if serverID != 0 {
			if _, err := tx.Exec(ctx, `UPDATE servers SET pod_name=$2,node_name=$3,raw=jsonb_set(raw,'{kubernetes}',$4::jsonb,true) WHERE id=$1`, serverID, pod.Name, pod.Node, normalizeJSON(pod)); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func firstLabel(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(labels[key]); value != "" {
			return value
		}
	}
	return ""
}

func (s *Store) Metrics(ctx context.Context, from, to time.Time, metric string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := s.Pool.Query(ctx, `SELECT source,metric_name,labels,value,sampled_at FROM metric_samples WHERE sampled_at BETWEEN $1 AND $2 AND ($3='' OR metric_name=$3) ORDER BY sampled_at DESC LIMIT $4`, from, to, metric, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var source, name string
		var labels []byte
		var value float64
		var sampled time.Time
		if err := rows.Scan(&source, &name, &labels, &value, &sampled); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"source": source, "metric": name, "labels": json.RawMessage(labels), "value": value, "sampled_at": sampled})
	}
	return items, rows.Err()
}

func (s *Store) PruneMetrics(ctx context.Context, rawRetentionDays, llmRetentionDays int) error {
	if rawRetentionDays < 1 {
		rawRetentionDays = 30
	}
	if llmRetentionDays < 1 {
		llmRetentionDays = 30
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM metric_samples WHERE sampled_at < now()-($1 * interval '1 day')`, rawRetentionDays)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM llm_usage_samples WHERE sampled_at < now()-($1 * interval '1 day')`, llmRetentionDays)
	return err
}

func (s *Store) RecordAIUsage(ctx context.Context, userID int64, provider, model, status, requestID string, latency time.Duration) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO ai_usage(user_id,provider,model,status,latency_ms,request_id) VALUES($1,$2,$3,$4,$5,$6)`, userID, provider, model, status, latency.Milliseconds(), requestID)
	return err
}
