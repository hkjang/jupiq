package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type TestRequest struct {
	Type      string         `json:"type"`
	Config    map[string]any `json:"config"`
	Secret    string         `json:"secret,omitempty"`
	SecretKey string         `json:"secret_key,omitempty"`
}

type Check struct {
	Name      string `json:"name"`
	Success   bool   `json:"success"`
	Detail    string `json:"detail"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
}

type TestResult struct {
	Type           string    `json:"type"`
	Success        bool      `json:"success"`
	LatencyMS      int64     `json:"latency_ms"`
	ResponseTimeMS int64     `json:"response_time_ms"`
	TLS            bool      `json:"tls"`
	Version        string    `json:"remote_version,omitempty"`
	VersionAlias   string    `json:"version,omitempty"`
	CheckedAt      time.Time `json:"checked_at"`
	Checks         []Check   `json:"checks"`
	Steps          []Check   `json:"steps"`
	Error          string    `json:"error,omitempty"`
	Remediation    string    `json:"remediation,omitempty"`
	Guidance       string    `json:"guidance,omitempty"`
}

func TestConnection(ctx context.Context, req TestRequest) (result TestResult) {
	started := time.Now()
	result = TestResult{Type: req.Type, CheckedAt: started.UTC(), Checks: []Check{}}
	defer func() {
		result.LatencyMS = time.Since(started).Milliseconds()
		result.ResponseTimeMS = result.LatencyMS
		result.VersionAlias = result.Version
		result.Steps = result.Checks
		result.Guidance = result.Remediation
	}()
	baseURL, _ := req.Config["base_url"].(string)
	if req.Type == "oidc" {
		baseURL, _ = req.Config["issuer_url"].(string)
	}
	u, err := ValidateEndpoint(baseURL)
	if err != nil {
		result.Error, result.Remediation = err.Error(), "http(s) 기반의 정확한 내부 서비스 주소를 입력하세요. URL에는 계정 정보를 넣지 마세요."
		result.Checks = append(result.Checks, Check{Name: "url", Success: false, Detail: result.Error})
		return result
	}
	result.TLS = u.Scheme == "https"
	result.Checks = append(result.Checks, Check{Name: "url", Success: true, Detail: "URL 형식 확인"})
	verifyTLS := true
	if value, ok := req.Config["verify_tls"].(bool); ok {
		verifyTLS = value
	}
	client := newHTTPClient(HTTPOptions{VerifyTLS: verifyTLS, Timeout: 10 * time.Second})
	llmLabelMappings := map[string]string{}
	firstResponse := true
	run := func(name, method, endpoint string, body []byte, target any, retry bool) bool {
		stepStarted := time.Now()
		authScheme := "Bearer"
		if req.Type == "jupyterhub" {
			authScheme = "token"
		}
		authSecret := integrationTestAuthSecret(req.Type, req.Secret)
		status, header, callErr := doJSONWithAuthScheme(ctx, client, method, endpoint, authSecret, authScheme, bytes.NewReader(body), target, retry)
		latency := time.Since(stepStarted).Milliseconds()
		if callErr != nil {
			result.Error = safeConnectionError(callErr)
			result.Remediation = remediation(status, callErr)
			result.Checks = append(result.Checks, Check{Name: name, Success: false, Detail: result.Error, LatencyMS: latency})
			return false
		}
		result.Checks = append(result.Checks, Check{Name: name, Success: true, Detail: fmt.Sprintf("HTTP %d", status), LatencyMS: latency})
		if firstResponse && result.TLS {
			result.Checks = append(result.Checks, Check{Name: "tls", Success: true, Detail: tlsDetail(header, verifyTLS)})
		}
		firstResponse = false
		return true
	}

	join := func(path string) (string, bool) {
		endpoint, joinErr := joinURL(baseURL, path)
		if joinErr != nil {
			result.Error, result.Remediation = joinErr.Error(), "연동 유형과 기본 주소를 다시 확인하세요."
			return "", false
		}
		return endpoint, true
	}

	switch req.Type {
	case "jupyterhub":
		infoURL, ok := join("hub/api/info")
		if !ok {
			return result
		}
		var info map[string]any
		if !run("연결·버전", http.MethodGet, infoURL, nil, &info, true) {
			return result
		}
		result.Version = extractVersion(info)
		usersURL, ok := join("hub/api/users")
		if !ok {
			return result
		}
		usersURL += "?limit=1"
		var users []json.RawMessage
		if !run("관리자 사용자 조회 권한", http.MethodGet, usersURL, nil, &users, true) {
			return result
		}
	case "prometheus", "llm_usage":
		if req.Type == "llm_usage" {
			source, _ := req.Config["source"].(string)
			if source != "" && source != "prometheus" {
				result.Error = "현재 LLM 사용량 연결 검증은 Prometheus 소스만 지원합니다"
				result.Remediation = "Prometheus를 선택하거나 Gateway 메트릭을 Prometheus에 노출하세요."
				result.Checks = append(result.Checks, Check{Name: "수집 소스", Success: false, Detail: result.Error})
				return result
			}
			pattern, _ := req.Config["pod_username_regex"].(string)
			re, regexErr := CompilePodUsernamePattern(pattern)
			if regexErr != nil {
				result.Error, result.Remediation = regexErr.Error(), "username named capture가 있는 안전한 RE2 정규식을 입력하세요."
				result.Checks = append(result.Checks, Check{Name: "Pod 사용자 매핑", Success: false, Detail: result.Error})
				return result
			}
			var mappingErr error
			llmLabelMappings, mappingErr = DecodeLLMLabelMappings(req.Config["label_mappings"])
			if mappingErr != nil {
				result.Error, result.Remediation = mappingErr.Error(), "지원 label을 사용하거나 PromQL에서 pod/path/status/model/hub/network로 별칭을 만드세요."
				result.Checks = append(result.Checks, Check{Name: "메타데이터 label 매핑", Success: false, Detail: result.Error})
				return result
			}
			sample, _ := req.Config["sample_pod"].(string)
			if sample != "" {
				username, matched := UsernameFromPod(re, sample)
				if !matched {
					result.Error, result.Remediation = "샘플 Pod에서 사용자를 추출할 수 없습니다", "정규식과 샘플 Pod 이름을 확인하세요."
					result.Checks = append(result.Checks, Check{Name: "Pod 사용자 매핑", Success: false, Detail: result.Error})
					return result
				}
				result.Checks = append(result.Checks, Check{Name: "Pod 사용자 매핑", Success: true, Detail: "샘플 Pod에서 사용자 " + username + " 추출"})
			}
		}
		queryURL, ok := join("api/v1/query")
		if !ok {
			return result
		}
		parsed, _ := url.Parse(queryURL)
		query := parsed.Query()
		query.Set("query", "vector(1)")
		if configured, ok := req.Config["calls_query"].(string); ok && strings.TrimSpace(configured) != "" {
			query.Set("query", configured)
		} else if promql, ok := req.Config["promql"].(map[string]any); ok {
			if calls, ok := promql["calls"].(string); ok && strings.TrimSpace(calls) != "" {
				query.Set("query", calls)
			}
		}
		parsed.RawQuery = query.Encode()
		var response map[string]any
		if !run("메트릭 쿼리 권한", http.MethodGet, parsed.String(), nil, &response, true) {
			return result
		}
		if status, _ := response["status"].(string); status != "success" {
			result.Error, result.Remediation = "Prometheus가 성공 상태를 반환하지 않았습니다", "PromQL과 원격 조회 권한을 확인하세요."
			result.Checks = append(result.Checks, Check{Name: "PromQL 결과", Success: false, Detail: result.Error})
			return result
		}
		if req.Type == "llm_usage" {
			sampleCount, fields, inspectErr := inspectLLMPrometheusResult(response, llmLabelMappings)
			if inspectErr != nil {
				result.Error, result.Remediation = inspectErr.Error(), "calls PromQL이 pod와 path label을 유지하는지, label_mappings가 실제 원격 label과 일치하는지 확인하세요."
				result.Checks = append(result.Checks, Check{Name: "LLM 메타데이터 샘플", Success: false, Detail: result.Error})
				return result
			}
			if sampleCount == 0 {
				result.Checks = append(result.Checks, Check{Name: "LLM 메타데이터 샘플", Success: true, Detail: "PromQL 쿼리 성공 / 현재 샘플 없음(실제 Pod·path 귀속은 미검증)"})
			} else {
				result.Checks = append(result.Checks, Check{Name: "LLM 메타데이터 샘플", Success: true, Detail: fmt.Sprintf("%d개 샘플에서 canonical label 확인: %s", sampleCount, strings.Join(fields, ", "))})
			}
		}
	case "kubernetes":
		versionURL, ok := join("version")
		if !ok {
			return result
		}
		var versionPayload map[string]any
		if !run("연결·버전", http.MethodGet, versionURL, nil, &versionPayload, true) {
			return result
		}
		result.Version = extractVersion(versionPayload)
		namespace, _ := req.Config["namespace"].(string)
		podPath := "api/v1/pods"
		if namespace != "" {
			podPath = "api/v1/namespaces/" + url.PathEscape(namespace) + "/pods"
		}
		podsURL, ok := join(podPath)
		if !ok {
			return result
		}
		podsURL += "?limit=1"
		var pods map[string]any
		if !run("Pod 조회 권한", http.MethodGet, podsURL, nil, &pods, true) {
			return result
		}
	case "ai":
		modelsURL, ok := join("models")
		if !ok {
			return result
		}
		var models map[string]any
		if !run("모델 조회 권한", http.MethodGet, modelsURL, nil, &models, true) {
			return result
		}
	case "oidc":
		discoveryURL, ok := join(".well-known/openid-configuration")
		if !ok {
			return result
		}
		var discovery map[string]any
		if !run("OIDC Discovery", http.MethodGet, discoveryURL, nil, &discovery, true) {
			return result
		}
		for _, field := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri"} {
			if value, _ := discovery[field].(string); value == "" {
				result.Error, result.Remediation = "Discovery 응답에 "+field+"가 없습니다", "Keycloak realm issuer URL을 확인하세요."
				result.Checks = append(result.Checks, Check{Name: "Discovery 필수 항목", Success: false, Detail: result.Error})
				return result
			}
		}
		clientID, _ := req.Config["client_id"].(string)
		if strings.TrimSpace(clientID) == "" {
			result.Error, result.Remediation = "Client ID가 필요합니다", "Keycloak에 등록한 OIDC Client ID를 입력하세요."
			result.Checks = append(result.Checks, Check{Name: "Client 설정", Success: false, Detail: result.Error})
			return result
		}
		result.Checks = append(result.Checks, Check{Name: "Client 설정", Success: true, Detail: "Discovery 형식 확인 완료(실제 로그인 시 code·token 검증 수행)"})
	case "webhook":
		webhookURL, ok := join("")
		if !ok {
			return result
		}
		if !run("Webhook 시험 전달", http.MethodPost, webhookURL, []byte(`{"event":"jupiq.integration.test"}`), nil, false) {
			return result
		}
	default:
		result.Error, result.Remediation = fmt.Sprintf("지원하지 않는 연동 유형입니다: %s", req.Type), "연동 유형을 다시 확인하세요."
		return result
	}
	result.Success = true
	return result
}

func integrationTestAuthSecret(kind, secret string) string {
	if kind == "oidc" {
		// Discovery is a public issuer metadata request. A client secret is
		// valid only at the token endpoint and must never be exposed here.
		return ""
	}
	return secret
}

func inspectLLMPrometheusResult(response map[string]any, mappings map[string]string) (int, []string, error) {
	data, ok := response["data"].(map[string]any)
	if !ok {
		return 0, nil, errors.New("Prometheus 응답에 data 객체가 없습니다")
	}
	results, ok := data["result"].([]any)
	if !ok {
		return 0, nil, errors.New("Prometheus 응답의 vector result 형식이 올바르지 않습니다")
	}
	present := map[string]bool{}
	for index, rawResult := range results {
		result, ok := rawResult.(map[string]any)
		if !ok {
			return 0, nil, fmt.Errorf("Prometheus 샘플 %d 형식이 올바르지 않습니다", index+1)
		}
		metric, ok := result["metric"].(map[string]any)
		if !ok {
			return 0, nil, fmt.Errorf("Prometheus 샘플 %d에 metric label이 없습니다", index+1)
		}
		canonical := map[string]string{}
		for label := range llmCanonicalLabels {
			if value, ok := metric[label].(string); ok && strings.TrimSpace(value) != "" {
				canonical[label] = value
				present[label] = true
			}
		}
		for label, remote := range mappings {
			if value, ok := metric[remote].(string); ok && strings.TrimSpace(value) != "" {
				canonical[label] = value
				present[label] = true
			}
		}
		for _, required := range []string{"pod", "path"} {
			if canonical[required] == "" {
				return 0, nil, fmt.Errorf("Prometheus 샘플 %d에 canonical %s label이 없습니다", index+1, required)
			}
		}
	}
	fields := []string{}
	for _, field := range []string{"pod", "path", "status", "model", "hub", "network"} {
		if present[field] {
			fields = append(fields, field)
		}
	}
	return len(results), fields, nil
}

func extractVersion(payload map[string]any) string {
	for _, key := range []string{"version", "gitVersion"} {
		if value, ok := payload[key].(string); ok {
			return value
		}
	}
	if data, ok := payload["data"].(map[string]any); ok {
		if value, ok := data["version"].(string); ok {
			return value
		}
	}
	return ""
}

func safeConnectionError(err error) string {
	message := err.Error()
	message = strings.ReplaceAll(message, "Bearer", "인증")
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

func remediation(status int, err error) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "토큰과 원격 서비스의 관리자/조회 권한을 확인하세요."
	case http.StatusNotFound:
		return "기본 URL과 필수 API 경로가 맞는지 확인하세요."
	default:
		if _, ok := err.(tls.RecordHeaderError); ok {
			return "HTTPS 설정과 인증서 체인을 확인하세요."
		}
		return "망 경로, DNS, 방화벽, TLS 인증서와 원격 서비스 상태를 확인하세요."
	}
}

func tlsDetail(_ http.Header, verify bool) string {
	if verify {
		return "TLS 인증서 검증 사용"
	}
	return "TLS 인증서 검증 비활성화(운영 환경에서는 권장하지 않음)"
}

func decodeConfig[T any](config map[string]any, target *T) error {
	b, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}
