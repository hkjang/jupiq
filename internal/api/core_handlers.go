package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/store"
)

func (s *Server) registerCore(mux router) {
	mux.HandleFunc("GET /api/v1/features", s.require("", s.featuresGet))
	mux.HandleFunc("GET /api/v1/search", s.require("", s.globalSearch))
	mux.HandleFunc("GET /api/v1/dashboard", s.require("dashboard:read", s.dashboard))
	mux.HandleFunc("GET /api/v1/live", s.require("dashboard:read", s.live))
	mux.HandleFunc("GET /api/v1/dashboard/live", s.require("dashboard:read", s.live))
	mux.HandleFunc("GET /api/v1/usage", s.require("usage:read", s.usage))
	mux.HandleFunc("GET /api/v1/usage/consumption", s.require("usage:read", s.usageConsumption))
	mux.HandleFunc("GET /api/v1/llm-usage", s.require("usage:read", s.llmUsage))
	mux.HandleFunc("GET /api/v1/llm-usage/live", s.require("usage:read", s.llmUsageLive))
	mux.HandleFunc("GET /api/v1/settings", s.require("settings:read", s.settingsGet))
	mux.HandleFunc("PUT /api/v1/settings", s.require("settings:write", s.settingsPut))
	// Integration tests are authorized by type in integrationTest. JupyterHub
	// preflight belongs to Hub administration; every other provider remains a
	// service-settings operation.
	mux.HandleFunc("POST /api/v1/integrations/test", s.require("", s.integrationTest))
	mux.HandleFunc("GET /api/v1/users", s.require("", s.managedUsers))
	mux.HandleFunc("GET /api/v1/users/{username}", s.require("users:read", s.userDetail))
	mux.HandleFunc("GET /api/v1/local-users", s.require("users:read", s.localUsers))
	mux.HandleFunc("GET /api/v1/servers", s.require("", s.servers))
	mux.HandleFunc("GET /api/v1/roles", s.require("roles:read", s.rolesList))
	mux.HandleFunc("POST /api/v1/roles", s.require("roles:write", interactiveSessionOnly(s.roleSave)))
	mux.HandleFunc("PUT /api/v1/roles/{id}", s.require("roles:write", interactiveSessionOnly(s.roleSave)))
	mux.HandleFunc("DELETE /api/v1/roles/{id}", s.require("roles:write", interactiveSessionOnly(s.roleDelete)))
	mux.HandleFunc("PUT /api/v1/local-users/{id}/roles", s.require("roles:write", interactiveSessionOnly(s.userRolesPut)))
	mux.HandleFunc("GET /api/v1/keys", s.require("profile:keys", s.keysList))
	mux.HandleFunc("POST /api/v1/keys", s.require("profile:keys", s.keyCreate))
	mux.HandleFunc("POST /api/v1/keys/{id}/rotate", s.require("profile:keys", s.keyRotate))
	mux.HandleFunc("POST /api/v1/keys/{id}/revoke", s.require("profile:keys", s.keyRevoke))
	mux.HandleFunc("GET /api/v1/audit", s.require("audit:read", s.auditList))
	mux.HandleFunc("GET /api/v1/metrics", s.require("metrics:read", s.metrics))
	mux.HandleFunc("GET /api/v1/gpus", s.require("gpu:read", s.gpus))
}

func (s *Server) globalSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) < 2 || len([]rune(query)) > 100 {
		apiError(w, r, http.StatusBadRequest, "invalid_search", "검색어는 2~100자로 입력해 주세요")
		return
	}
	p := principal(r)
	items, err := s.Store.GlobalSearch(r.Context(), query, store.SearchAccess{
		Users: p.Allows("users:read"), Hubs: p.Allows("hubs:read"), Servers: p.Allows("servers:read"), Projects: p.Allows("project:read"),
	}, 8)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, map[string]any{"query": query, "items": items, "total": len(items)})
}

// featuresGet exposes only the global on/off state needed to build an
// authenticated user's navigation. It deliberately avoids returning the full
// settings document, which is restricted to settings:read.
func (s *Server) featuresGet(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.ListSettings(r.Context())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, featureStatus(settings))
}

func featureStatus(settings map[string]json.RawMessage) map[string]bool {
	features := map[string]bool{}
	if raw := settings["features"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &features)
	}
	workflow := struct {
		ApprovalEnabled bool `json:"approval_enabled"`
	}{}
	if raw := settings["workflow"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &workflow)
	}
	return map[string]bool{
		"gpu_monitoring":       features["gpu_monitoring"],
		"llm_usage_monitoring": features["llm_usage_monitoring"],
		"approval_workflow":    workflow.ApprovalEnabled,
	}
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.cachedLiveSnapshot(r)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	applyDashboardFilters(snapshot, r)
	data(w, http.StatusOK, snapshot)
}

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		apiError(w, r, http.StatusNotImplemented, "streaming_unavailable", "스트리밍을 지원하지 않는 연결입니다")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func() bool {
		snapshot, err := s.cachedLiveSnapshot(r)
		if err != nil {
			_, _ = fmt.Fprintf(w, "event: error\ndata: {\"message\":\"실시간 데이터를 읽지 못했습니다\"}\n\n")
			flusher.Flush()
			return false
		}
		applyDashboardFilters(snapshot, r)
		payload, _ := json.Marshal(snapshot)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

func (s *Server) cachedLiveSnapshot(r *http.Request) (map[string]any, error) {
	s.liveMu.Lock()
	if len(s.liveSnapshot) == 0 || time.Since(s.liveCachedAt) >= 4*time.Second {
		snapshot, err := s.Store.LiveSnapshot(r.Context())
		if err != nil {
			s.liveMu.Unlock()
			return nil, err
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			s.liveMu.Unlock()
			return nil, err
		}
		s.liveSnapshot = encoded
		s.liveCachedAt = time.Now()
	}
	encoded := append([]byte(nil), s.liveSnapshot...)
	s.liveMu.Unlock()
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil, err
	}
	redactDisabledMonitoring(clone, s.currentMonitoringFeatures(r.Context()))
	return clone, nil
}

func (s *Server) invalidateLiveSnapshot() {
	s.liveMu.Lock()
	s.liveSnapshot = nil
	s.liveCachedAt = time.Time{}
	s.liveMu.Unlock()
}

func (s *Server) currentMonitoringFeatures(ctx context.Context) map[string]bool {
	features := map[string]bool{"gpu_monitoring": false, "llm_usage_monitoring": false}
	var raw map[string]json.RawMessage
	if err := s.Store.GetSetting(ctx, "features", &raw); err != nil {
		return features
	}
	for key := range features {
		var enabled bool
		if value, exists := raw[key]; exists && json.Unmarshal(value, &enabled) == nil {
			features[key] = enabled
		}
	}
	return features
}

