package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

type unreadBody struct{ closeCalls int }

func (body *unreadBody) Read([]byte) (int, error) { panic("error body must not be read") }
func (body *unreadBody) Close() error {
	body.closeCalls++
	return nil
}

func TestStreamAIChatNonSuccessDoesNotWaitForBody(t *testing.T) {
	body := &unreadBody{}
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: body, Header: make(http.Header)}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	response, err := streamAIChatWithClient(ctx, client, "https://ai.example.invalid/v1/chat/completions", "", AIChatRequest{
		Model: "test-model", Messages: json.RawMessage(`[{"role":"user","content":"hello"}]`), MaxTokens: 32,
	})
	if response != nil {
		t.Fatal("non-success response must not be returned")
	}
	if err == nil {
		t.Fatal("expected upstream HTTP error")
	}
	if strings.Contains(err.Error(), "private-upstream-error-marker") {
		t.Fatal("upstream error body must not be exposed")
	}
	if body.closeCalls != 1 {
		t.Fatalf("error body close calls=%d want 1", body.closeCalls)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("waited for streaming error body: %s", elapsed)
	}
}

func TestStreamAIChatLeavesSuccessfulSSEBodyToCaller(t *testing.T) {
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("data: ok\n\n")), Header: http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}}}, nil
	})
	response, err := streamAIChatWithClient(context.Background(), client, "https://ai.example.invalid/v1/chat/completions", "", AIChatRequest{
		Model: "test-model", Messages: json.RawMessage(`[{"role":"user","content":"hello"}]`), MaxTokens: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "data: ok\n\n" {
		t.Fatalf("successful SSE body was not preserved: body=%q err=%v", body, err)
	}
}

func TestStreamAIChatRejectsNonSSESuccess(t *testing.T) {
	body := &unreadBody{}
	client := doerFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})
	response, err := streamAIChatWithClient(context.Background(), client, "https://ai.example.invalid/v1/chat/completions", "", AIChatRequest{
		Model: "test-model", Messages: json.RawMessage(`[{"role":"user","content":"hello"}]`), MaxTokens: 32,
	})
	if response != nil || err == nil || body.closeCalls != 1 {
		t.Fatalf("non-SSE response was accepted: response=%v err=%v close_calls=%d", response, err, body.closeCalls)
	}
}
