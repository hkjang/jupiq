package integration

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var prometheusLabelNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,127}$`)

var llmCanonicalLabels = map[string]bool{
	"pod": true, "path": true, "status": true, "model": true,
	"hub": true, "network": true,
}

// Non-standard labels can still be projected to one of these names in PromQL.
// Keeping this list explicit prevents an administrator from accidentally
// mapping prompt, response, authorization, cookie, or token labels into a
// metadata column such as model.
var llmRemoteLabels = map[string]bool{
	"pod": true, "pod_name": true, "kubernetes_pod_name": true,
	"path": true, "route": true, "http_route": true,
	"status": true, "code": true, "status_code": true, "http_status_code": true,
	"model": true, "model_name": true, "served_model": true, "served_model_name": true,
	"hub": true, "hub_name": true, "network": true, "network_name": true,
}

func CompilePodUsernamePattern(pattern string) (*regexp.Regexp, error) {
	if len(pattern) == 0 || len(pattern) > 256 {
		return nil, errors.New("pod_username_regex는 1~256자여야 합니다")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, errors.New("pod_username_regex 형식이 올바르지 않습니다")
	}
	found := false
	for _, name := range re.SubexpNames() {
		if name == "username" {
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("pod_username_regex에는 (?P<username>...) 캡처가 필요합니다")
	}
	return re, nil
}

func UsernameFromPod(re *regexp.Regexp, pod string) (string, bool) {
	match := re.FindStringSubmatch(pod)
	if match == nil {
		return "", false
	}
	for i, name := range re.SubexpNames() {
		if name == "username" && i < len(match) {
			username := strings.TrimSpace(match[i])
			return username, username != ""
		}
	}
	return "", false
}

// ValidateLLMLabelMapping limits administrator-defined aliases to metadata
// fields that jupiq persists and rejects common secret/content label names.
// It is applied both when settings are saved and again by the collector.
func ValidateLLMLabelMapping(canonical, remote string) error {
	canonical = strings.TrimSpace(canonical)
	remote = strings.TrimSpace(remote)
	if !llmCanonicalLabels[canonical] {
		return fmt.Errorf("지원하지 않는 canonical LLM label입니다: %s", canonical)
	}
	if !prometheusLabelNamePattern.MatchString(remote) {
		return fmt.Errorf("Prometheus label 이름이 올바르지 않습니다: %s", remote)
	}
	if !llmRemoteLabels[remote] {
		return fmt.Errorf("허용되지 않은 원격 LLM label입니다: %s", remote)
	}
	return nil
}

func ValidateLLMLabelMappings(mappings map[string]string) error {
	for canonical, remote := range mappings {
		if err := ValidateLLMLabelMapping(canonical, remote); err != nil {
			return err
		}
	}
	return nil
}

func DecodeLLMLabelMappings(value any) (map[string]string, error) {
	if value == nil {
		return map[string]string{}, nil
	}
	mappings := map[string]string{}
	switch typed := value.(type) {
	case map[string]string:
		for canonical, remote := range typed {
			mappings[canonical] = remote
		}
	case map[string]any:
		for canonical, rawRemote := range typed {
			remote, ok := rawRemote.(string)
			if !ok {
				return nil, errors.New("label_mappings의 값은 문자열이어야 합니다")
			}
			mappings[canonical] = remote
		}
	default:
		return nil, errors.New("label_mappings는 문자열 값의 JSON 객체여야 합니다")
	}
	if err := ValidateLLMLabelMappings(mappings); err != nil {
		return nil, err
	}
	return mappings, nil
}