func redactDisabledMonitoring(snapshot map[string]any, features map[string]bool) {
	snapshot["feature_enabled"] = map[string]bool{
		"gpu_monitoring":       features["gpu_monitoring"],
		"llm_usage_monitoring": features["llm_usage_monitoring"],
	}
	if !features["gpu_monitoring"] {
		for _, key := range []string{"gpu_users", "gpu_usage", "gpu_waste", "waste_users"} {
			delete(snapshot, key)
		}
		redactGPURecord(snapshot["summary"])
		for _, key := range []string{"live_users", "sessions"} {
			for _, record := range dashboardSessions(snapshot[key]) {
				redactGPURecord(record)
			}
		}
		if aggregates, ok := snapshot["aggregates"].(map[string]any); ok {
			for _, aggregate := range aggregates {
				redactGPURecord(aggregate)
			}
		}
	}
	if !features["llm_usage_monitoring"] {
		snapshot["llm_usage"] = map[string]any{
			"feature_enabled": false,
			"data":            []any{},
			"top_callers":     []any{},
			"breakdown":       []any{},
			"usage_trend":     []any{},
			"summary": map[string]any{
				"calls": int64(0), "success": int64(0), "errors": int64(0),
				"input_tokens": int64(0), "output_tokens": int64(0), "total_tokens": int64(0),
				"estimated_cost": float64(0), "success_rate": nil, "latency_p95_ms": nil,
			},
		}
	}
}

func redactGPURecord(value any) {
	record, ok := value.(map[string]any)
	if !ok {
		return
	}
	for _, key := range []string{
		"gpu", "gpus", "gpu_count", "gpu_usage", "gpu_utilization", "gpu_percent", "gpu_samples",
		"vram", "vram_bytes", "vram_usage", "vram_utilization", "vram_percent",
		"gpu_sampled_at", "waste_candidate", "waste_score", "idle_ratio",
	} {
		delete(record, key)
	}
}

func applyDashboardFilters(snapshot map[string]any, r *http.Request) {
	sessions := dashboardSessions(snapshot["live_users"])
	if sessions == nil {
		return
	}
	selected := map[string]string{"hub": r.URL.Query().Get("hub"), "network": r.URL.Query().Get("network"), "department": r.URL.Query().Get("department"), "project": r.URL.Query().Get("project")}
	options := map[string]map[string]bool{"hub": {}, "network": {}, "department": {}, "project": {}}
	filtered := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		matches := true
		for key, wanted := range selected {
			value, _ := session[key].(string)
			if value != "" {
				options[key][value] = true
			}
			if wanted != "" && value != wanted {
				matches = false
			}
		}
		if matches {
			filtered = append(filtered, session)
		}
	}
	filterResponse := map[string]any{"selected": selected, "group_by": []string{"hub", "network", "department", "project"}}
	for key, values := range options {
		list := make([]string, 0, len(values))
		for value := range values {
			list = append(list, value)
		}
		sort.Strings(list)
		filterResponse[key] = list
	}
	snapshot["filters"], snapshot["live_users"], snapshot["sessions"] = filterResponse, filtered, filtered
	users := map[string]bool{}
	summary := map[string]any{"running_servers": 0, "idle_sessions": 0, "long_running_sessions": 0}
	var cpuTotal, memoryTotal float64
	var cpuSamples, memorySamples int
	var gpuTotal, vramTotal float64
	var gpuSamples, vramSamples int
	gpuWaste := []map[string]any{}
	gpuOn := false
	switch featureMap := snapshot["feature_enabled"].(type) {
	case map[string]bool:
		gpuOn = featureMap["gpu_monitoring"]
	case map[string]any:
		gpuOn, _ = featureMap["gpu_monitoring"].(bool)
	}
	for _, session := range filtered {
		if stale, _ := session["stale"].(bool); stale {
			continue
		}
		summary["running_servers"] = summary["running_servers"].(int) + 1
		username, _ := session["username"].(string)
		users[fmt.Sprintf("%v\x00%s", session["hub_id"], username)] = true
		if value, ok := numericValue(session["cpu_cores"]); ok {
			cpuTotal += value
			cpuSamples++
		}
		if value, ok := numericValue(session["memory_bytes"]); ok {
			memoryTotal += value
			memorySamples++
		}
		if idle, _ := session["idle_candidate"].(bool); idle {
			summary["idle_sessions"] = summary["idle_sessions"].(int) + 1
		}
		if numberFromAny(session["runtime_seconds"]) >= 86400 {
			summary["long_running_sessions"] = summary["long_running_sessions"].(int) + 1
		}
		if gpuOn {
			if value, ok := numericValue(session["gpu_count"]); ok {
				gpuTotal += value
				gpuSamples++
			}
			if value, ok := numericValue(session["vram_bytes"]); ok {
				vramTotal += value
				vramSamples++
			}
			if waste, _ := session["waste_candidate"].(bool); waste {
				gpuWaste = append(gpuWaste, session)
			}
		}
	}
	summary["active_users"] = len(users)
	if cpuSamples > 0 {
		summary["cpu_usage"] = cpuTotal
	}
	if memorySamples > 0 {
		summary["memory_usage"] = memoryTotal
	}
	if gpuSamples > 0 {
		summary["gpu_usage"] = gpuTotal
	}
	if vramSamples > 0 {
		summary["vram_usage"] = vramTotal
	}
	snapshot["summary"] = summary
	if gpuOn {
		snapshot["gpu_waste"] = gpuWaste
	} else {
		delete(snapshot, "gpu_waste")
	}
}

func dashboardSessions(value any) []map[string]any {
	if sessions, ok := value.([]map[string]any); ok {
		return sessions
	}
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	sessions := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if session, ok := item.(map[string]any); ok {
			sessions = append(sessions, session)
		}
	}
	return sessions
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	from, to, err := boundedTimeRange(r, now.Add(-30*24*time.Hour), now, 366*24*time.Hour)
	if err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_range", err.Error())
		return
	}
	result, err := s.Store.Usage(r.Context(), from, to, r.URL.Query().Get("granularity"), r.URL.Query().Get("group_by"))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	// Consumption totals travel with the rate trend so a caller reading the
	// usage response can tell how much was used, not only how hard pods worked.
	groupBy := r.URL.Query().Get("group_by")
	if consumption, err := s.Store.ResourceConsumption(r.Context(), from, to, groupBy, 100); err == nil {
		result["consumption"] = consumption
	} else {
		s.Logger.Warn("resource consumption unavailable", "error", err)
	}
	data(w, http.StatusOK, result)
}

// usageConsumption reports integrated CPU and memory consumption per user, hub,
// network or department. These are core-hours and GB-hours rather than the
// averages in /usage: an average cannot separate one core held for a day from
// one core held for five minutes, which is the question this endpoint answers.
func (s *Server) usageConsumption(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	from, to, err := boundedTimeRange(r, now.Add(-30*24*time.Hour), now, 366*24*time.Hour)
	if err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_range", err.Error())
		return
	}
	items, err := s.Store.ResourceConsumption(r.Context(), from, to, r.URL.Query().Get("group_by"), queryInt(r, "limit", 100))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	through, throughErr := s.Store.RolledUpThrough(r.Context())
	payload := map[string]any{"from": from, "to": to, "data": items}
	if throughErr == nil {
		// Consumption is only complete up to the last hour the rollup consumed;
		// the in-progress hour is deliberately excluded from the buckets.
		payload["rolled_up_through"] = through
	}
	data(w, http.StatusOK, payload)
}

func (s *Server) llmUsage(w http.ResponseWriter, r *http.Request) {
	from, to, groupBy, rangeErr := llmUsageParameters(r, time.Now().UTC())
	if rangeErr != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_range", rangeErr.Error())
		return
	}
	result, err := s.Store.LLMUsage(r.Context(), from, to, groupBy)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, result)
}

