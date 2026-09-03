package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/store"
)

var resourceKinds = map[string]string{
	"policies": "policy", "profiles": "profile", "images": "image", "projects": "project",
	"approvals": "approval", "incidents": "incident", "notifications": "notification", "costs": "cost",
}

func (s *Server) registerResources(mux *http.ServeMux) {
	for path, kind := range resourceKinds {
		path, kind := path, kind
		mux.HandleFunc("GET /api/v1/"+path, s.require(resourcePermission(kind, "read"), func(w http.ResponseWriter, r *http.Request) { s.resourceList(w, r, kind) }))
		mux.HandleFunc("POST /api/v1/"+path, s.require(resourcePermission(kind, "write"), func(w http.ResponseWriter, r *http.Request) { s.resourceCreate(w, r, kind) }))
		mux.HandleFunc("GET /api/v1/"+path+"/{id}", s.require(resourcePermission(kind, "read"), func(w http.ResponseWriter, r *http.Request) { s.resourceGet(w, r, kind) }))
		mux.HandleFunc("PUT /api/v1/"+path+"/{id}", s.require(resourcePermission(kind, "write"), func(w http.ResponseWriter, r *http.Request) { s.resourceUpdate(w, r, kind) }))
		mux.HandleFunc("DELETE /api/v1/"+path+"/{id}", s.require(resourcePermission(kind, "write"), func(w http.ResponseWriter, r *http.Request) { s.resourceDelete(w, r, kind) }))
	}
	mux.HandleFunc("POST /api/v1/approvals/{id}/approve", s.require("approval:approve", s.approvalApprove))
	mux.HandleFunc("POST /api/v1/approvals/{id}/review", s.require("approval:review", s.approvalReview))
	mux.HandleFunc("POST /api/v1/approvals/{id}/reject", s.require("approval:approve", s.approvalReject))
}

func resourcePermission(kind, action string) string {
	if kind == "profile" {
		return "profiles:" + action
	}
	return kind + ":" + action
}

func (s *Server) resourceList(w http.ResponseWriter, r *http.Request, kind string) {
	var ownerID *int64
	if value := r.URL.Query().Get("owner_user_id"); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			ownerID = &parsed
		}
	}
	items, page, err := s.Store.ListResources(r.Context(), kind, queryInt(r, "page", 1), queryInt(r, "page_size", 20), r.URL.Query().Get("status"), r.URL.Query().Get("search"), ownerID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	list(w, flattenResources(items), page)
}
func (s *Server) resourceGet(w http.ResponseWriter, r *http.Request, kind string) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "ID가 올바르지 않습니다")
		return
	}
	item, err := s.Store.GetResource(r.Context(), kind, id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, flattenResource(item))
}
func (s *Server) resourceCreate(w http.ResponseWriter, r *http.Request, kind string) {
	if kind == "approval" {
		apiError(w, r, http.StatusMethodNotAllowed, "workflow_managed", "승인 요청은 대상 작업 API에서만 생성할 수 있습니다")
		return
	}
	input, err := decodeResourceWrite(r)
	if err != nil {
		apiError(w, r, 400, "invalid_resource", err.Error())
		return
	}
	p := principal(r)
	item, err := s.Store.CreateResource(r.Context(), kind, input, p.User.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, kind+".create", kind, strconv.FormatInt(item.ID, 10), "success", "", nil, flattenResource(item)))
	data(w, http.StatusCreated, flattenResource(item))
}
func (s *Server) resourceUpdate(w http.ResponseWriter, r *http.Request, kind string) {
	if kind == "approval" {
		apiError(w, r, http.StatusMethodNotAllowed, "workflow_managed", "승인 상태는 검토·승인·반려 API로만 변경할 수 있습니다")
		return
	}
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "ID가 올바르지 않습니다")
		return
	}
	before, err := s.Store.GetResource(r.Context(), kind, id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	input, err := decodeResourceWrite(r)
	if err != nil {
		apiError(w, r, 400, "invalid_resource", err.Error())
		return
	}
	item, err := s.Store.UpdateResource(r.Context(), kind, id, input, principal(r).User.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, kind+".update", kind, strconv.FormatInt(id, 10), "success", "", flattenResource(before), flattenResource(item)))
	data(w, http.StatusOK, flattenResource(item))
}
func (s *Server) resourceDelete(w http.ResponseWriter, r *http.Request, kind string) {
	if kind == "approval" {
		apiError(w, r, http.StatusMethodNotAllowed, "workflow_managed", "감사 추적을 위해 승인 요청을 직접 삭제할 수 없습니다")
		return
	}
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "ID가 올바르지 않습니다")
		return
	}
	before, err := s.Store.GetResource(r.Context(), kind, id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if err := s.Store.DeleteResource(r.Context(), kind, id); err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, kind+".delete", kind, strconv.FormatInt(id, 10), "success", "", flattenResource(before), nil))
	w.WriteHeader(http.StatusNoContent)
}

