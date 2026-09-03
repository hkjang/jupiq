package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"strings"
	"time"
)

const MaxAITokens = 262144

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type AIChatRequest struct {
	Model       string          `json:"model"`
	Messages    json.RawMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature,omitempty"`
	Stream      bool            `json:"stream"`
}

func StreamAIChat(ctx context.Context, baseURL, apiKey string, verifyTLS bool, request AIChatRequest) (*http.Response, error) {
	if request.MaxTokens < 1 || request.MaxTokens > MaxAITokens {
		return nil, fmt.Errorf("max_tokens는 1~%d 범위여야 합니다", MaxAITokens)
	}
	var messages []json.RawMessage
	if len(request.Messages) == 0 || json.Unmarshal(request.Messages, &messages) != nil || len(messages) == 0 {
		return nil, errors.New("messages가 올바른 JSON 배열이어야 합니다")
	}
	if request.Temperature != nil && (math.IsNaN(*request.Temperature) || math.IsInf(*request.Temperature, 0) || *request.Temperature < 0 || *request.Temperature > 2) {
		return nil, errors.New("temperature는 0~2 범위여야 합니다")
	}
	endpoint, err := joinURL(baseURL, "chat/completions")
	if err != nil {
		return nil, err
	}
	client := newHTTPClient(HTTPOptions{VerifyTLS: verifyTLS, Timeout: 20 * time.Second})
	client.Timeout = 0
	return streamAIChatWithClient(ctx, client, endpoint, apiKey, request)
}

func streamAIChatWithClient(ctx context.Context, client httpDoer, endpoint, apiKey string, request AIChatRequest) (*http.Response, error) {
	request.Stream = true
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "jupiq-control-plane")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A streaming client intentionally has no whole-request timeout. Do not
		// wait for an untrusted error body here: it could be endless or slow.
		_ = resp.Body.Close()
		return nil, fmt.Errorf("AI API가 HTTP %d를 반환했습니다", resp.StatusCode)
	}
	mediaType, _, contentTypeErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if contentTypeErr != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		_ = resp.Body.Close()
		return nil, errors.New("AI API가 text/event-stream 응답을 반환하지 않았습니다")
	}
	return resp, nil
}