func (s *Server) llmUsageLive(w http.ResponseWriter, r *http.Request) {
	if _, _, _, err := llmUsageParameters(r, time.Now().UTC()); err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_range", err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		apiError(w, r, http.StatusNotImplemented, "streaming_unavailable", "스트리밍을 지원하지 않는 연결입니다")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func() bool {
		from, to, groupBy, err := llmUsageParameters(r, time.Now().UTC())
		if err != nil {
			return false
		}
		result, err := s.Store.LLMUsage(r.Context(), from, to, groupBy)
		if err != nil {
			_, _ = fmt.Fprint(w, "event: error\ndata: {\"message\":\"LLM 사용량 데이터를 읽지 못했습니다\"}\n\n")
			flusher.Flush()
			return false
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return false
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

func llmUsageParameters(r *http.Request, now time.Time) (time.Time, time.Time, string, error) {
	to := now.UTC()
	if raw := strings.TrimSpace(r.URL.Query().Get("to")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, "", fmt.Errorf("to는 RFC3339 날짜·시간이어야 합니다")
		}
		to = parsed.UTC()
	}
	duration := 24 * time.Hour
	switch r.URL.Query().Get("range") {
	case "week":
		duration = 7 * 24 * time.Hour
	case "month":
		duration = 30 * 24 * time.Hour
	}
	from := to.Add(-duration)
	if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, "", fmt.Errorf("from는 RFC3339 날짜·시간이어야 합니다")
		}
		from = parsed.UTC()
	}
	if !from.Before(to) || to.Sub(from) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, "", fmt.Errorf("조회 시작은 종료보다 앞서야 하며 기간은 최대 366일입니다")
	}
	return from, to, r.URL.Query().Get("group_by"), nil
}

func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.ListSettings(r.Context())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	secrets, err := s.Store.SecretStatus(r.Context())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	health, err := s.Store.ListIntegrationHealth(r.Context())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	result := map[string]any{"settings": settings, "secrets": secrets, "integration_health": health}
	for key, value := range settings {
		result[key] = value
	}
	if value, ok := settings["auth.oidc"]; ok {
		result["oidc"] = value
	}
	data(w, http.StatusOK, result)
}

func (s *Server) settingsPut(w http.ResponseWriter, r *http.Request) {
	var input map[string]any
	if err := decodeJSON(r, &input); err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	p := principal(r)
	changed := []string{}
	secretKeys := []string{}
	settingsToWrite := map[string]any{}
	secretsToWrite := map[string]string{}
	// Accept either {settings:{...}} or a direct setting map.
	if nested, ok := input["settings"].(map[string]any); ok {
		input = nested
	}
	for key, value := range input {
		if isSecretKey(key) {
			secret, ok := value.(string)
			secretKey := canonicalSecretKey(key)
			if !ok || !store.IsSettingsSecretKey(secretKey) {
				apiError(w, r, http.StatusBadRequest, "unsupported_secret", "지원하지 않는 비밀정보 항목입니다: "+key)
				return
			}
			if secret != "" && !isMasked(secret) {
				if _, duplicate := secretsToWrite[secretKey]; duplicate {
					apiError(w, r, http.StatusBadRequest, "duplicate_secret", secretKey+" 비밀정보가 중복되었습니다")
					return
				}
				secretsToWrite[secretKey] = secret
				secretKeys = append(secretKeys, secretKey)
			}
			continue
		}
		if object, ok := value.(map[string]any); ok {
			clean := make(map[string]any, len(object))
			for nestedKey, nestedValue := range object {
				clean[nestedKey] = nestedValue
			}
			object = clean
			value = object
			for nestedKey, nestedValue := range object {
				if !isSecretKey(nestedKey) {
					continue
				}
				secret, stringValue := nestedValue.(string)
				delete(object, nestedKey)
				secretKey := canonicalNestedSecret(key, nestedKey)
				if !stringValue || !store.IsSettingsSecretKey(secretKey) {
					apiError(w, r, http.StatusBadRequest, "unsupported_secret", "지원하지 않는 비밀정보 항목입니다: "+key+"."+nestedKey)
					return
				}
				if secret != "" && !isMasked(secret) {
					if _, duplicate := secretsToWrite[secretKey]; duplicate {
						apiError(w, r, http.StatusBadRequest, "duplicate_secret", secretKey+" 비밀정보가 중복되었습니다")
						return
					}
					secretsToWrite[secretKey] = secret
					secretKeys = append(secretKeys, secretKey)
				}
			}
		}
		canonical := canonicalSettingKey(key)
		if _, duplicate := settingsToWrite[canonical]; duplicate {
			apiError(w, r, http.StatusBadRequest, "duplicate_setting", canonical+" 설정이 중복되었습니다")
			return
		}
		settingsToWrite[canonical] = value
		changed = append(changed, canonical)
	}
	applySecureIntegrationDefaults(settingsToWrite)
	if err := validateSettingsUpdate(settingsToWrite, secretsToWrite); err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	if err := s.Store.UpdateSettingsAndSecrets(r.Context(), settingsToWrite, secretsToWrite, p.User.ID); err != nil {
		switch {
		case errors.Is(err, store.ErrSettingsSecretReentry):
			apiError(w, r, http.StatusBadRequest, "secret_reentry_required", "연동 주소 또는 TLS 검증 설정을 바꾸려면 해당 인증정보를 다시 입력해야 합니다")
		case errors.Is(err, store.ErrOIDCSecretRequired):
			apiError(w, r, http.StatusBadRequest, "oidc_secret_required", "OIDC를 사용하려면 Client Secret을 입력해야 합니다")
		default:
			handleStoreError(w, r, err)
		}
		return
	}
	s.invalidateLiveSnapshot()
	sort.Strings(changed)
	sort.Strings(secretKeys)
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "settings.update", "settings", "", "success", "", nil, map[string]any{"changed": changed, "secrets_changed": secretKeys}))
	s.settingsGet(w, r)
}

func applySecureIntegrationDefaults(values map[string]any) {
	for _, section := range []string{"auth.oidc", "ai", "prometheus", "kubernetes"} {
		object, ok := values[section].(map[string]any)
		if !ok {
			continue
		}
		if _, exists := object["verify_tls"]; !exists {
			object["verify_tls"] = true
		}
	}
	if ai, ok := values["ai"].(map[string]any); ok {
		// The only implemented provider contract is OpenAI-compatible SSE.
		// Persist the invariant even when older or hand-written clients omit it.
		ai["provider"] = "openai-compatible"
		ai["streaming"] = true
	}
}

var allowedSettingKeys = map[string]bool{
	"system": true, "workflow": true, "auth.oidc": true, "ai": true, "prometheus": true,
	"kubernetes": true, "notifications": true, "features": true, "llm_usage": true,
	"security": true,
}

var allowedSettingFields = map[string]map[string]bool{
	"system":        {"raw_retention_days": true, "usage_retention_days": true},
	"workflow":      {"approval_enabled": true, "manager_review_enabled": true, "require_reason": true, "request_types": true},
	"auth.oidc":     {"enabled": true, "issuer_url": true, "client_id": true, "redirect_url": true, "scopes": true, "username_claim": true, "auto_create_users": true, "verify_tls": true},
	"ai":            {"enabled": true, "provider": true, "base_url": true, "model": true, "max_tokens": true, "streaming": true, "verify_tls": true},
	"prometheus":    {"enabled": true, "base_url": true, "verify_tls": true, "queries": true},
	"kubernetes":    {"enabled": true, "base_url": true, "verify_tls": true, "namespace": true, "label_selector": true, "pod_username_regex": true},
	"notifications": {"webhook_enabled": true, "base_url": true, "events": true},
	"features":      {"gpu_monitoring": true, "llm_usage_monitoring": true},
	"llm_usage":     {"source": true, "pod_username_regex": true, "sample_pod": true, "path_matcher": true, "label_mappings": true, "promql": true, "input_cost_per_million": true, "output_cost_per_million": true, "stale_seconds": true, "retention_days": true},
	"security":      {"key_rotation_days": true, "key_max_lifetime_days": true, "key_permissions": true},
}