func decodeResourceWrite(r *http.Request) (store.ResourceWrite, error) {
	var object map[string]any
	if err := decodeJSON(r, &object); err != nil {
		return store.ResourceWrite{}, err
	}
	name, _ := object["name"].(string)
	if strings.TrimSpace(name) == "" {
		return store.ResourceWrite{}, fmt.Errorf("name이 필요합니다")
	}
	status, _ := object["status"].(string)
	var ownerID *int64
	switch v := object["owner_user_id"].(type) {
	case float64:
		id := int64(v)
		ownerID = &id
	case json.Number:
		id, _ := v.Int64()
		ownerID = &id
	}
	payload := map[string]any{}
	if nested, ok := object["data"].(map[string]any); ok {
		for k, v := range nested {
			payload[k] = v
		}
	}
	reserved := map[string]bool{"id": true, "kind": true, "name": true, "status": true, "owner_user_id": true, "data": true, "created_by": true, "updated_by": true, "created_at": true, "updated_at": true}
	for k, v := range object {
		if !reserved[k] {
			payload[k] = v
		}
	}
	raw, err := json.Marshal(payload)
	return store.ResourceWrite{Name: name, Status: status, OwnerUserID: ownerID, Data: raw}, err
}
func flattenResource(item store.Resource) map[string]any {
	result := map[string]any{}
	_ = json.Unmarshal(item.Data, &result)
	result["id"], result["kind"], result["name"], result["status"] = item.ID, item.Kind, item.Name, item.Status
	result["owner_user_id"], result["data"], result["created_by"], result["updated_by"], result["created_at"], result["updated_at"] = item.OwnerUserID, item.Data, item.CreatedBy, item.UpdatedBy, item.CreatedAt, item.UpdatedAt
	return result
}
func flattenResources(items []store.Resource) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, flattenResource(item))
	}
	return result
}

type approvalActionInput struct {
	Reason string `json:"reason"`
}

func decodeApprovalAction(r *http.Request) (approvalActionInput, error) {
	var input approvalActionInput
	if r.ContentLength == 0 {
		return input, nil
	}
	err := decodeJSON(r, &input)
	input.Reason = strings.TrimSpace(input.Reason)
	return input, err
}

func approvalWorkflow(ctx context.Context, database *store.Store, details map[string]any) (store.WorkflowConfig, error) {
	if snapshot, ok := details["workflow"]; ok {
		raw, err := json.Marshal(snapshot)
		if err == nil {
			var cfg store.WorkflowConfig
			if json.Unmarshal(raw, &cfg) == nil {
				return cfg, nil
			}
		}
	}
	return database.GetWorkflowConfig(ctx)
}

func validateApprovalReason(required bool, reason string) error {
	if required && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("사유를 입력해야 합니다")
	}
	return nil
}

