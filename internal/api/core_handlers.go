package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/store"
)

func (s *Server) registerCore(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/dashboard", s.require("dashboard:read", s.dashboard))
	mux.HandleFunc("GET /api/v1/live", s.require("dashboard:read", s.live))
	mux.HandleFunc("GET /api/v1/dashboard/live", s.require("dashboard:read", s.live))
	mux.HandleFunc("GET /api/v1/usage", s.require("usage:read", s.usage))
	mux.HandleFunc("GET /api/v1/llm-usage", s.require("usage:read", s.llmUsage))
	mux.HandleFunc("GET /api/v1/llm-usage/live", s.require("usage:read", s.llmUsageLive))
	mux.HandleFunc("GET /api/v1/settings", s.require("settings:read", s.settingsGet))
	mux.HandleFunc("PUT /api/v1/settings", s.require("settings:write", s.settingsPut))
	mux.HandleFunc("POST /api/v1/integrations/test", s.require("settings:write", s.integrationTest))
	mux.HandleFunc("GET /api/v1/users", s.require("users:read", s.managedUsers))
	mux.HandleFunc("GET /api/v1/users/{username}", s.require("users:read", s.userDetail))
	mux.HandleFunc("GET /api/v1/local-users", s.require("users:read", s.localUsers))
	mux.HandleFunc("GET /api/v1/servers", s.require("servers:read", s.servers))
	mux.HandleFunc("GET /api/v1/roles", s.require("roles:read", s.rolesList))
	mux.HandleFunc("POST /api/v1/roles", s.require("roles:write", s.roleSave))
	mux.HandleFunc("PUT /api/v1/roles/{id}", s.require("roles:write", s.roleSave))
	mux.HandleFunc("DELETE /api/v1/roles/{id}", s.require("roles:write", s.roleDelete))
	mux.HandleFunc("PUT /api/v1/local-users/{id}/roles", s.require("roles:write", s.userRolesPut))
	mux.HandleFunc("GET /api/v1/keys", s.require("profile:keys", s.keysList))
	mux.HandleFunc("POST /api/v1/keys", s.require("profile:keys", s.keyCreate))
	mux.HandleFunc("POST /api/v1/keys/{id}/rotate", s.require("profile:keys", s.keyRotate))
	mux.HandleFunc("POST /api/v1/keys/{id}/revoke", s.require("profile:keys", s.keyRevoke))
	mux.HandleFunc("GET /api/v1/audit", s.require("audit:read", s.auditList))
	mux.HandleFunc("GET /api/v1/metrics", s.require("metrics:read", s.metrics))
	mux.HandleFunc("GET /api/v1/gpus", s.require("gpu:read", s.gpus))
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.Store.LiveSnapshot(r.Context())
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
		snapshot, err := s.Store.LiveSnapshot(r.Context())
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

func applyDashboardFilters(snapshot map[string]any, r *http.Request) {
	sessions, ok := snapshot["live_users"].([]map[string]any)
	if !ok {
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
	summary := map[string]any{"running_servers": 0, "idle_sessions": 0, "long_running_sessions": 0, "cpu_usage": float64(0), "memory_usage": float64(0)}
	gpuWaste := []map[string]any{}
	featureMap, _ := snapshot["feature_enabled"].(map[string]bool)
	gpuOn := featureMap["gpu_monitoring"]
	for _, session := range filtered {
		if stale, _ := session["stale"].(bool); stale {
			continue
		}
		summary["running_servers"] = summary["running_servers"].(int) + 1
		username, _ := session["username"].(string)
		users[username] = true
		summary["cpu_usage"] = summary["cpu_usage"].(float64) + numberFromAny(session["cpu_cores"])
		summary["memory_usage"] = summary["memory_usage"].(float64) + numberFromAny(session["memory_bytes"])
		if idle, _ := session["idle_candidate"].(bool); idle {
			summary["idle_sessions"] = summary["idle_sessions"].(int) + 1
		}
		if numberFromAny(session["runtime_seconds"]) >= 86400 {
			summary["long_running_sessions"] = summary["long_running_sessions"].(int) + 1
		}
		if gpuOn {
			if _, ok := summary["gpu_usage"]; !ok {
				summary["gpu_usage"], summary["vram_usage"] = float64(0), float64(0)
			}
			summary["gpu_usage"] = summary["gpu_usage"].(float64) + numberFromAny(session["gpu_count"])
			summary["vram_usage"] = summary["vram_usage"].(float64) + numberFromAny(session["vram_bytes"])
			if waste, _ := session["waste_candidate"].(bool); waste {
				gpuWaste = append(gpuWaste, session)
			}
		}
	}
	summary["active_users"] = len(users)
	snapshot["summary"] = summary
	snapshot["gpu_waste"] = gpuWaste
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	to := parseTimeQuery(r, "to", now)
	from := parseTimeQuery(r, "from", to.Add(-30*24*time.Hour))
	if !from.Before(to) || to.Sub(from) > 366*24*time.Hour {
		apiError(w, r, http.StatusBadRequest, "invalid_range", "조회 기간은 1일 이상 366일 이하여야 합니다")
		return
	}
	result, err := s.Store.Usage(r.Context(), from, to, r.URL.Query().Get("granularity"), r.URL.Query().Get("group_by"))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, result)
}

func (s *Server) llmUsage(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	to := parseTimeQuery(r, "to", now)
	fallback := to.Add(-24 * time.Hour)
	switch r.URL.Query().Get("range") {
	case "week":
		fallback = to.Add(-7 * 24 * time.Hour)
	case "month":
		fallback = to.Add(-30 * 24 * time.Hour)
	}
	from := parseTimeQuery(r, "from", fallback)
	result, err := s.Store.LLMUsage(r.Context(), from, to, r.URL.Query().Get("group_by"))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, result)
}