func validateSettingsUpdate(values map[string]any, secrets map[string]string) error {
	for key, secret := range secrets {
		if len(secret) > 65536 {
			return fmt.Errorf("%s 비밀값은 64KiB 이하여야 합니다", key)
		}
	}
	for key, value := range values {
		if !allowedSettingKeys[key] {
			return fmt.Errorf("지원하지 않는 설정 영역입니다: %s", key)
		}
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s 설정은 JSON object여야 합니다", key)
		}
		if err := validateSettingSection(key, object); err != nil {
			return err
		}
	}
	return nil
}

func validateSettingSection(key string, object map[string]any) error {
	allowed := allowedSettingFields[key]
	for field := range object {
		if !allowed[field] {
			return fmt.Errorf("지원하지 않는 %s 설정입니다: %s", key, field)
		}
	}
	stringFields := map[string][]string{
		"auth.oidc":     {"issuer_url", "client_id", "redirect_url", "username_claim"},
		"ai":            {"provider", "base_url", "model"},
		"prometheus":    {"base_url"},
		"kubernetes":    {"base_url", "namespace", "label_selector", "pod_username_regex"},
		"notifications": {"base_url"},
		"llm_usage":     {"source", "pod_username_regex", "sample_pod", "path_matcher"},
	}
	for _, field := range stringFields[key] {
		if raw, exists := object[field]; exists {
			value, ok := raw.(string)
			if !ok {
				return fmt.Errorf("%s.%s는 문자열이어야 합니다", key, field)
			}
			if len(value) > 16384 {
				return fmt.Errorf("%s.%s가 허용 길이를 초과했습니다", key, field)
			}
		}
	}
	boolFields := map[string][]string{
		"features":      {"gpu_monitoring", "llm_usage_monitoring"},
		"workflow":      {"approval_enabled", "manager_review_enabled", "require_reason"},
		"auth.oidc":     {"enabled", "auto_create_users", "verify_tls"},
		"ai":            {"enabled", "streaming", "verify_tls"},
		"prometheus":    {"enabled", "verify_tls"},
		"kubernetes":    {"enabled", "verify_tls"},
		"notifications": {"webhook_enabled"},
	}
	for _, field := range boolFields[key] {
		if value, exists := object[field]; exists {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s.%s는 true 또는 false여야 합니다", key, field)
			}
		}
	}
	for _, bound := range []struct {
		section, field string
		min, max       float64
	}{
		{"system", "raw_retention_days", 1, 365},
		// Consumption buckets are small and are the only record left once raw
		// samples age out, so they are allowed a multi-year window.
		{"system", "usage_retention_days", 1, 3650},
		{"ai", "max_tokens", 1, integration.MaxAITokens},
		{"llm_usage", "stale_seconds", 30, 86400}, {"llm_usage", "retention_days", 1, 365},
		{"llm_usage", "input_cost_per_million", 0, 1e12}, {"llm_usage", "output_cost_per_million", 0, 1e12},
		{"security", "key_rotation_days", 1, 3650}, {"security", "key_max_lifetime_days", 1, 3650},
	} {
		if bound.section != key {
			continue
		}
		if value, exists := object[bound.field]; exists {
			number, ok := numericValue(value)
			if !ok || number < bound.min || number > bound.max {
				return fmt.Errorf("%s.%s는 %.0f~%.0f 범위여야 합니다", key, bound.field, bound.min, bound.max)
			}
			if bound.field != "input_cost_per_million" && bound.field != "output_cost_per_million" && math.Trunc(number) != number {
				return fmt.Errorf("%s.%s는 정수여야 합니다", key, bound.field)
			}
		}
	}
	for _, field := range []string{"base_url", "issuer_url", "redirect_url"} {
		if raw, exists := object[field]; exists && strings.TrimSpace(fmt.Sprint(raw)) != "" {
			value, ok := raw.(string)
			if !ok {
				return fmt.Errorf("%s.%s는 URL 문자열이어야 합니다", key, field)
			}
			if _, err := integration.ValidateEndpoint(value); err != nil {
				return fmt.Errorf("%s.%s: %w", key, field, err)
			}
		}
	}
	enabled, _ := object["enabled"].(bool)
	if enabled {
		switch key {
		case "auth.oidc":
			if blankString(object["issuer_url"]) || blankString(object["client_id"]) || blankString(object["username_claim"]) {
				return fmt.Errorf("OIDC 사용 시 issuer_url, client_id, username_claim이 필요합니다")
			}
		case "ai":
			if blankString(object["base_url"]) || blankString(object["model"]) {
				return fmt.Errorf("AI 사용 시 base_url과 model이 필요합니다")
			}
			if _, ok := numericValue(object["max_tokens"]); !ok {
				return fmt.Errorf("AI 사용 시 max_tokens가 필요합니다")
			}
		case "prometheus", "kubernetes":
			if blankString(object["base_url"]) {
				return fmt.Errorf("%s 사용 시 base_url이 필요합니다", key)
			}
		}
	}
	if key == "ai" {
		if provider, exists := object["provider"]; exists && strings.TrimSpace(fmt.Sprint(provider)) != "" && provider != "openai-compatible" {
			return fmt.Errorf("ai.provider는 openai-compatible만 지원합니다")
		}
		if streaming, exists := object["streaming"]; exists && streaming != true {
			return fmt.Errorf("AI streaming은 보안·응답성 정책상 항상 true여야 합니다")
		}
	}
	if key == "workflow" {
		if raw, exists := object["request_types"]; exists {
			items, ok := stringSettingArray(raw)
			if !ok {
				return fmt.Errorf("workflow.request_types는 문자열 배열이어야 합니다")
			}
			for _, item := range items {
				if item != "server_action" {
					return fmt.Errorf("현재 지원하는 승인 대상은 server_action뿐입니다")
				}
			}
		}
	}
	if key == "auth.oidc" {
		raw, exists := object["scopes"]
		if enabled && !exists {
			return fmt.Errorf("OIDC 사용 시 auth.oidc.scopes가 필요합니다")
		}
		if exists {
			scopes, ok := stringSettingArray(raw)
			if !ok || len(scopes) == 0 || len(scopes) > 32 {
				return fmt.Errorf("auth.oidc.scopes는 1~32개 문자열 배열이어야 합니다")
			}
			hasOpenID := false
			for _, scope := range scopes {
				if strings.TrimSpace(scope) == "" || len(scope) > 128 {
					return fmt.Errorf("auth.oidc.scopes 값이 올바르지 않습니다")
				}
				hasOpenID = hasOpenID || scope == "openid"
			}
			if !hasOpenID {
				return fmt.Errorf("auth.oidc.scopes에 openid가 필요합니다")
			}
		}
	}
	if key == "notifications" {
		if raw, exists := object["events"]; exists {
			events, ok := stringSettingArray(raw)
			if !ok || len(events) > 64 {
				return fmt.Errorf("notifications.events는 최대 64개 문자열 배열이어야 합니다")
			}
			for _, event := range events {
				if strings.TrimSpace(event) == "" || len(event) > 128 {
					return fmt.Errorf("notifications.events 값이 올바르지 않습니다")
				}
			}
		}
		webhookEnabled, _ := object["webhook_enabled"].(bool)
		if webhookEnabled && blankString(object["base_url"]) {
			return fmt.Errorf("Webhook 사용 시 base_url이 필요합니다")
		}
	}
	if key == "prometheus" {
		if raw, exists := object["queries"]; exists {
			if err := validateStringMapSetting("prometheus.queries", raw, false); err != nil {
				return err
			}
		}
	}
	if key == "kubernetes" {
		if pattern, exists := object["pod_username_regex"]; exists && strings.TrimSpace(pattern.(string)) != "" {
			if _, err := integration.CompilePodUsernamePattern(pattern.(string)); err != nil {
				return fmt.Errorf("kubernetes.%w", err)
			}
		}
	}
	if key == "llm_usage" {
		if raw, exists := object["promql"]; exists {
			if err := validateStringMapSetting("llm_usage.promql", raw, true); err != nil {
				return err
			}
		}
		if raw, exists := object["label_mappings"]; exists {
			if err := validateStringMapSetting("llm_usage.label_mappings", raw, false); err != nil {
				return err
			}
		}
	}
	if key == "security" {
		rotation, rotationOK := numericValue(object["key_rotation_days"])
		maximum, maximumOK := numericValue(object["key_max_lifetime_days"])
		if !rotationOK || !maximumOK {
			return fmt.Errorf("security.key_rotation_days와 key_max_lifetime_days가 필요합니다")
		}
		if rotation > maximum {
			return fmt.Errorf("security.key_rotation_days는 key_max_lifetime_days보다 클 수 없습니다")
		}
		rawPermissions, exists := object["key_permissions"]
		if !exists {
			return fmt.Errorf("security.key_permissions가 필요합니다")
		}
		permissions, ok := stringSettingArray(rawPermissions)
		if !ok {
			return fmt.Errorf("security.key_permissions는 문자열 배열이어야 합니다")
		}
		if len(permissions) > 0 {
			if err := store.ValidateScopes(permissions); err != nil {
				return fmt.Errorf("security.key_permissions: %w", err)
			}
		}
	}
	if key == "llm_usage" {
		if source, exists := object["source"]; !exists || source != "prometheus" {
			return fmt.Errorf("llm_usage.source는 prometheus만 지원합니다")
		}
		if blankString(object["pod_username_regex"]) || blankString(object["path_matcher"]) {
			return fmt.Errorf("llm_usage.pod_username_regex와 path_matcher가 필요합니다")
		}
		if _, exists := object["promql"]; !exists {
			return fmt.Errorf("llm_usage.promql이 필요합니다")
		}
		if err := validateLLMUsage(object); err != nil {
			return err
		}
	}
	return nil
}