func (s *Server) approvalReview(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_id", "승인 ID가 올바르지 않습니다")
		return
	}
	approval, err := s.Store.GetResource(r.Context(), "approval", id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if approval.Status != "pending_review" {
		apiError(w, r, http.StatusConflict, "review_closed", "팀장 검토 대기 상태가 아닙니다")
		return
	}
	var details map[string]any
	_ = json.Unmarshal(approval.Data, &details)
	if details == nil {
		details = map[string]any{}
	}
	cfg, err := approvalWorkflow(r.Context(), s.Store, details)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !cfg.ManagerReviewEnabled {
		apiError(w, r, http.StatusConflict, "review_disabled", "이 요청에는 팀장 검토 단계가 없습니다")
		return
	}
	input, err := decodeApprovalAction(r)
	if err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := validateApprovalReason(cfg.RequireReason, input.Reason); err != nil {
		apiError(w, r, http.StatusBadRequest, "reason_required", err.Error())
		return
	}
	p := principal(r)
	if code, message := approvalActorConflict(details, p.User.ID, p.User.Username, "review"); code != "" {
		apiError(w, r, http.StatusConflict, code, message)
		return
	}
	details["review"] = map[string]any{"status": "reviewed", "reviewed_at": time.Now().UTC(), "reviewed_by_user_id": p.User.ID, "reviewed_by": p.User.Username, "reason": input.Reason}
	raw, _ := json.Marshal(details)
	saved, transitioned, err := s.Store.TransitionResource(r.Context(), "approval", id, "pending_review", store.ResourceWrite{Name: approval.Name, Status: "pending", OwnerUserID: approval.OwnerUserID, Data: raw}, p.User.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !transitioned {
		apiError(w, r, http.StatusConflict, "review_closed", "다른 사용자가 이미 검토했습니다")
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "approval.review", "approval", strconv.FormatInt(id, 10), "success", input.Reason, flattenResource(approval), flattenResource(saved)))
	response := flattenResource(saved)
	response["next_action"] = "approve_or_reject"
	data(w, http.StatusOK, response)
}

func (s *Server) approvalApprove(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "승인 ID가 올바르지 않습니다")
		return
	}
	approval, err := s.Store.GetResource(r.Context(), "approval", id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	var details map[string]any
	_ = json.Unmarshal(approval.Data, &details)
	if details == nil {
		details = map[string]any{}
	}
	if approval.Status == "pending_review" {
		apiError(w, r, http.StatusConflict, "review_required", "팀장 검토가 완료되어야 승인할 수 있습니다")
		return
	}
	if approval.Status != "pending" {
		apiError(w, r, http.StatusConflict, "approval_closed", "이미 처리 중이거나 완료된 승인 요청입니다")
		return
	}
	cfg, err := approvalWorkflow(r.Context(), s.Store, details)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	input, err := decodeApprovalAction(r)
	if err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := validateApprovalReason(cfg.RequireReason, input.Reason); err != nil {
		apiError(w, r, http.StatusBadRequest, "reason_required", err.Error())
		return
	}
	approverPrincipal := principal(r)
	approver := approverPrincipal.User.Username
	if code, message := approvalActorConflict(details, approverPrincipal.User.ID, approver, "approve"); code != "" {
		apiError(w, r, http.StatusConflict, code, message)
		return
	}
	details["approval_reason"] = input.Reason
	details["execution"] = map[string]any{"status": "executing", "started_at": time.Now().UTC(), "idempotency_key": requestID(r), "approved_by_user_id": approverPrincipal.User.ID, "approved_by": approver}
	executingData, _ := json.Marshal(details)
	executing, claimed, err := s.Store.TransitionResource(r.Context(), "approval", id, "pending", store.ResourceWrite{Name: approval.Name, Status: "executing", OwnerUserID: approval.OwnerUserID, Data: executingData}, principal(r).User.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !claimed {
		apiError(w, r, 409, "approval_closed", "이미 처리 중이거나 완료된 승인 요청입니다")
		return
	}
	if details["resource_type"] == "server" {
		serverID := int64(numberFromAny(details["server_id"]))
		action, _ := details["action"].(string)
		if err := s.executeServerAction(r, serverID, action); err != nil {
			details["execution"] = map[string]any{"status": "failed", "finished_at": time.Now().UTC(), "idempotency_key": requestID(r), "approved_by_user_id": approverPrincipal.User.ID, "approved_by": approver, "error": err.Error()}
			failedData, _ := json.Marshal(details)
			failed, _, transitionErr := s.Store.TransitionResource(r.Context(), "approval", id, "executing", store.ResourceWrite{Name: approval.Name, Status: "failed", OwnerUserID: approval.OwnerUserID, Data: failedData}, principal(r).User.ID)
			if transitionErr != nil {
				handleStoreError(w, r, transitionErr)
				return
			}
			_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "approval.approve", "approval", strconv.FormatInt(id, 10), "failure", err.Error(), flattenResource(executing), flattenResource(failed)))
			apiError(w, r, 502, "remote_action_failed", err.Error())
			return
		}
	}
	details["execution"] = map[string]any{"status": "approved", "finished_at": time.Now().UTC(), "idempotency_key": requestID(r), "approved_by_user_id": approverPrincipal.User.ID, "approved_by": approver}
	approvedData, _ := json.Marshal(details)
	saved, transitioned, err := s.Store.TransitionResource(r.Context(), "approval", id, "executing", store.ResourceWrite{Name: approval.Name, Status: "approved", OwnerUserID: approval.OwnerUserID, Data: approvedData}, principal(r).User.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !transitioned {
		apiError(w, r, 409, "approval_state_changed", "승인 실행 상태가 변경되어 결과를 저장하지 못했습니다")
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "approval.approve", "approval", strconv.FormatInt(id, 10), "success", input.Reason, flattenResource(approval), flattenResource(saved)))
	data(w, http.StatusOK, flattenResource(saved))
}

