package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
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
	Raw       json.RawMessage   `json:"raw,omitempty"`
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
	var response struct {
		Items []json.RawMessage `json:"items"`
	}
	if _, _, err := doJSON(ctx, c.http, http.MethodGet, u.String(), c.token, nil, &response, true); err != nil {
		return nil, err
	}
	pods := make([]Pod, 0, len(response.Items))
	for _, raw := range response.Items {
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
			continue
		}
		pods = append(pods, Pod{Name: item.Metadata.Name, Namespace: item.Metadata.Namespace, Node: item.Spec.NodeName, Phase: item.Status.Phase, Labels: item.Metadata.Labels, Raw: raw})
	}
	return pods, nil
}
