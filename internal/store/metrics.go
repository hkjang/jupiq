package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/jackc/pgx/v5"
)

func (s *Store) SaveLLMMetricPoints(ctx context.Context, metricName, podPattern, pathMatcher string, points []integration.MetricPoint) error {
	_, err := s.saveLLMMetricPoints(ctx, metricName, podPattern, pathMatcher, points, nil)
	return err
}

func (s *Store) SaveLLMMetricPointsIfCurrent(ctx context.Context, generation IntegrationGeneration, metricName, podPattern, pathMatcher string, points []integration.MetricPoint) (bool, error) {
	return s.saveLLMMetricPoints(ctx, metricName, podPattern, pathMatcher, points, &generation)
}

func (s *Store) saveLLMMetricPoints(ctx context.Context, metricName, podPattern, pathMatcher string, points []integration.MetricPoint, generation *IntegrationGeneration) (bool, error) {
	if len(points) == 0 {
		return true, nil
	}
	re, err := integration.CompilePodUsernamePattern(podPattern)
	if err != nil {
		return false, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if generation != nil {
		current, err := lockAndMatchIntegrationGeneration(ctx, tx, *generation)
		if err != nil {
			return false, err
		}
		if !current {
			return false, nil
		}
	}
	enabled, err := featureEnabledForSave(ctx, tx, "llm_usage_monitoring")
	if err != nil {
		return false, err
	}
	if !enabled {
		return true, nil
	}
	aggregated := make(map[string]llmMetricSample)
	for _, point := range points {
		if !finiteNonNegative(point.Value) {
			continue
		}
		pod := point.Labels["pod"]
		if pod == "" {
			pod = point.Labels["pod_name"]
		}
		podUsername, ok := integration.UsernameFromPod(re, pod)
		if !ok {
			continue
		}
		point.Labels = cloneMetricLabels(point.Labels)
		username, mappedHub, mappedNetwork, ok, err := resolveLLMIdentity(ctx, tx, pod, podUsername, point.Labels)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		clearMetricIdentityLabels(point.Labels)
		point.Labels["hub"] = mappedHub
		point.Labels["network"] = mappedNetwork
		path := point.Labels["path"]
		if path == "" {
			path = point.Labels["route"]
		}
		if pathMatcher != "" && path != pathMatcher {
			continue
		}
		var counterValue, latencyP50, latencyP95 *float64
		var estimatedCost *float64
		switch metricName {
		case "calls", "request_count", "requests":
			v := point.Value
			counterValue = &v
		case "latency_p50_ms", "p50_latency_ms":
			v := point.Value
			latencyP50 = &v
		case "latency_p95_ms", "p95_latency_ms":
			v := point.Value
			latencyP95 = &v
		case "input_tokens":
			v := point.Value
			counterValue = &v
		case "output_tokens":
			v := point.Value
			counterValue = &v
		case "total_tokens":
			v := point.Value
			counterValue = &v
		case "bytes":
			v := point.Value
			counterValue = &v
		case "estimated_cost", "estimated_cost_input_tokens", "estimated_cost_output_tokens":
			v := point.Value
			estimatedCost = &v
		default:
			continue
		}
		windowStart := point.Timestamp.UTC().Truncate(time.Minute)
		fingerprint := llmDimensionFingerprint(username, pod, point.Labels)
		key := windowStart.Format(time.RFC3339Nano) + "\x00" + fingerprint
		safeLabels := llmSafeLabels(point.Labels)
		// Normalize the Pod alias so equivalent Prometheus series retain the
		// same safe labels regardless of whether the exporter used pod or
		// pod_name. Instance/container labels are intentionally not persisted.
		delete(safeLabels, "pod_name")
		safeLabels["pod"] = pod
		candidate := llmMetricSample{
			windowStart: windowStart, fingerprint: fingerprint, username: username,
			pod: pod, hub: point.Labels["hub"], model: point.Labels["model"],
			counterValue: counterValue, latencyP50: latencyP50, latencyP95: latencyP95,
			estimatedCost: estimatedCost, sampledAt: point.Timestamp, labels: safeLabels,
		}
		if current, exists := aggregated[key]; exists {
			merged, err := mergeLLMMetricSamples(current, candidate)
			if err != nil {
				return false, err
			}
			aggregated[key] = merged
		} else {
			aggregated[key] = candidate
		}
	}
	keys := make([]string, 0, len(aggregated))
	for key := range aggregated {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sample := aggregated[key]
		calls, successCount, errorCount, inputTokens, outputTokens, totalTokens, byteCount, err := finalizeLLMCounter(metricName, sample.counterValue, sample.labels["status"])
		if err != nil {
			return false, err
		}
		labels, _ := json.Marshal(sample.labels)
		_, err = tx.Exec(ctx, `INSERT INTO llm_usage_samples(source,metric_name,window_start,dimension_fingerprint,username,pod_name,hub_name,model,calls,success_count,error_count,latency_p50_ms,latency_p95_ms,input_tokens,output_tokens,total_tokens,bytes,estimated_cost,sampled_at,labels) VALUES('prometheus',$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) ON CONFLICT(source,metric_name,window_start,dimension_fingerprint) DO UPDATE SET username=EXCLUDED.username,pod_name=EXCLUDED.pod_name,hub_name=EXCLUDED.hub_name,model=EXCLUDED.model,calls=EXCLUDED.calls,success_count=EXCLUDED.success_count,error_count=EXCLUDED.error_count,latency_p50_ms=EXCLUDED.latency_p50_ms,latency_p95_ms=EXCLUDED.latency_p95_ms,input_tokens=EXCLUDED.input_tokens,output_tokens=EXCLUDED.output_tokens,total_tokens=EXCLUDED.total_tokens,bytes=EXCLUDED.bytes,estimated_cost=EXCLUDED.estimated_cost,sampled_at=EXCLUDED.sampled_at,labels=EXCLUDED.labels`, metricName, sample.windowStart, sample.fingerprint, sample.username, sample.pod, sample.hub, sample.model, calls, successCount, errorCount, sample.latencyP50, sample.latencyP95, inputTokens, outputTokens, totalTokens, byteCount, sample.estimatedCost, sample.sampledAt, labels)
		if err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

type llmMetricSample struct {
	windowStart                            time.Time
	fingerprint, username, pod, hub, model string
	counterValue                           *float64
	latencyP50, latencyP95, estimatedCost  *float64
	sampledAt                              time.Time
	labels                                 map[string]string
}

// mergeLLMMetricSamples combines Prometheus series that collapse to the same
// canonical jupiq dimensions (for example, series differing only by instance).
// Counters and costs are additive. Latency is a gauge, so summing would invent
// latency; the maximum observed value is retained as a conservative signal.
func mergeLLMMetricSamples(current, candidate llmMetricSample) (llmMetricSample, error) {
	var err error
	current.counterValue, err = addOptionalMetricValue(current.counterValue, candidate.counterValue)
	if err != nil {
		return llmMetricSample{}, err
	}
	current.estimatedCost, err = addOptionalMetricValue(current.estimatedCost, candidate.estimatedCost)
	if err != nil {
		return llmMetricSample{}, err
	}
	current.latencyP50 = maxOptionalMetricValue(current.latencyP50, candidate.latencyP50)
	current.latencyP95 = maxOptionalMetricValue(current.latencyP95, candidate.latencyP95)
	if candidate.sampledAt.After(current.sampledAt) {
		current.sampledAt = candidate.sampledAt
	}
	return current, nil
}

func finalizeLLMCounter(metricName string, raw *float64, status string) (calls, successCount, errorCount int64, inputTokens, outputTokens, totalTokens, byteCount *int64, err error) {
	if raw == nil {
		return
	}
	value, ok := roundedMetricCount(*raw)
	if !ok {
		err = errors.New("LLM metric 합계가 bigint 범위를 초과했습니다")
		return
	}
	switch metricName {
	case "calls", "request_count", "requests":
		calls = value
		if strings.HasPrefix(status, "2") || status == "success" {
			successCount = calls
		} else if status != "" {
			errorCount = calls
		}
	case "input_tokens":
		inputTokens = &value
	case "output_tokens":
		outputTokens = &value
	case "total_tokens":
		totalTokens = &value
	case "bytes":
		byteCount = &value
	}
	return
}

func addOptionalMetricValue(left, right *float64) (*float64, error) {
	if left == nil {
		return right, nil
	}
	if right == nil {
		return left, nil
	}
	value := *left + *right
	if !finiteNonNegative(value) {
		return nil, errors.New("LLM metric 합계가 허용 범위를 초과했습니다")
	}
	return &value, nil
}

func maxOptionalMetricValue(left, right *float64) *float64 {
	if left == nil || (right != nil && *right > *left) {
		return right
	}
	return left
}

func resolveLLMIdentity(ctx context.Context, tx pgx.Tx, pod, podUsername string, labels map[string]string) (string, string, string, bool, error) {
	hubHint := firstLabel(labels, "hub", "hub_name", "network", "network_name")
	var username, hub, network string
	var matches int
	err := tx.QueryRow(ctx, `SELECT s.username,h.name,h.network,count(*) OVER() FROM servers s JOIN hubs h ON h.id=s.hub_id
		WHERE s.pod_name=$1 AND s.username=$2 AND ($3='' OR h.name=$3 OR h.network=$3)
		ORDER BY (s.status='running') DESC,s.synced_at DESC LIMIT 1`, pod, podUsername, hubHint).Scan(&username, &hub, &network, &matches)
	if err == nil {
		if matches != 1 {
			return "", "", "", false, nil
		}
		return username, hub, network, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", false, err
	}
	// Never attribute an unknown remote label by username alone. A configured
	// regex is only a filter; the exact Pod-to-server inventory is authoritative.
	return "", "", "", false, nil
}

func llmDimensionFingerprint(username, pod string, labels map[string]string) string {
	parts := []string{username, pod}
	for _, key := range []string{"hub", "network", "model", "status", "path", "route"} {
		parts = append(parts, strings.TrimSpace(labels[key]))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("%x", sum[:])
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
	_, err := s.saveMetricPoints(ctx, source, name, IsGPUMetric(name, ""), points, nil)
	return err
}

func (s *Store) SavePrometheusMetricPoints(ctx context.Context, name, query string, points []integration.MetricPoint) error {
	_, err := s.saveMetricPoints(ctx, "prometheus", name, IsGPUMetric(name, query), points, nil)
	return err
}

func (s *Store) SavePrometheusMetricPointsIfCurrent(ctx context.Context, generation IntegrationGeneration, name, query string, points []integration.MetricPoint) (bool, error) {
	return s.saveMetricPoints(ctx, "prometheus", name, IsGPUMetric(name, query), points, &generation)
}

func (s *Store) saveMetricPoints(ctx context.Context, source, name string, gpuMetric bool, points []integration.MetricPoint, generation *IntegrationGeneration) (bool, error) {
	if len(points) == 0 {
		return true, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if generation != nil {
		current, err := lockAndMatchIntegrationGeneration(ctx, tx, *generation)
		if err != nil {
			return false, err
		}
		if !current {
			return false, nil
		}
	}
	if gpuMetric {
		enabled, err := featureEnabledForSave(ctx, tx, "gpu_monitoring")
		if err != nil {
			return false, err
		}
		if !enabled {
			return true, nil
		}
	}
	for _, point := range points {
		if !finiteNonNegative(point.Value) {
			continue
		}
		point.Labels = cloneMetricLabels(point.Labels)
		pod := point.Labels["pod"]
		if pod == "" {
			pod = point.Labels["pod_name"]
		}
		if pod != "" {
			var serverID int64
			var matches int
			var username, hub, network, department, project string
			hubHint := firstLabel(point.Labels, "hub", "hub_name", "network", "network_name")
			clearMetricIdentityLabels(point.Labels)
			_ = tx.QueryRow(ctx, `SELECT s.id,s.username,h.name,h.network,COALESCE(u.department,''),COALESCE(s.raw->>'project',''),count(*) OVER() FROM servers s JOIN hubs h ON h.id=s.hub_id LEFT JOIN managed_users u ON u.id=s.managed_user_id WHERE s.pod_name=$1 AND ($2='' OR h.name=$2 OR h.network=$2) ORDER BY (s.status='running') DESC,s.synced_at DESC LIMIT 1`, pod, hubHint).Scan(&serverID, &username, &hub, &network, &department, &project, &matches)
			if matches != 1 {
				serverID = 0 // Ambiguous across isolated networks; do not guess.
				username, hub, network, department, project = "", "", "", "", ""
			}
			for key, value := range map[string]string{"username": username, "hub": hub, "network": network, "department": department, "project": project} {
				if value != "" {
					point.Labels[key] = value
				}
			}
			switch {
			case serverID == 0:
				// Keep the raw metric sample, but never attach it to an arbitrary
				// server when a Pod name is ambiguous across networks.
			case name == "cpu_cores":
				_, _ = tx.Exec(ctx, `UPDATE servers SET cpu_cores=$2,raw=jsonb_set(jsonb_set(raw,'{resource_sampled_at}',to_jsonb($3::timestamptz),true),'{cpu_sampled_at}',to_jsonb($3::timestamptz),true) WHERE id=$1`, serverID, point.Value, point.Timestamp)
			case name == "memory_bytes":
				_, _ = tx.Exec(ctx, `UPDATE servers SET memory_bytes=$2,raw=jsonb_set(jsonb_set(raw,'{resource_sampled_at}',to_jsonb($3::timestamptz),true),'{memory_sampled_at}',to_jsonb($3::timestamptz),true) WHERE id=$1`, serverID, int64(point.Value), point.Timestamp)
			case name == "gpu_count":
				count, valid := roundedMetricCount(point.Value)
				if valid && count <= math.MaxInt32 {
					_, _ = tx.Exec(ctx, `UPDATE servers SET gpu_count=$2,raw=jsonb_set(jsonb_set(raw,'{resource_sampled_at}',to_jsonb($3::timestamptz),true),'{gpu_sampled_at}',to_jsonb($3::timestamptz),true) WHERE id=$1`, serverID, int(count), point.Timestamp)
				}
			case name == "gpu_utilization" || name == "vram_bytes":
				_, _ = tx.Exec(ctx, `UPDATE servers SET gpu_count=COALESCE(gpu_count,1),raw=jsonb_set(jsonb_set(jsonb_set(raw,$2::text[],to_jsonb($3::double precision),true),'{resource_sampled_at}',to_jsonb($4::timestamptz),true),'{gpu_sampled_at}',to_jsonb($4::timestamptz),true) WHERE id=$1`, serverID, []string{name}, point.Value, point.Timestamp)
			}
		}
		if pod == "" {
			clearMetricIdentityLabels(point.Labels)
		}
		labels, _ := json.Marshal(metricSafeLabels(point.Labels))
		metricKind := "generic"
		if gpuMetric {
			metricKind = "gpu"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO metric_samples(source,metric_name,metric_kind,labels,value,sampled_at) VALUES($1,$2,$3,$4,$5,$6)`, source, name, metricKind, labels, point.Value, point.Timestamp); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// featureEnabledForSave reads and locks the feature row in the same
// transaction that persists a feature-controlled sample. The shared row lock
// makes a concurrent OFF update wait for an already-authorized save, while a
// save that starts after OFF observes false and writes nothing. Missing,
// scalar, and non-boolean values all fail closed.
func featureEnabledForSave(ctx context.Context, tx pgx.Tx, feature string) (bool, error) {
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT CASE
		WHEN jsonb_typeof(value)='object' AND jsonb_typeof(value->$2)='boolean'
		THEN (value->>$2)::boolean ELSE false END
		FROM settings WHERE setting_key=$1 FOR SHARE`, "features", feature).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return enabled, err
}

func cloneMetricLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func clearMetricIdentityLabels(labels map[string]string) {
	for _, key := range []string{"username", "user", "hub", "hub_name", "network", "network_name", "department", "project"} {
		delete(labels, key)
	}
}

func metricSafeLabels(labels map[string]string) map[string]string {
	safe := make(map[string]string, 16)
	for _, key := range []string{
		"pod", "pod_name", "hub", "hub_name", "network", "network_name", "username", "user",
		"department", "project", "node", "node_name", "namespace", "container", "device", "gpu", "gpu_uuid",
		"model", "status", "code", "path", "route", "method",
	} {
		if value := strings.TrimSpace(labels[key]); value != "" {
			safe[key] = value
		}
	}
	return safe
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) && value < float64(math.MaxInt64)
}

func roundedMetricCount(value float64) (int64, bool) {
	if !finiteNonNegative(value) {
		return 0, false
	}
	return int64(math.Round(value)), true
}

func IsGPUMetricName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(lower, "gpu") || strings.Contains(lower, "vram") || strings.Contains(lower, "dcgm") || strings.Contains(lower, "cuda") || strings.Contains(lower, "nvidia")
}

func IsGPUMetric(name, query string) bool {
	return IsGPUMetricName(name) || IsGPUMetricName(query)
}

const (
	maxPodReconciliationItems = 5000
	podReconciliationTimeout  = 20 * time.Second
)

type podReconciliationCandidate struct {
	serverID int64
	pod      integration.Pod
	priority int
	snapshot []byte
}

type validatedPodCandidate struct {
	pod               integration.Pod
	username, hubHint string
	priority          int
	snapshot          []byte
}

func (s *Store) UpdatePods(ctx context.Context, pods []integration.Pod, podPatterns ...string) error {
	_, err := s.updatePods(ctx, pods, nil, podPatterns...)
	return err
}

func (s *Store) UpdatePodsIfCurrent(ctx context.Context, generation IntegrationGeneration, pods []integration.Pod, podPatterns ...string) (bool, error) {
	return s.updatePods(ctx, pods, &generation, podPatterns...)
}

func (s *Store) updatePods(ctx context.Context, pods []integration.Pod, generation *IntegrationGeneration, podPatterns ...string) (bool, error) {
	if len(pods) > maxPodReconciliationItems {
		return false, fmt.Errorf("Kubernetes Pod 목록이 저장 한도 %d개를 초과했습니다", maxPodReconciliationItems)
	}
	var podPattern string
	if len(podPatterns) > 0 {
		podPattern = podPatterns[0]
	}
	re, err := integration.CompilePodUsernamePattern(podPattern)
	if err != nil {
		return false, err
	}
	validated := make([]validatedPodCandidate, 0, len(pods))
	for _, pod := range pods {
		pod.Name = strings.TrimSpace(pod.Name)
		if pod.Name == "" {
			continue
		}
		nameUsername, ok := integration.UsernameFromPod(re, pod.Name)
		if !ok {
			continue
		}
		labelUsername, labelsConsistent := trustedPodUsername(pod.Labels)
		if !labelsConsistent || (labelUsername != "" && labelUsername != nameUsername) {
			continue
		}
		priority := podPhasePriority(pod.Phase)
		if priority == 0 {
			continue
		}
		validated = append(validated, validatedPodCandidate{
			pod: pod, username: nameUsername, hubHint: firstLabel(pod.Labels, "hub", "hub_name", "jupyterhub"),
			priority: priority, snapshot: safePodSnapshot(pod),
		})
	}
	if len(validated) == 0 {
		return true, nil
	}
	writeCtx, cancel := context.WithTimeout(ctx, podReconciliationTimeout)
	defer cancel()
	tx, err := s.Pool.Begin(writeCtx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(writeCtx) }()
	if generation != nil {
		current, err := lockAndMatchIntegrationGeneration(writeCtx, tx, *generation)
		if err != nil {
			return false, err
		}
		if !current {
			return false, nil
		}
	}
	candidatesByServer := make(map[int64][]podReconciliationCandidate)
	for _, candidate := range validated {
		serverID, ok, err := resolvePodServer(writeCtx, tx, candidate.pod.Name, candidate.username, candidate.hubHint)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		candidatesByServer[serverID] = append(candidatesByServer[serverID], podReconciliationCandidate{
			serverID: serverID, pod: candidate.pod, priority: candidate.priority, snapshot: candidate.snapshot,
		})
	}
	serverIDs := make([]int64, 0, len(candidatesByServer))
	for serverID := range candidatesByServer {
		serverIDs = append(serverIDs, serverID)
	}
	sort.Slice(serverIDs, func(i, j int) bool { return serverIDs[i] < serverIDs[j] })
	for _, serverID := range serverIDs {
		candidate, ok := selectPodCandidate(candidatesByServer[serverID])
		if !ok {
			continue
		}
		if _, err := tx.Exec(writeCtx, `UPDATE servers SET pod_name=$2,node_name=$3,raw=jsonb_set(raw,'{kubernetes}',$4::jsonb,true) WHERE id=$1`, candidate.serverID, candidate.pod.Name, candidate.pod.Node, candidate.snapshot); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(writeCtx); err != nil {
		return false, err
	}
	return true, nil
}

func trustedPodUsername(labels map[string]string) (string, bool) {
	username := ""
	for _, key := range []string{"hub.jupyter.org/username", "jupyterhub-user"} {
		value := strings.TrimSpace(labels[key])
		if value == "" {
			continue
		}
		if username != "" && username != value {
			return "", false
		}
		username = value
	}
	return username, true
}

func resolvePodServer(ctx context.Context, tx pgx.Tx, podName, username, hubLabel string) (int64, bool, error) {
	var serverID int64
	var matches int
	err := tx.QueryRow(ctx, `SELECT s.id,count(*) OVER() FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE s.pod_name=$1 AND s.username=$2 AND ($3='' OR h.name=$3 OR h.network=$3) ORDER BY (s.status='running') DESC,s.synced_at DESC LIMIT 1`, podName, username, hubLabel).Scan(&serverID, &matches)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	if err == nil && matches == 1 {
		return serverID, true, nil
	}
	serverID, matches = 0, 0
	if hubLabel != "" {
		err = tx.QueryRow(ctx, `SELECT s.id,count(*) OVER() FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE s.status='running' AND s.username=$1 AND (h.name=$2 OR h.network=$2) ORDER BY s.synced_at DESC LIMIT 1`, username, hubLabel).Scan(&serverID, &matches)
	} else {
		err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(min(id),0) FROM servers WHERE status='running' AND username=$1`, username).Scan(&matches, &serverID)
	}
	if err != nil {
		return 0, false, err
	}
	return serverID, matches == 1, nil
}

func podPhasePriority(phase string) int {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "running":
		return 3
	case "pending":
		return 2
	case "succeeded", "failed":
		return 0
	default:
		return 1
	}
}

func selectPodCandidate(candidates []podReconciliationCandidate) (podReconciliationCandidate, bool) {
	if len(candidates) == 0 {
		return podReconciliationCandidate{}, false
	}
	bestPriority := 0
	for _, candidate := range candidates {
		if candidate.priority > bestPriority {
			bestPriority = candidate.priority
		}
	}
	var selected podReconciliationCandidate
	count := 0
	for _, candidate := range candidates {
		if candidate.priority == bestPriority {
			selected = candidate
			count++
		}
	}
	if count != 1 {
		return podReconciliationCandidate{}, false
	}
	return selected, true
}

func safePodSnapshot(pod integration.Pod) []byte {
	// Kubernetes Pod objects can contain environment variables, annotations,
	// projected secret references and service-account details. Persist only the
	// operational fields used by jupiq; labels are consumed in-memory for
	// matching and are never written to the server record.
	return normalizeJSON(map[string]any{
		"name": pod.Name, "namespace": pod.Namespace, "node": pod.Node, "phase": pod.Phase,
	})
}

func firstLabel(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(labels[key]); value != "" {
			return value
		}
	}
	return ""
}

func (s *Store) GPUUsage(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT s.id,s.hub_id,h.name,h.network,h.collect_interval_seconds,s.username,s.server_name,s.pod_name,s.node_name,s.image,s.gpu_count,s.raw,s.synced_at
		FROM servers s JOIN hubs h ON h.id=s.hub_id
		WHERE s.status='running' AND (COALESCE(s.gpu_count,0)>0 OR s.raw ? 'gpu_utilization' OR s.raw ? 'vram_bytes')
		ORDER BY h.name,s.username,s.server_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var serverID, hubID int64
		var hub, network, username, serverName, podName, nodeName, image string
		var collectInterval int
		var gpuCount *int
		var raw []byte
		var syncedAt time.Time
		if err := rows.Scan(&serverID, &hubID, &hub, &network, &collectInterval, &username, &serverName, &podName, &nodeName, &image, &gpuCount, &raw, &syncedAt); err != nil {
			return nil, err
		}
		detail := map[string]any{}
		_ = json.Unmarshal(raw, &detail)
		gpuUtilization, vramBytes, sampledAt, stale := gpuMetricSnapshot(detail, time.Now().UTC(), collectInterval)
		items = append(items, map[string]any{
			"id": serverID, "server_id": serverID, "hub_id": hubID, "hub": hub, "network": network,
			"username": username, "server_name": serverName, "pod_name": podName, "node_name": nodeName, "image": image,
			"gpu_count": derefInt(gpuCount), "gpu_utilization": gpuUtilization, "vram_bytes": vramBytes,
			"sampled_at": sampledAt, "stale": stale, "server_synced_at": syncedAt,
		})
	}
	return items, rows.Err()
}

func gpuMetricSnapshot(detail map[string]any, now time.Time, collectInterval int) (any, any, *time.Time, bool) {
	sampledAt := metricSampleTime(detail["gpu_sampled_at"])
	stale := sampledAt == nil || sampledAt.After(now.Add(time.Minute)) || sessionSnapshotStale(now, valueOrZero(sampledAt), collectInterval)
	if stale {
		return nil, nil, sampledAt, true
	}
	var utilization, vram any
	if value, ok := optionalNumber(detail["gpu_utilization"]); ok {
		utilization = value
	}
	if value, ok := optionalNumber(detail["vram_bytes"]); ok {
		vram = value
	}
	return utilization, vram, sampledAt, false
}

func valueOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// GPUFeatureBlocked reports whether a metric lookup names a GPU metric that a
// disabled gpu_monitoring feature has to hide. Metrics returns it so callers can
// tell "the feature is off" apart from the otherwise identical "no samples".
func GPUFeatureBlocked(metric string, gpuMonitoring bool) bool {
	return !gpuMonitoring && IsGPUMetricName(metric)
}

func (s *Store) Metrics(ctx context.Context, from, to time.Time, metric string, limit int) ([]map[string]any, bool, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	var features struct {
		GPUMonitoring bool `json:"gpu_monitoring"`
	}
	_ = s.GetSetting(ctx, "features", &features)
	if GPUFeatureBlocked(metric, features.GPUMonitoring) {
		return []map[string]any{}, true, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT source,metric_name,labels,value,sampled_at FROM metric_samples WHERE sampled_at BETWEEN $1 AND $2 AND ($3='' OR metric_name=$3) AND ($5 OR metric_kind<>'gpu') ORDER BY sampled_at DESC LIMIT $4`, from, to, metric, limit, features.GPUMonitoring)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var source, name string
		var labels []byte
		var value float64
		var sampled time.Time
		if err := rows.Scan(&source, &name, &labels, &value, &sampled); err != nil {
			return nil, false, err
		}
		items = append(items, map[string]any{"source": source, "metric": name, "labels": json.RawMessage(labels), "value": value, "sampled_at": sampled})
	}
	return items, false, rows.Err()
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