func approvalActorConflict(details map[string]any, actorID int64, actor, stage string) (string, string) {
	requestedBy, _ := details["requested_by"].(string)
	requestedByID := int64(numberFromAny(details["requested_by_user_id"]))
	requesterMatches := requestedByID > 0 && actorID > 0 && requestedByID == actorID
	if requestedByID == 0 {
		requesterMatches = requestedBy != "" && strings.EqualFold(strings.TrimSpace(requestedBy), strings.TrimSpace(actor))
	}
	if requesterMatches {
		switch stage {
		case "review":
			return "self_review_forbidden", "요청자는 자신의 요청을 검토할 수 없습니다"
		case "reject":
			return "self_rejection_forbidden", "요청자는 자신의 요청을 반려할 수 없습니다"
		default:
			return "self_approval_forbidden", "요청자는 자신의 요청을 승인할 수 없습니다"
		}
	}
	if stage == "approve" {
		if review, ok := details["review"].(map[string]any); ok {
			reviewer, _ := review["reviewed_by"].(string)
			reviewerID := int64(numberFromAny(review["reviewed_by_user_id"]))
			reviewerMatches := reviewerID > 0 && actorID > 0 && reviewerID == actorID
			if reviewerID == 0 {
				reviewerMatches = reviewer != "" && strings.EqualFold(strings.TrimSpace(reviewer), strings.TrimSpace(actor))
			}
			if reviewerMatches {
				return "reviewer_approver_conflict", "검토자와 승인자는 서로 달라야 합니다"
			}
		}
	}
	return "", ""
}
func (s *Server) approvalReject(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "승인 ID가 올바르지 않습니다")
		return
	}
	approval, err := s.Store.GetResource(r.Context(), "approval", id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if approval.Status != "pending" && approval.Status != "pending_review" {
		apiError(w, r, http.StatusConflict, "approval_closed", "이미 처리 중이거나 완료된 승인 요청입니다")
		return
	}
	inputBody, err := decodeApprovalAction(r)
	if err != nil {
		apiError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	var details map[string]any
	_ = json.Unmarshal(approval.Data, &details)
	if details == nil {
		details = map[string]any{}
	}
	cfg, err := approvalWorkflow(r.Context(), s.Store, details)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if err := validateApprovalReason(cfg.RequireReason, inputBody.Reason); err != nil {
		apiError(w, r, http.StatusBadRequest, "reason_required", err.Error())
		return
	}
	rejector := principal(r)
	if code, message := approvalActorConflict(details, rejector.User.ID, rejector.User.Username, "reject"); code != "" {
		apiError(w, r, http.StatusConflict, code, message)
		return
	}
	details["rejection"] = map[string]any{"reason": inputBody.Reason, "rejected_at": time.Now().UTC(), "rejected_by_user_id": rejector.User.ID, "rejected_by": rejector.User.Username, "from_status": approval.Status}
	raw, _ := json.Marshal(details)
	saved, transitioned, err := s.Store.TransitionResource(r.Context(), "approval", id, approval.Status, store.ResourceWrite{Name: approval.Name, Status: "rejected", OwnerUserID: approval.OwnerUserID, Data: raw}, principal(r).User.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !transitioned {
		apiError(w, r, 409, "approval_closed", "이미 처리 중이거나 완료된 승인 요청입니다")
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "approval.reject", "approval", strconv.FormatInt(id, 10), "success", inputBody.Reason, flattenResource(approval), flattenResource(saved)))
	data(w, http.StatusOK, flattenResource(saved))
}

func numberFromAny(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

func (s *Server) registerHubs(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/hubs", s.require("", s.hubsList))
	mux.HandleFunc("POST /api/v1/hubs", s.require("hubs:write", s.hubCreate))
	mux.HandleFunc("GET /api/v1/hubs/{id}", s.require("", s.hubGet))
	mux.HandleFunc("PUT /api/v1/hubs/{id}", s.require("", s.hubUpdate))
	mux.HandleFunc("DELETE /api/v1/hubs/{id}", s.require("", s.hubDelete))
	mux.HandleFunc("POST /api/v1/hubs/{id}/test", s.require("", s.hubTest))
	mux.HandleFunc("POST /api/v1/hubs/{id}/sync", s.require("", s.hubSync))
	mux.HandleFunc("POST /api/v1/servers/{id}/{action}", s.require("", s.serverAction))
}
func (s *Server) hubsList(w http.ResponseWriter, r *http.Request) {
	access, ok := scopedAccess(w, r, "hubs:read")
	if !ok {
		return
	}
	items, err := s.Store.ListHubsWithAccess(r.Context(), access)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, items)
}
func decodeHub(r *http.Request) (store.HubWrite, error) {
	var raw map[string]any
	if err := decodeJSON(r, &raw); err != nil {
		return store.HubWrite{}, err
	}
	alias := func(primary string, aliases ...string) any {
		if v, ok := raw[primary]; ok {
			return v
		}
		for _, a := range aliases {
			if v, ok := raw[a]; ok {
				return v
			}
		}
		return nil
	}
	input := store.HubWrite{}
	input.Name, _ = alias("name").(string)
	input.BaseURL, _ = alias("base_url", "url").(string)
	input.Network, _ = alias("network").(string)
	input.APIToken, _ = alias("api_token", "token").(string)
	if v, ok := alias("enabled").(bool); ok {
		input.Enabled = &v
	}
	if v, ok := alias("verify_tls", "tls_verify").(bool); ok {
		input.VerifyTLS = &v
	}
	input.CollectIntervalSeconds = int(numberFromAny(alias("collect_interval_seconds", "collection_interval")))
	return input, nil
}
func (s *Server) hubCreate(w http.ResponseWriter, r *http.Request) {
	input, err := decodeHub(r)
	if err != nil {
		apiError(w, r, 400, "invalid_hub", err.Error())
		return
	}
	item, err := s.Store.CreateHub(r.Context(), input)
	if err != nil {
		apiError(w, r, 400, "invalid_hub", err.Error())
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "hub.create", "hub", strconv.FormatInt(item.ID, 10), "success", "", nil, item))
	data(w, http.StatusCreated, item)
}
func (s *Server) hubGet(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "Hub ID가 올바르지 않습니다")
		return
	}
	if !scopedTargetAllowed(w, r, "hubs:read", id, "") {
		return
	}
	item, err := s.Store.GetHub(r.Context(), id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	data(w, http.StatusOK, item)
}
func (s *Server) hubUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "Hub ID가 올바르지 않습니다")
		return
	}
	if !scopedTargetAllowed(w, r, "hubs:write", id, "") {
		return
	}
	before, err := s.Store.GetHub(r.Context(), id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	input, err := decodeHub(r)
	if err != nil {
		apiError(w, r, 400, "invalid_hub", err.Error())
		return
	}
	item, err := s.Store.UpdateHub(r.Context(), id, input)
	if err != nil {
		apiError(w, r, 400, "invalid_hub", err.Error())
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "hub.update", "hub", strconv.FormatInt(id, 10), "success", "", before, item))
	data(w, http.StatusOK, item)
}
func (s *Server) hubDelete(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "Hub ID가 올바르지 않습니다")
		return
	}
	if !scopedTargetAllowed(w, r, "hubs:write", id, "") {
		return
	}
	before, err := s.Store.GetHub(r.Context(), id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if err := s.Store.DeleteHub(r.Context(), id); err != nil {
		handleStoreError(w, r, err)
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "hub.delete", "hub", strconv.FormatInt(id, 10), "success", "", before, nil))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) hubTest(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "Hub ID가 올바르지 않습니다")
		return
	}
	if !scopedTargetAllowed(w, r, "hubs:write", id, "") {
		return
	}
	hub, token, err := s.Store.GetHubCredential(r.Context(), id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	result := integration.TestConnection(ctx, integration.TestRequest{Type: "jupyterhub", Config: map[string]any{"base_url": hub.BaseURL, "verify_tls": hub.VerifyTLS}, Secret: token})
	healthStored, err := s.Store.UpdateHubHealthIfCurrent(r.Context(), hub, result.Success, result.Version, result.Error, map[string]any{"test_checked_at": result.CheckedAt})
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !healthStored {
		apiError(w, r, http.StatusConflict, "hub_configuration_changed", "연결 테스트 중 Hub 설정이 변경되어 이전 응답을 폐기했습니다. 다시 시도해 주세요")
		return
	}
	status := "success"
	if !result.Success {
		status = "failure"
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "hub.test", "hub", strconv.FormatInt(id, 10), status, result.Error, nil, map[string]any{"success": result.Success, "latency_ms": result.LatencyMS, "version": result.Version}))
	data(w, http.StatusOK, result)
}
func (s *Server) hubSync(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "Hub ID가 올바르지 않습니다")
		return
	}
	if !scopedTargetAllowed(w, r, "hubs:write", id, "") {
		return
	}
	hub, token, err := s.Store.GetHubCredential(r.Context(), id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	client, err := integration.NewJupyterHub(hub.BaseURL, token, hub.VerifyTLS)
	if err != nil {
		apiError(w, r, 400, "invalid_hub", err.Error())
		return
	}
	ctx, cancel := contextWithTimeout(r, 25*time.Second)
	defer cancel()
	info, err := client.Info(ctx)
	if err == nil {
		var users []integration.JupyterUser
		users, err = client.Users(ctx)
		if err == nil {
			var stored bool
			stored, err = s.Store.SyncHubUsersIfCurrent(ctx, hub, users)
			if err == nil && !stored {
				apiError(w, r, http.StatusConflict, "hub_configuration_changed", "동기화 중 Hub 설정이 변경되어 이전 응답을 폐기했습니다. 다시 시도해 주세요")
				return
			}
			if err == nil {
				healthStored, healthErr := s.Store.UpdateHubHealthIfCurrent(ctx, hub, true, info.Version, "", map[string]any{"users": len(users), "synced_at": time.Now().UTC()})
				if healthErr != nil {
					handleStoreError(w, r, healthErr)
					return
				}
				if !healthStored {
					apiError(w, r, http.StatusConflict, "hub_configuration_changed", "동기화 완료 처리 중 Hub 설정이 변경되어 이전 응답을 폐기했습니다. 다시 시도해 주세요")
					return
				}
				_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "hub.sync", "hub", strconv.FormatInt(id, 10), "success", "", nil, map[string]any{"users": len(users)}))
				data(w, http.StatusOK, map[string]any{"synced": true, "users": len(users), "version": info.Version})
				return
			}
		}
	}
	_, _ = s.Store.UpdateHubHealthIfCurrent(r.Context(), hub, false, info.Version, err.Error(), nil)
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "hub.sync", "hub", strconv.FormatInt(id, 10), "failure", err.Error(), nil, nil))
	apiError(w, r, 502, "hub_sync_failed", "JupyterHub 동기화에 실패했습니다: "+err.Error())
}