func stringSettingArray(value any) ([]string, bool) {
	switch raw := value.(type) {
	case []string:
		return raw, true
	case []any:
		result := make([]string, 0, len(raw))
		for _, item := range raw {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			result = append(result, text)
		}
		return result, true
	default:
		return nil, false
	}
}

func validateStringMapSetting(name string, value any, requireCalls bool) error {
	items := map[string]string{}
	switch raw := value.(type) {
	case map[string]string:
		items = raw
	case map[string]any:
		for key, item := range raw {
			text, ok := item.(string)
			if !ok {
				return fmt.Errorf("%s의 %s 값은 문자열이어야 합니다", name, key)
			}
			items[key] = text
		}
	default:
		return fmt.Errorf("%s은 문자열 값의 JSON object여야 합니다", name)
	}
	if len(items) > 64 {
		return fmt.Errorf("%s은 최대 64개 항목만 허용합니다", name)
	}
	for key, value := range items {
		if strings.TrimSpace(key) == "" || len(key) > 100 || strings.TrimSpace(value) == "" || len(value) > 65536 {
			return fmt.Errorf("%s의 키 또는 값이 올바르지 않습니다", name)
		}
	}
	if requireCalls && strings.TrimSpace(items["calls"]) == "" {
		return fmt.Errorf("%s에 calls 쿼리가 필요합니다", name)
	}
	return nil
}

func blankString(value any) bool {
	text, ok := value.(string)
	return !ok || strings.TrimSpace(text) == ""
}

func canonicalSettingKey(key string) string {
	if key == "oidc" {
		return "auth.oidc"
	}
	return key
}
func isSecretKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "secret") || strings.HasSuffix(key, "token") || strings.HasSuffix(key, "api_key") || strings.Contains(key, "password")
}
func isMasked(value string) bool           { return strings.Trim(value, "*• ") == "" }
func canonicalSecretKey(key string) string { key = strings.TrimPrefix(key, "auth."); return key }
func canonicalNestedSecret(parent, key string) string {
	parent = strings.TrimPrefix(parent, "auth.")
	if parent == "oidc" || parent == "auth.oidc" {
		parent = "oidc"
	}
	if key == "bearer_token" {
		key = "token"
	}
	if parent == "notifications" && (key == "secret" || key == "webhook_secret" || key == "webhook_token") {
		return "webhook.secret"
	}
	return parent + "." + key
}

func validateLLMUsage(value any) error {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if rawMappings, exists := object["label_mappings"]; exists {
		if _, err := integration.DecodeLLMLabelMappings(rawMappings); err != nil {
			return err
		}
	}
	pattern, _ := object["pod_username_regex"].(string)
	if pattern == "" {
		return nil
	}
	_, err := integration.CompilePodUsernamePattern(pattern)
	return err
}

