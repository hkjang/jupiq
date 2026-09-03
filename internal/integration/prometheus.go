package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type MetricPoint struct {
	Labels    map[string]string `json:"labels"`
	Value     float64           `json:"value"`
	Timestamp time.Time         `json:"timestamp"`
}

type PrometheusClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewPrometheus(baseURL, token string, verifyTLS bool) (*PrometheusClient, error) {
	if _, err := ValidateEndpoint(baseURL); err != nil {
		return nil, err
	}
	return &PrometheusClient{baseURL: baseURL, token: token, http: newHTTPClient(HTTPOptions{VerifyTLS: verifyTLS, Timeout: 12 * time.Second})}, nil
}

func (c *PrometheusClient) Query(ctx context.Context, query string, at time.Time) ([]MetricPoint, error) {
	endpoint, err := joinURL(c.baseURL, "api/v1/query")
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint)
	values := u.Query()
	values.Set("query", query)
	if !at.IsZero() {
		values.Set("time", at.UTC().Format(time.RFC3339Nano))
	}
	u.RawQuery = values.Encode()
	var response struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if _, _, err := doJSON(ctx, c.http, http.MethodGet, u.String(), c.token, nil, &response, true); err != nil {
		return nil, err
	}
	if response.Status != "success" {
		return nil, fmt.Errorf("Prometheus query failed: %s", response.Error)
	}
	points := make([]MetricPoint, 0, len(response.Data.Result))
	for _, item := range response.Data.Result {
		if len(item.Value) != 2 {
			continue
		}
		var timestamp float64
		var text string
		if json.Unmarshal(item.Value[0], &timestamp) != nil || json.Unmarshal(item.Value[1], &text) != nil {
			continue
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil || !validPrometheusSample(timestamp, value, time.Now()) {
			continue
		}
		points = append(points, MetricPoint{Labels: item.Metric, Value: value, Timestamp: time.UnixMilli(int64(timestamp * 1000)).UTC()})
	}
	return points, nil
}

func validPrometheusSample(timestamp, value float64, now time.Time) bool {
	if math.IsNaN(timestamp) || math.IsInf(timestamp, 0) || math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	// jupiq's configured metrics are resource usage, counters, latency, bytes,
	// tokens and cost; negative values are never meaningful for these series.
	if value < 0 || timestamp < 946684800 || timestamp > float64(now.Add(5*time.Minute).Unix()) {
		return false
	}
	return true
}