func (s *Server) llmUsageLive(w http.ResponseWriter, r *http.Request) { s.live(w, r) }

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
	result := map[string]any{"settings": settings, "secrets": secrets}
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
	// Accept either {settings:{...}} or a direct setting map.
	if nested, ok := input["settings"].(map[string]any); ok {
		input = nested
	}
	for key, value := range input {
		if secret, ok := value.(string); ok && isSecretKey(key) {
			if secret != "" && !isMasked(secret) {
				if err := s.Store.SetSecret(r.Context(), canonicalSecretKey(key), secret, p.User.ID); err != nil {
					handleStoreError(w, r, err)
					return
				}
				secretKeys = append(secretKeys, canonicalSecretKey(key))
			}
			continue
		}
		if object, ok := value.(map[string]any); ok {
			for nestedKey, nestedValue := range object {
				if !isSecretKey(nestedKey) {
					continue
				}
				secret, _ := nestedValue.(string)
				delete(object, nestedKey)
				if secret != "" && !isMasked(secret) {
					secretKey := canonicalNestedSecret(key, nestedKey)
					if err := s.Store.SetSecret(r.Context(), secretKey, secret, p.User.ID); err != nil {
						handleStoreError(w, r, err)
						return
					}
					secretKeys = append(secretKeys, secretKey)
				}
			}
		}
		canonical := canonicalSettingKey(key)
		if canonical == "llm_usage" {
			if err := validateLLMUsage(value); err != nil {
				apiError(w, r, http.StatusBadRequest, "invalid_llm_usage", err.Error())
				return
			}
		}
		if err := s.Store.SetSetting(r.Context(), canonical, value, p.User.ID); err != nil {
			handleStoreError(w, r, err)
			return
		}
		changed = append(changed, canonical)
	}
	sort.Strings(changed)
	sort.Strings(secretKeys)
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "settings.update", "settings", "", "success", "", nil, map[string]any{"changed": changed, "secrets_changed": secretKeys}))
	s.settingsGet(w, r)
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
	settingKey := map[string]string{"oidc": "auth.oidc", "jupyterhub": "jupyterhub", "prometheus": "prometheus", "kubernetes": "kubernetes", "ai": "ai", "webhook": "notifications", "llm_usage": "llm_usage"}[req.Type]
	if settingKey != "" {
		var saved map[string]any
		if s.Store.GetSetting(r.Context(), settingKey, &saved) == nil {
			for key, value := range saved {
				if _, exists := req.Config[key]; !exists {
					req.Config[key] = value
				}
			}
		}
	}
	if req.Type == "llm_usage" {
		var prometheus map[string]any
		if s.Store.GetSetting(r.Context(), "prometheus", &prometheus) == nil {
			if _, ok := req.Config["base_url"]; !ok {
				req.Config["base_url"] = prometheus["base_url"]
			}
			if _, ok := req.Config["verify_tls"]; !ok {
				req.Config["verify_tls"] = prometheus["verify_tls"]
			}
		}
	}
	normalizeIntegrationRequest(&req)
	if req.Secret == "" {
		key := integrationSecretKey(req.Type)
		if key != "" {
			req.Secret, _ = s.Store.GetSecret(r.Context(), key)
		}
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	result := integration.TestConnection(ctx, req)
	status := "success"
	if !result.Success {
		status = "failure"
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "integration.test", req.Type, "", status, result.Error, nil, map[string]any{"success": result.Success, "latency_ms": result.LatencyMS, "version": result.Version}))
	data(w, http.StatusOK, result)
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
	return map[string]string{"oidc": "oidc.client_secret", "jupyterhub": "jupyterhub.api_token", "prometheus": "prometheus.token", "kubernetes": "kubernetes.token", "ai": "ai.api_key", "webhook": "webhook.secret", "llm_usage": "prometheus.token"}[kind]
}

