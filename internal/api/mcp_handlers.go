package api

import (
	"encoding/json"
	"net/http"
	"time"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) registerMCP(mux router) {
	mux.HandleFunc("POST /mcp", s.require("mcp:use", s.mcp))
	mux.HandleFunc("POST /api/v1/mcp", s.require("mcp:use", s.mcp))
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	var request rpcRequest
	if err := decodeJSON(r, &request); err != nil {
		s.writeRPC(w, r, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "Parse error"}})
		return
	}
	if request.JSONRPC != "2.0" || request.Method == "" {
		s.writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: request.ID, Error: &rpcError{Code: -32600, Message: "Invalid Request"}})
		return
	}
	if request.Method == "notifications/initialized" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "jupiq", "version": s.versionInfo().Version}, "instructions": "jupiq 운영 데이터를 최소 권한으로 조회하는 읽기 전용 MCP입니다."}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": mcpTools()}
	case "tools/call":
		result, err := s.mcpToolCall(r, request.Params)
		if err != nil {
			response.Result = map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}
		} else {
			response.Result = result
		}
	default:
		response.Error = &rpcError{Code: -32601, Message: "Method not found"}
	}
	s.writeRPC(w, r, response)
}

func (s *Server) writeRPC(w http.ResponseWriter, r *http.Request, response rpcResponse) {
	if len(response.ID) == 0 && response.Error == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	payload, _ := json.Marshal(response)
	if acceptsSSE(r) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(payload)
		_, _ = w.Write([]byte("\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(payload)
}

func acceptsSSE(r *http.Request) bool {
	return containsHeader(r.Header.Values("Accept"), "text/event-stream")
}
func containsHeader(values []string, want string) bool {
	for _, value := range values {
		for _, part := range splitComma(value) {
			if part == want {
				return true
			}
		}
	}
	return false
}
func splitComma(value string) []string {
	result := []string{}
	start := 0
	for i, c := range value {
		if c == ',' {
			result = append(result, trim(value[start:i]))
			start = i + 1
		}
	}
	result = append(result, trim(value[start:]))
	return result
}
func trim(v string) string {
	for len(v) > 0 && (v[0] == ' ' || v[0] == '\t') {
		v = v[1:]
	}
	for len(v) > 0 && (v[len(v)-1] == ' ' || v[len(v)-1] == '\t') {
		v = v[:len(v)-1]
	}
	return v
}

func mcpTools() []map[string]any {
	return []map[string]any{
		{"name": "jupiq.dashboard", "description": "실시간 사용자·서버·자원 요약 조회", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "jupiq.list_hubs", "description": "등록된 JupyterHub 상태 목록 조회", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "jupiq.list_servers", "description": "사용자 Notebook 서버 목록 조회", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"hub_id": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"}, "search": map[string]any{"type": "string"}}}},
		{"name": "jupiq.usage", "description": "기간별 자원 이용 통계 조회", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"from": map[string]any{"type": "string", "format": "date-time"}, "to": map[string]any{"type": "string", "format": "date-time"}, "granularity": map[string]any{"type": "string", "enum": []string{"hour", "day", "week", "month"}}, "group_by": map[string]any{"type": "string", "enum": []string{"user", "hub", "network", "department", "project"}}}}},
	}
}

func (s *Server) mcpToolCall(r *http.Request, raw json.RawMessage) (map[string]any, error) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	var result any
	var err error
	required := map[string]string{"jupiq.dashboard": "dashboard:read", "jupiq.list_hubs": "hubs:read", "jupiq.list_servers": "servers:read", "jupiq.usage": "usage:read"}[params.Name]
	if required != "" && !principal(r).Allows(required) {
		return nil, &toolError{"이 MCP 도구를 실행할 세부 권한이 없습니다"}
	}
	switch params.Name {
	case "jupiq.dashboard":
		result, err = s.Store.LiveSnapshot(r.Context())
	case "jupiq.list_hubs":
		result, err = s.Store.ListHubs(r.Context())
	case "jupiq.list_servers":
		hubID := int64(numberFromAny(params.Arguments["hub_id"]))
		status, _ := params.Arguments["status"].(string)
		search, _ := params.Arguments["search"].(string)
		var page any
		result, page, err = s.Store.ListServers(r.Context(), 1, 200, hubID, status, search)
		_ = page
	case "jupiq.usage":
		now := time.Now().UTC()
		from := now.Add(-30 * 24 * time.Hour)
		to := now
		if value, ok := params.Arguments["from"].(string); ok {
			parsed, e := time.Parse(time.RFC3339, value)
			if e != nil {
				return nil, &toolError{"from은 RFC3339 날짜·시간이어야 합니다"}
			}
			from = parsed
		}
		if value, ok := params.Arguments["to"].(string); ok {
			parsed, e := time.Parse(time.RFC3339, value)
			if e != nil {
				return nil, &toolError{"to는 RFC3339 날짜·시간이어야 합니다"}
			}
			to = parsed
		}
		if !from.Before(to) || to.Sub(from) > 366*24*time.Hour {
			return nil, &toolError{"조회 시작은 종료보다 앞서야 하며 기간은 최대 366일입니다"}
		}
		granularity, _ := params.Arguments["granularity"].(string)
		groupBy, _ := params.Arguments["group_by"].(string)
		result, err = s.Store.Usage(r.Context(), from, to, granularity, groupBy)
	default:
		return nil, &toolError{"알 수 없는 MCP 도구입니다"}
	}
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(result)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(payload)}}, "structuredContent": result}, nil
}

type toolError struct{ message string }

func (e *toolError) Error() string { return e.message }
