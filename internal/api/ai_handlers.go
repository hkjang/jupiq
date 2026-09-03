package api

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
)

type aiConfig struct {
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	VerifyTLS bool   `json:"verify_tls"`
}

type aiChatInput struct {
	Model       string          `json:"model"`
	Messages    json.RawMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature"`
}

func (s *Server) registerAI(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/ai/chat", s.require("ai:chat", s.aiChat))
}

func (s *Server) aiChat(w http.ResponseWriter, r *http.Request) {
	var input aiChatInput
	if err := decodeJSON(r, &input); err != nil {
		apiError(w, r, 400, "invalid_json", err.Error())
		return
	}
	var cfg aiConfig
	apiKey, _, err := s.Store.GetSettingAndSecret(r.Context(), "ai", "ai.api_key", &cfg)
	if err != nil || !cfg.Enabled || cfg.BaseURL == "" || strings.TrimSpace(cfg.Model) == "" {
		apiError(w, r, 503, "ai_disabled", "AI 공급자가 설정되지 않았습니다")
		return
	}
	if code, err := validateAIChatInput(&input, cfg); err != nil {
		apiError(w, r, http.StatusBadRequest, code, err.Error())
		return
	}
	started := time.Now()
	resp, err := integration.StreamAIChat(r.Context(), cfg.BaseURL, apiKey, cfg.VerifyTLS, integration.AIChatRequest{Model: input.Model, Messages: input.Messages, MaxTokens: input.MaxTokens, Temperature: input.Temperature, Stream: true})
	if err != nil {
		_ = s.Store.RecordAIUsage(r.Context(), principal(r).User.ID, cfg.Provider, input.Model, "error", requestID(r), time.Since(started))
		apiError(w, r, 502, "ai_upstream_error", "AI 공급자 호출에 실패했습니다: "+err.Error())
		return
	}
	defer resp.Body.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		apiError(w, r, 501, "streaming_unavailable", "스트리밍을 지원하지 않는 연결입니다")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	written, copyErr := copyFlushedStream(w, flusher, resp.Body, 64<<20)
	status := "success"
	if copyErr != nil || written >= 64<<20 {
		status = "truncated"
	}
	auditCtx, cancelAudit := contextWithDetachedTimeout(r, 5*time.Second)
	defer cancelAudit()
	_ = s.Store.RecordAIUsage(auditCtx, principal(r).User.ID, cfg.Provider, input.Model, status, requestID(r), time.Since(started))
	_ = s.Store.RecordAudit(auditCtx, requestAudit(r, "ai.chat", "ai", input.Model, status, "", nil, map[string]any{"max_tokens": input.MaxTokens, "stream": true}))
}

func copyFlushedStream(dst io.Writer, flusher http.Flusher, src io.Reader, limit int64) (int64, error) {
	limited := &io.LimitedReader{R: src, N: limit}
	buffer := make([]byte, 32*1024)
	var written int64
	for {
		read, readErr := limited.Read(buffer)
		if read > 0 {
			count, writeErr := dst.Write(buffer[:read])
			written += int64(count)
			flusher.Flush()
			if writeErr != nil {
				return written, writeErr
			}
			if count != read {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return written, nil
			}
			return written, readErr
		}
		if limited.N == 0 {
			return written, fmt.Errorf("AI SSE 응답이 최대 중계 크기를 초과했습니다")
		}
	}
}

func validateAIChatInput(input *aiChatInput, cfg aiConfig) (string, error) {
	configuredModel := strings.TrimSpace(cfg.Model)
	if strings.TrimSpace(input.Model) == "" {
		input.Model = configuredModel
	} else if input.Model != configuredModel {
		return "model_not_allowed", fmt.Errorf("관리자가 설정한 모델만 호출할 수 있습니다")
	}
	var messages []json.RawMessage
	if len(input.Messages) == 0 || json.Unmarshal(input.Messages, &messages) != nil || len(messages) == 0 {
		return "invalid_messages", fmt.Errorf("messages는 하나 이상의 메시지를 포함한 JSON 배열이어야 합니다")
	}
	if input.Temperature != nil && (math.IsNaN(*input.Temperature) || math.IsInf(*input.Temperature, 0) || *input.Temperature < 0 || *input.Temperature > 2) {
		return "invalid_temperature", fmt.Errorf("temperature는 0~2 범위의 유한한 숫자여야 합니다")
	}
	if input.MaxTokens == 0 {
		input.MaxTokens = cfg.MaxTokens
	}
	if cfg.MaxTokens > 0 && input.MaxTokens > cfg.MaxTokens {
		return "max_tokens_exceeded", fmt.Errorf("관리자 설정 최대값 %d를 초과할 수 없습니다", cfg.MaxTokens)
	}
	if input.MaxTokens < 1 || input.MaxTokens > integration.MaxAITokens {
		return "invalid_max_tokens", fmt.Errorf("max_tokens는 1~%d 범위여야 합니다", integration.MaxAITokens)
	}
	return "", nil
}