func (s *Server) serverAction(w http.ResponseWriter, r *http.Request) {
	id, err := intPath(r, "id")
	if err != nil {
		apiError(w, r, 400, "invalid_id", "서버 ID가 올바르지 않습니다")
		return
	}
	server, err := s.Store.GetServer(r.Context(), id)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if !scopedTargetAllowed(w, r, "servers:operate", server.HubID, server.Department) {
		return
	}
	action := r.PathValue("action")
	if action != "start" && action != "stop" && action != "restart" {
		apiError(w, r, 400, "invalid_action", "start, stop, restart만 지원합니다")
		return
	}
	workflow, err := s.Store.GetWorkflowConfig(r.Context())
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if workflow.RequiresApproval("server_action") {
		p := principal(r)
		initialStatus := "pending"
		if workflow.ManagerReviewEnabled {
			initialStatus = "pending_review"
		}
		payload, _ := json.Marshal(map[string]any{"resource_type": "server", "request_type": "server_action", "server_id": id, "action": action, "requested_by_user_id": p.User.ID, "requested_by": p.User.Username, "requested_at": time.Now().UTC(), "workflow": workflow})
		approval, err := s.Store.CreateResource(r.Context(), "approval", store.ResourceWrite{Name: fmt.Sprintf("서버 %d %s 요청", id, action), Status: initialStatus, OwnerUserID: &p.User.ID, Data: payload}, p.User.ID)
		if err != nil {
			handleStoreError(w, r, err)
			return
		}
		_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "server."+action+".request", "server", strconv.FormatInt(id, 10), "success", "", nil, map[string]any{"approval_id": approval.ID}))
		data(w, http.StatusAccepted, map[string]any{"approval_required": true, "manager_review_required": workflow.ManagerReviewEnabled, "status": initialStatus, "next_action": map[bool]string{true: "review", false: "approve_or_reject"}[workflow.ManagerReviewEnabled], "approval": flattenResource(approval)})
		return
	}
	if err := s.executeServerAction(r, id, action); err != nil {
		_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "server."+action, "server", strconv.FormatInt(id, 10), "failure", err.Error(), nil, nil))
		apiError(w, r, 502, "server_action_failed", err.Error())
		return
	}
	_ = s.Store.RecordAudit(r.Context(), requestAudit(r, "server."+action, "server", strconv.FormatInt(id, 10), "success", "", nil, nil))
	data(w, http.StatusAccepted, map[string]any{"approval_required": false, "action": action, "accepted": true})
}
func (s *Server) executeServerAction(r *http.Request, id int64, action string) error {
	server, hub, token, err := s.Store.GetServerActionCredential(r.Context(), id)
	if err != nil {
		return err
	}
	client, err := integration.NewJupyterHub(hub.BaseURL, token, hub.VerifyTLS)
	if err != nil {
		return err
	}
	ctx, cancel := contextWithTimeout(r, 25*time.Second)
	defer cancel()
	return client.ServerAction(ctx, server.Username, server.ServerName, action)
}
