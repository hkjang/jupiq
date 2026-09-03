package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

const (
	kubernetesPodPageLimit          = 500
	maxKubernetesPodPages           = 10
	maxKubernetesPodCount           = kubernetesPodPageLimit * maxKubernetesPodPages
	maxKubernetesPodAggregateBytes  = 32 << 20
	maxKubernetesContinueTokenBytes = 4096
)

type KubernetesClient struct {
	baseURL string
	token   string
	http    *http.Client
}

type Pod struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Node      string            `json:"node"`
	Phase     string            `json:"phase"`
	Labels    map[string]string `json:"labels"`
}

func NewKubernetes(baseURL, token string, verifyTLS bool) (*KubernetesClient, error) {
	if _, err := ValidateEndpoint(baseURL); err != nil {
		return nil, err
	}
	return &KubernetesClient{baseURL: baseURL, token: token, http: newHTTPClient(HTTPOptions{VerifyTLS: verifyTLS, Timeout: 12 * time.Second})}, nil
}

func (c *KubernetesClient) Pods(ctx context.Context, namespace, labelSelector string) ([]Pod, error) {
	path := "api/v1/pods"
	if namespace != "" {
		path = "api/v1/namespaces/" + url.PathEscape(namespace) + "/pods"
	}
	endpoint, err := joinURL(c.baseURL, path)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint)
	query := u.Query()
	query.Set("limit", "500")
	if labelSelector != "" {
		query.Set("labelSelector", labelSelector)
	}
	u.RawQuery = query.Encode()
	pods := make([]Pod, 0, kubernetesPodPageLimit)
	seenContinueTokens := make(map[string]struct{})
	totalBytes := 0
	for page := 0; page < maxKubernetesPodPages; page++ {
		var response struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []json.RawMessage `json:"items"`
		}
		if _, _, err := doJSON(ctx, c.http, http.MethodGet, u.String(), c.token, nil, &response, true); err != nil {
			return nil, err
		}
		if len(response.Items) > kubernetesPodPageLimit {
			return nil, errors.New("Kubernetes Pods API가 요청한 페이지 제한을 초과했습니다")
		}
		for _, raw := range response.Items {
			totalBytes += len(raw)
			if totalBytes > maxKubernetesPodAggregateBytes {
				return nil, errors.New("Kubernetes Pod 목록의 누적 크기가 32 MiB 제한을 초과했습니다")
			}
			pod, ok := decodeKubernetesPod(raw)
			if ok {
				pods = append(pods, pod)
				if len(pods) > maxKubernetesPodCount {
					return nil, errors.New("Kubernetes Pod 목록이 최대 항목 수를 초과했습니다")
				}
			}
		}
		continuation := response.Metadata.Continue
		if continuation == "" {
			return pods, nil
		}
		if len(continuation) > maxKubernetesContinueTokenBytes {
			return nil, errors.New("Kubernetes continue token이 허용 길이를 초과했습니다")
		}
		if _, exists := seenContinueTokens[continuation]; exists {
			return nil, errors.New("Kubernetes Pods API가 반복 continue token을 반환했습니다")
		}
		seenContinueTokens[continuation] = struct{}{}
		if page+1 >= maxKubernetesPodPages {
			return nil, errors.New("Kubernetes Pod 목록 조회가 최대 페이지 수를 초과했습니다")
		}
		query.Set("continue", continuation)
		u.RawQuery = query.Encode()
	}
	return nil, errors.New("Kubernetes Pod 목록 조회가 종료되지 않았습니다")
}

func decodeKubernetesPod(raw json.RawMessage) (Pod, bool) {
	var item struct {
		Metadata struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			NodeName string `json:"nodeName"`
		} `json:"spec"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return Pod{}, false
	}
	if item.Metadata.Name == "" {
		return Pod{}, false
	}
	return Pod{Name: item.Metadata.Name, Namespace: item.Metadata.Namespace, Node: item.Spec.NodeName, Phase: item.Status.Phase, Labels: item.Metadata.Labels}, true
}