func (s *Server) integrationTest(w http.ResponseWriter, r *http.Request) {
	var req integration.TestRequest
	if err := decodeJSON(r, &req); err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	p := principal(r)
	if req.Type == "jupyterhub" {
		hubID := int64(numberFromAny(req.Config["hub_id"]))
		// Creating a Hub has no existing target on which a restricted grant can
		// be evaluated, so it remains global-only. An existing Hub preflight is
		// a target operation and may use that Hub's restricted hubs:write grant.
		if (hubID <= 0 && !p.Allows("hubs:write")) || (hubID > 0 && !p.AllowsTarget("hubs:write", hubID, "")) {
			apiError(w, r, http.StatusForbidden, "forbidden", "이 Hub 연결을 테스트할 권한이 없습니다")
			return
		}
	} else if !p.Allows("settings:write") {
		apiError(w, r, http.StatusForbidden, "forbidden", "이 연동을 테스트할 권한이 없습니다")
		return
	}
	settingKey := map[string]string{"oidc": "auth.oidc", "prometheus": "prometheus", "kubernetes": "kubernetes", "ai": "ai", "webhook": "notifications", "llm_usage": "llm_usage"}[req.Type]
	var savedConfig map[string]any
	var savedSecret string
	var savedSecretConfigured bool
	if settingKey != "" {
		settingKeys := []string{settingKey}
		if req.Type == "llm_usage" {
			settingKeys = append(settingKeys, "prometheus")
		}
		settings, secret, configured, err := s.Store.GetSettingsAndSecret(r.Context(), settingKeys, integrationSecretKey(req.Type))
		if err != nil {
			handleStoreError(w, r, err)
			return
		}
		savedSecret, savedSecretConfigured = secret, configured
		if raw, exists := settings[settingKey]; exists {
			if err := json.Unmarshal(raw, &savedConfig); err != nil {
				handleStoreError(w, r, err)
				return
			}
			for key, value := range savedConfig {
				if _, exists := req.Config[key]; !exists {
					req.Config[key] = value
				}
			}
		}
		if req.Type == "llm_usage" {
			var prometheus map[string]any
			raw, exists := settings["prometheus"]
			if exists {
				if err := json.Unmarshal(raw, &prometheus); err != nil {
					handleStoreError(w, r, err)
					return
				}
				savedConfig = prometheus
				if _, ok := req.Config["base_url"]; !ok {
					req.Config["base_url"] = prometheus["base_url"]
				}
				if _, ok := req.Config["verify_tls"]; !ok {
					req.Config["verify_tls"] = prometheus["verify_tls"]
				}
			}
		}
	}
	normalizeIntegrationRequest(&req)
	if req.Secret == "" {
		if req.Type == "jupyterhub" {
			hubID := int64(numberFromAny(req.Config["hub_id"]))
			if hubID <= 0 {
				apiError(w, r, http.StatusBadRequest, "hub_token_required", "새 Hub 연결 테스트에는 관리자 API token을 입력해야 합니다")
				return
			}
			hub, savedToken, err := s.Store.GetHubCredential(r.Context(), hubID)
			if err != nil {
				handleStoreError(w, r, err)
				return
			}
			savedConfig = map[string]any{"base_url": hub.BaseURL, "verify_tls": hub.VerifyTLS}
			if !integrationBindingMatches(req.Type, req.Config, savedConfig) {
				apiError(w, r, http.StatusBadRequest, "hub_token_reentry_required", "Hub URL 또는 TLS 검증 설정을 바꿔 테스트하려면 관리자 API token을 다시 입력해야 합니다")
				return
			}
			req.Secret = savedToken
			if req.Secret == "" {
				apiError(w, r, http.StatusBadRequest, "hub_token_required", "저장된 Hub token이 없습니다. 관리자 API token을 입력해 주세요")
				return
			}
		} else {
			key := integrationSecretKey(req.Type)
			if key != "" && savedSecretConfigured {
				if !integrationBindingMatches(req.Type, req.Config, savedConfig) {
					apiError(w, r, http.StatusBadRequest, "secret_reentry_required", "연동 주소 또는 TLS 검증 설정을 바꿔 테스트하려면 인증정보를 다시 입력해야 합니다")
					return
				}
				req.Secret = savedSecret
			}
		}
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	result := integration.TestConnection(ctx, req)
	// This endpoint validates candidate form values and never changes persisted
	// configuration or operational health. Saved Hub tests have their own
	// /hubs/{id}/test endpoint; provider health can be recorded by its collector.
	status := "success"
	if !result.Success {
		status = "failure"
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "integration.test", req.Type, "", status, result.Error, nil, map[string]any{"success": result.Success, "latency_ms": result.LatencyMS, "version": result.Version}))
	data(w, http.StatusOK, result)
}

func integrationBindingMatches(kind string, candidate, saved map[string]any) bool {
	if len(candidate) == 0 || len(saved) == 0 {
		return false
	}
	targetKey := "base_url"
	if kind == "oidc" {
		targetKey = "issuer_url"
	}
	candidateTarget := canonicalIntegrationTarget(candidate[targetKey])
	savedTarget := canonicalIntegrationTarget(saved[targetKey])
	if candidateTarget == "" || savedTarget == "" || candidateTarget != savedTarget {
		return false
	}
	if kind == "oidc" {
		candidateClientID, _ := candidate["client_id"].(string)
		savedClientID, _ := saved["client_id"].(string)
		if strings.TrimSpace(candidateClientID) == "" || strings.TrimSpace(candidateClientID) != strings.TrimSpace(savedClientID) {
			return false
		}
	}
	return integrationVerifyTLS(candidate) == integrationVerifyTLS(saved)
}

func canonicalIntegrationTarget(value any) string {
	target, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(target), "/")
}

func integrationVerifyTLS(config map[string]any) bool {
	if value, ok := config["verify_tls"].(bool); ok {
		return value
	}
	return true
}

func normalizeIntegrationRequest(req *integration.TestRequest) {
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	if _, ok := req.Config["base_url"]; !ok {
		for _, alias := range []string{"url", "api_url"} {
			if value, exists := req.Config[alias]; exists {
				req.Config["base_url"] = value
				break
			}
		}
	}
	if _, ok := req.Config["verify_tls"]; !ok {
		if value, exists := req.Config["tls_verify"]; exists {
			req.Config["verify_tls"] = value
		}
	}
	for _, key := range []string{"token", "api_key", "client_secret", "secret"} {
		if value, ok := req.Config[key].(string); ok && value != "" && !isMasked(value) {
			req.Secret = value
			delete(req.Config, key)
			break
		}
	}
}

func integrationSecretKey(kind string) string {
	return map[string]string{"oidc": "oidc.client_secret", "prometheus": "prometheus.token", "kubernetes": "kubernetes.token", "ai": "ai.api_key", "webhook": "webhook.secret", "llm_usage": "prometheus.token"}[kind]
}

func (s *Server) managedUsers(w http.ResponseWriter, r *http.Request) {
	access, ok := scopedAccess(w, r, "users:read")
	if !ok {
		return
	}
	items, page, err := s.Store.ListManagedUsersWithAccess(r.Context(), queryInt(r, "page", 1), queryInt(r, "page_size", 20), int64(queryInt(r, "hub_id", 0)), r.URL.Query().Get("search"), access, listSort(r))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	list(w, items, page)
}

var userDetailUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+-]{0,127}$`)

func (s *Server) userDetail(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if !userDetailUsernamePattern.MatchString(username) {
		apiError(w, r, http.StatusBadRequest, "invalid_username", "사용자 이름은 영문 또는 숫자로 시작하는 1~128자의 ID 형식이어야 합니다")
		return
	}
	page, limit, includeLLM, err := userDetailQuery(r)
	if err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	detail, err := s.Store.GetUserDetail(r.Context(), username, store.UserDetailOptions{Page: page, Limit: limit, IncludeLLM: includeLLM})
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, detail)
}

func userDetailQuery(r *http.Request) (int, int, bool, error) {
	page, err := boundedUserDetailQueryInt(r, "page", 1, 1, 10000)
	if err != nil {
		return 0, 0, false, err
	}
	limit, err := boundedUserDetailQueryInt(r, "limit", 50, 1, 100)
	if err != nil {
		return 0, 0, false, err
	}
	includeLLM := true
	if raw := r.URL.Query().Get("include_llm"); raw != "" {
		includeLLM, err = strconv.ParseBool(raw)
		if err != nil {
			return 0, 0, false, fmt.Errorf("include_llm은 true 또는 false여야 합니다")
		}
	}
	return page, limit, includeLLM, nil
}

func boundedUserDetailQueryInt(r *http.Request, name string, fallback, min, max int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("%s는 %d~%d 범위여야 합니다", name, min, max)
	}
	return value, nil
}
func (s *Server) localUsers(w http.ResponseWriter, r *http.Request) {
	items, page, err := s.Store.ListLocalUsers(r.Context(), queryInt(r, "page", 1), queryInt(r, "page_size", 20), r.URL.Query().Get("search"))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	list(w, items, page)
}
func (s *Server) servers(w http.ResponseWriter, r *http.Request) {
	access, ok := scopedAccess(w, r, "servers:read")
	if !ok {
		return
	}
	items, page, err := s.Store.ListServersWithAccess(r.Context(), queryInt(r, "page", 1), queryInt(r, "page_size", 20), int64(queryInt(r, "hub_id", 0)), r.URL.Query().Get("status"), r.URL.Query().Get("search"), access, listSort(r))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	list(w, items, page)
}

func (s *Server) rolesList(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListRoles(r.Context())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, items)
}
func (s *Server) roleSave(w http.ResponseWriter, r *http.Request) {
	var role store.Role
	if err := decodeJSON(r, &role); err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if r.PathValue("id") != "" {
		id, err := intPath(r, "id")
		if err != nil {
			apiError(w, r, 400, "invalid_id", "역할 ID가 올바르지 않습니다")
			return
		}
		role.ID = id
		current, err := s.Store.GetRole(r.Context(), id)
		if err != nil {
			handleStoreError(w, r, err)
			return
		}
		if !permissionsWithin(current.Permissions, principal(r).GlobalPermissionSet()) {
			apiError(w, r, http.StatusForbidden, "role_grant_ceiling", "본인 권한 범위를 넘는 역할은 변경할 수 없습니다")
			return
		}
		if current.System {
			role.Key = current.Key
		}
	}
	if err := store.ValidateScopes(role.Permissions); err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_role_permissions", err.Error())
		return
	}
	if !permissionsWithin(role.Permissions, principal(r).GlobalPermissionSet()) {
		apiError(w, r, http.StatusForbidden, "role_grant_ceiling", "본인이 보유한 권한 범위 안에서만 역할을 저장할 수 있습니다")
		return
	}
	saved, err := s.Store.SaveRoleAuthorized(r.Context(), role, principal(r).User.ID)
	if err != nil {
		if handleRBACMutationError(w, r, err) {
			return
		}
		apiError(w, r, 400, "invalid_role", err.Error())
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "role.save", "role", strconv.FormatInt(saved.ID, 10), "success", "", nil, saved))
	data(w, http.StatusOK, saved)
}
func (s *Server) roleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "역할 ID가 올바르지 않습니다")
		return
	}
	role, err := s.Store.GetRole(r.Context(), id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !permissionsWithin(role.Permissions, principal(r).GlobalPermissionSet()) {
		apiError(w, r, http.StatusForbidden, "role_grant_ceiling", "본인 권한 범위를 넘는 역할은 삭제할 수 없습니다")
		return
	}
	if err := s.Store.DeleteRoleAuthorized(r.Context(), id, principal(r).User.ID); err != nil {
		if handleRBACMutationError(w, r, err) {
			return
		}
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "role.delete", "role", strconv.FormatInt(id, 10), "success", "", nil, nil))
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) userRolesPut(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "사용자 ID가 올바르지 않습니다")
		return
	}
	var input struct {
		RoleIDs  *[]int64             `json:"role_ids"`
		Bindings *[]store.RoleBinding `json:"bindings"`
	}
	if decodeJSON(r, &input) != nil {
		apiError(w, r, 400, "invalid_json", "role_ids 또는 bindings가 필요합니다")
		return
	}
	if (input.RoleIDs == nil) == (input.Bindings == nil) {
		apiError(w, r, http.StatusBadRequest, "invalid_role_bindings", "role_ids와 bindings 중 하나만 입력해야 합니다")
		return
	}
	bindings := []store.RoleBinding{}
	legacy := input.RoleIDs != nil
	if legacy {
		for _, roleID := range *input.RoleIDs {
			bindings = append(bindings, store.RoleBinding{RoleID: roleID, ScopeMode: "global"})
		}
	} else {
		for _, binding := range *input.Bindings {
			bindings = append(bindings, store.RoleBinding{RoleID: binding.RoleID, ScopeMode: binding.ScopeMode, Scopes: append([]store.ScopeClause(nil), binding.Scopes...)})
		}
	}
	if len(bindings) > 64 {
		apiError(w, r, http.StatusBadRequest, "too_many_roles", "사용자에게는 최대 64개 역할만 할당할 수 있습니다")
		return
	}
	actorPermissions := principal(r).GlobalPermissionSet()
	currentUser, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !permissionsWithin(currentUser.Permissions, actorPermissions) {
		apiError(w, r, http.StatusForbidden, "role_grant_ceiling", "본인 권한 범위를 넘는 사용자의 역할은 변경할 수 없습니다")
		return
	}
	seenRoleIDs := make(map[int64]struct{}, len(bindings))
	for _, binding := range bindings {
		roleID := binding.RoleID
		if roleID <= 0 {
			apiError(w, r, http.StatusBadRequest, "invalid_role_id", "역할 ID가 올바르지 않습니다")
			return
		}
		if _, duplicate := seenRoleIDs[roleID]; duplicate {
			apiError(w, r, http.StatusBadRequest, "duplicate_role_id", "중복된 역할 ID가 있습니다")
			return
		}
		seenRoleIDs[roleID] = struct{}{}
		role, err := s.Store.GetRole(r.Context(), roleID)
		if err != nil {
			handleStoreError(w, r, err)
			return
		}
		if !permissionsWithin(role.Permissions, actorPermissions) {
			apiError(w, r, http.StatusForbidden, "role_grant_ceiling", "본인이 보유한 권한 범위 안의 역할만 할당할 수 있습니다")
			return
		}
	}
	if legacy {
		err = s.Store.SetUserRolesAuthorized(r.Context(), id, *input.RoleIDs, principal(r).User.ID)
	} else {
		err = s.Store.SetUserRoleBindingsAuthorized(r.Context(), id, bindings, principal(r).User.ID)
	}
	if err != nil {
		if handleRBACMutationError(w, r, err) {
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			handleStoreError(w, r, err)
			return
		}
		apiError(w, r, http.StatusBadRequest, "invalid_role_bindings", err.Error())
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "user.roles.update", "user", strconv.FormatInt(id, 10), "success", "", nil, map[string]any{"bindings": bindings}))
	user, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, user)
}

func permissionsWithin(candidate, ceiling []string) bool {
	return len(candidate) == 0 || store.ScopesWithinPermissions(candidate, ceiling)
}

func handleRBACMutationError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, store.ErrImmutableRole):
		apiError(w, r, http.StatusConflict, "protected_super_admin", "super_admin 역할의 전체 권한(*)은 변경할 수 없습니다")
		return true
	case errors.Is(err, store.ErrLastSuperAdmin):
		apiError(w, r, http.StatusConflict, "last_super_admin", "활성 최고 관리자가 최소 1명은 남아 있어야 합니다")
		return true
	case errors.Is(err, store.ErrScopeRequired):
		apiError(w, r, http.StatusConflict, "scope_aware_client_required", "범위가 지정된 역할은 bindings 형식으로만 변경할 수 있습니다")
		return true
	case errors.Is(err, store.ErrRoleGrantCeiling):
		apiError(w, r, http.StatusForbidden, "role_grant_ceiling", "현재 보유한 전역 권한 범위를 넘는 역할 변경은 수행할 수 없습니다")
		return true
	default:
		return false
	}
}

func (s *Server) keysList(w http.ResponseWriter, r *http.Request) {
	if !requireInteractiveKeyManagement(w, r) {
		return
	}
	items, err := s.Store.ListAPIKeys(r.Context(), principal(r).User.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, keyView(item))
	}
	data(w, http.StatusOK, views)
}
func (s *Server) keyCreate(w http.ResponseWriter, r *http.Request) {
	if !requireInteractiveKeyManagement(w, r) {
		return
	}
	var input struct {
		Name          string     `json:"name"`
		Scopes        []string   `json:"scopes"`
		Permissions   []string   `json:"permissions"`
		ExpiresAt     *time.Time `json:"expires_at"`
		ExpiresInDays int        `json:"expires_in_days"`
	}
	if err := decodeJSON(r, &input); err != nil || strings.TrimSpace(input.Name) == "" {
		apiError(w, r, 400, "invalid_key", "키 이름이 필요합니다")
		return
	}
	if len(input.Scopes) == 0 {
		input.Scopes = input.Permissions
	}
	if input.ExpiresAt == nil && input.ExpiresInDays > 0 {
		expires := time.Now().UTC().Add(time.Duration(input.ExpiresInDays) * 24 * time.Hour)
		input.ExpiresAt = &expires
	}
	if err := store.ValidateScopes(input.Scopes); err != nil {
		apiError(w, r, 400, "invalid_scopes", err.Error())
		return
	}
	p := principal(r)
	if !store.ScopesWithinPermissions(input.Scopes, p.UserPermissions) {
		apiError(w, r, http.StatusForbidden, "scope_escalation", "API 키 권한은 현재 사용자 권한의 일부여야 합니다")
		return
	}
	policy, err := s.loadAPIKeyPolicy(r.Context())
	if err != nil {
		apiError(w, r, http.StatusServiceUnavailable, "key_policy_unavailable", err.Error())
		return
	}
	if err := applyAPIKeyPolicy(policy, input.Scopes, &input.ExpiresAt); err != nil {
		apiError(w, r, http.StatusBadRequest, "key_policy_denied", err.Error())
		return
	}
	key, secret, err := s.Store.CreateAPIKey(r.Context(), p.User.ID, input.Name, input.Scopes, input.ExpiresAt, nil)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "api_key.create", "api_key", strconv.FormatInt(key.ID, 10), "success", "", nil, map[string]any{"name": key.Name, "scopes": key.Scopes}))
	data(w, http.StatusCreated, map[string]any{"key": keyView(key), "secret": secret, "warning": "이 키는 다시 표시되지 않습니다"})
}
func (s *Server) keyRotate(w http.ResponseWriter, r *http.Request) {
	if !requireInteractiveKeyManagement(w, r) {
		return
	}
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "키 ID가 올바르지 않습니다")
		return
	}
	p := principal(r)
	old, err := s.Store.GetAPIKey(r.Context(), p.User.ID, id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !store.ScopesWithinPermissions(old.Scopes, p.UserPermissions) {
		apiError(w, r, http.StatusForbidden, "scope_no_longer_allowed", "현재 사용자 권한에서 제외된 키는 회전할 수 없습니다")
		return
	}
	var expiresAt *time.Time
	policy, err := s.loadAPIKeyPolicy(r.Context())
	if err != nil {
		apiError(w, r, http.StatusServiceUnavailable, "key_policy_unavailable", err.Error())
		return
	}
	if err := applyAPIKeyPolicy(policy, old.Scopes, &expiresAt); err != nil {
		apiError(w, r, http.StatusBadRequest, "key_policy_denied", err.Error())
		return
	}
	key, secret, err := s.Store.RotateAPIKey(r.Context(), p.User.ID, id, expiresAt)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "api_key.rotate", "api_key", strconv.FormatInt(id, 10), "success", "", nil, map[string]any{"new_id": key.ID}))
	data(w, http.StatusOK, map[string]any{"key": keyView(key), "secret": secret, "warning": "이 키는 다시 표시되지 않습니다"})
}
func (s *Server) keyRevoke(w http.ResponseWriter, r *http.Request) {
	if !requireInteractiveKeyManagement(w, r) {
		return
	}
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "키 ID가 올바르지 않습니다")
		return
	}
	p := principal(r)
	if err := s.Store.RevokeAPIKey(r.Context(), p.User.ID, id, "revoked"); err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "api_key.revoke", "api_key", strconv.FormatInt(id, 10), "success", "", nil, nil))
	w.WriteHeader(http.StatusNoContent)
}

func requireInteractiveKeyManagement(w http.ResponseWriter, r *http.Request) bool {
	if principal(r).APIKeyID == 0 {
		return true
	}
	apiError(w, r, http.StatusForbidden, "interactive_session_required", "API 키 조회·발급·회전·폐기는 브라우저 로그인 세션에서만 수행할 수 있습니다")
	return false
}

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	items, page, err := s.Store.ListAudit(r.Context(), queryInt(r, "page", 1), queryInt(r, "page_size", 50), listSort(r))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	list(w, items, page)
}
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	from, to, rangeErr := boundedTimeRange(r, now.Add(-time.Hour), now, 31*24*time.Hour)
	if rangeErr != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_range", rangeErr.Error())
		return
	}
	metric := r.URL.Query().Get("metric")
	items, gpuBlocked, err := s.Store.Metrics(r.Context(), from, to, metric, queryInt(r, "limit", 1000))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	// 어떤 이름이 GPU 지표인지는 store가 단독으로 판정한다. 여기서 목록을 따로
	// 두면 판정이 갈라져 일부 별칭이 '기능 꺼짐' 대신 '표본 없음'으로 보인다.
	if gpuBlocked {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{}, "feature_enabled": false, "meta": map[string]any{"total": 0}})
		return
	}
	data(w, http.StatusOK, items)
}

func boundedTimeRange(r *http.Request, defaultFrom, defaultTo time.Time, maximum time.Duration) (time.Time, time.Time, error) {
	parse := func(name string, fallback time.Time) (time.Time, error) {
		raw := strings.TrimSpace(r.URL.Query().Get(name))
		if raw == "" {
			return fallback, nil
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s는 RFC3339 날짜·시간이어야 합니다", name)
		}
		return parsed.UTC(), nil
	}
	to, err := parse("to", defaultTo)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	from, err := parse("from", defaultFrom)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !from.Before(to) || to.Sub(from) > maximum {
		return time.Time{}, time.Time{}, fmt.Errorf("조회 시작은 종료보다 앞서야 하며 기간은 최대 %d일입니다", int(maximum.Hours()/24))
	}
	return from, to, nil
}
func (s *Server) gpus(w http.ResponseWriter, r *http.Request) {
	var features struct {
		GPU bool `json:"gpu_monitoring"`
	}
	var raw map[string]bool
	if s.Store.GetSetting(r.Context(), "features", &raw) == nil {
		features.GPU = raw["gpu_monitoring"]
	}
	if !features.GPU {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{}, "feature_enabled": false, "meta": map[string]any{"total": 0}})
		return
	}
	items, err := s.Store.GPUUsage(r.Context())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "feature_enabled": true, "meta": map[string]any{"total": len(items)}})
}

func keyView(key store.APIKey) map[string]any {
	raw, _ := json.Marshal(key)
	result := map[string]any{}
	_ = json.Unmarshal(raw, &result)
	result["permissions"] = key.Scopes
	return result
}
