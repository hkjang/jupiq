package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func (s *Server) registerAI(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/ai/chat", s.require("ai:chat", s.aiChat))
}

func (s *Server) aiChat(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Model       string          `json:"model"`
		Messages    json.RawMessage `json:"messages"`
		MaxTokens   int             `json:"max_tokens"`
		Temperature *float64        `json:"temperature"`
	}
	if err := decodeJSON(r, &input); err != nil {
		apiError(w, r, 400, "invalid_json", err.Error())
		return
	}
	var cfg aiConfig
	if err := s.Store.GetSetting(r.Context(), "ai", &cfg); err != nil || !cfg.Enabled || cfg.BaseURL == "" {
		apiError(w, r, 503, "ai_disabled", "AI 공급자가 설정되지 않았습니다")
		return
	}
	if input.Model == "" {
		input.Model = cfg.Model
	}
	if input.MaxTokens == 0 {
		input.MaxTokens = cfg.MaxTokens
	}
	if cfg.MaxTokens > 0 && input.MaxTokens > cfg.MaxTokens {
		apiError(w, r, 400, "max_tokens_exceeded", fmt.Sprintf("관리자 설정 최대값 %d를 초과할 수 없습니다", cfg.MaxTokens))
		return
	}
	if input.MaxTokens < 1 || input.MaxTokens > integration.MaxAITokens {
		apiError(w, r, 400, "invalid_max_tokens", fmt.Sprintf("max_tokens는 1~%d 범위여야 합니다", integration.MaxAITokens))
		return
	}
	apiKey, _ := s.Store.GetSecret(r.Context(), "ai.api_key")
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
	written, copyErr := io.Copy(w, io.LimitReader(resp.Body, 64<<20))
	flusher.Flush()
	status := "success"
	if copyErr != nil || written >= 64<<20 {
		status = "truncated"
	}
	_ = s.Store.RecordAIUsage(contextWithoutCancel(r), principal(r).User.ID, cfg.Provider, input.Model, status, requestID(r), time.Since(started))
	_ = s.Store.RecordAudit(contextWithoutCancel(r), requestAudit(r, "ai.chat", "ai", input.Model, status, "", nil, map[string]any{"max_tokens": input.MaxTokens, "stream": true}))
}