func (s *Server) managedUsers(w http.ResponseWriter, r *http.Request) {
	items, page, err := s.Store.ListManagedUsers(r.Context(), queryInt(r, "page", 1), queryInt(r, "page_size", 20), int64(queryInt(r, "hub_id", 0)), r.URL.Query().Get("search"))
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
	items, page, err := s.Store.ListServers(r.Context(), queryInt(r, "page", 1), queryInt(r, "page_size", 20), int64(queryInt(r, "hub_id", 0)), r.URL.Query().Get("status"), r.URL.Query().Get("search"))
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
	}
	saved, err := s.Store.SaveRole(r.Context(), role)
	if err != nil {
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
	if err := s.Store.DeleteRole(r.Context(), id); err != nil {
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
		RoleIDs []int64 `json:"role_ids"`
	}
	if decodeJSON(r, &input) != nil {
		apiError(w, r, 400, "invalid_json", "role_ids가 필요합니다")
		return
	}
	if err := s.Store.SetUserRoles(r.Context(), id, input.RoleIDs); err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "user.roles.update", "user", strconv.FormatInt(id, 10), "success", "", nil, map[string]any{"role_ids": input.RoleIDs}))
	user, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, user)
}

func (s *Server) keysList(w http.ResponseWriter, r *http.Request) {
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
	if err := applyAPIKeyPolicy(s.loadAPIKeyPolicy(r.Context()), input.Scopes, &input.ExpiresAt); err != nil {
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
	if err := applyAPIKeyPolicy(s.loadAPIKeyPolicy(r.Context()), old.Scopes, &expiresAt); err != nil {
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

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	items, page, err := s.Store.ListAudit(r.Context(), queryInt(r, "page", 1), queryInt(r, "page_size", 50))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	list(w, items, page)
}
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	from := parseTimeQuery(r, "from", now.Add(-time.Hour))
	to := parseTimeQuery(r, "to", now)
	metric := r.URL.Query().Get("metric")
	lowerMetric := strings.ToLower(metric)
	if strings.Contains(lowerMetric, "gpu") || strings.Contains(lowerMetric, "vram") || strings.Contains(lowerMetric, "dcgm") {
		var features map[string]bool
		_ = s.Store.GetSetting(r.Context(), "features", &features)
		if !features["gpu_monitoring"] {
			writeJSON(w, http.StatusOK, map[string]any{"data": []any{}, "feature_enabled": false, "meta": map[string]any{"total": 0}})
			return
		}
	}
	items, err := s.Store.Metrics(r.Context(), from, to, metric, queryInt(r, "limit", 1000))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, items)
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
	items, page, err := s.Store.ListResources(r.Context(), "gpu", 1, 200, "", "", nil)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": flattenResources(items), "feature_enabled": true, "meta": page})
}

func keyView(key store.APIKey) map[string]any {
	raw, _ := json.Marshal(key)
	result := map[string]any{}
	_ = json.Unmarshal(raw, &result)
	result["permissions"] = key.Scopes
	return result
}
