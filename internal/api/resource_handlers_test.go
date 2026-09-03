package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeResourceWriteCollectsFlatFields(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(`{"name":"RAG","status":"active","department":"AI","quota":10,"data":{"owner":"user01"}}`))
	input, err := decodeResourceWrite(r)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(input.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["department"] != "AI" || data["owner"] != "user01" || data["quota"].(float64) != 10 {
		t.Fatalf("flat data not preserved: %#v", data)
	}
}

func TestValidateApprovalReason(t *testing.T) {
	if validateApprovalReason(true, "  ") == nil {
		t.Fatal("required blank reason accepted")
	}
	if err := validateApprovalReason(true, "검토 완료"); err != nil {
		t.Fatal(err)
	}
	if err := validateApprovalReason(false, ""); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentProfilePermissionIsSeparateFromPersonalProfile(t *testing.T) {
	if got := resourcePermission("profile", "read"); got != "profiles:read" {
		t.Fatalf("profile catalog permission=%q", got)
	}
	if got := resourcePermission("project", "write"); got != "project:write" {
		t.Fatalf("unrelated resource permission changed: %q", got)
	}
}

func TestApprovalActorsMustBeSeparated(t *testing.T) {
	details := map[string]any{"requested_by": "requester", "review": map[string]any{"reviewed_by": "reviewer"}}
	if code, _ := approvalActorConflict(details, 0, "REQUESTER", "review"); code != "self_review_forbidden" {
		t.Fatalf("self review code=%q", code)
	}
	if code, _ := approvalActorConflict(details, 0, "requester", "approve"); code != "self_approval_forbidden" {
		t.Fatalf("self approval code=%q", code)
	}
	if code, _ := approvalActorConflict(details, 0, "requester", "reject"); code != "self_rejection_forbidden" {
		t.Fatalf("self rejection code=%q", code)
	}
	if code, _ := approvalActorConflict(details, 0, "reviewer", "approve"); code != "reviewer_approver_conflict" {
		t.Fatalf("reviewer/approver conflict code=%q", code)
	}
	if code, _ := approvalActorConflict(details, 0, "approver", "approve"); code != "" {
		t.Fatalf("independent approver rejected: %q", code)
	}
}

func TestApprovalActorIdentitySurvivesUsernameChanges(t *testing.T) {
	details := map[string]any{
		"requested_by_user_id": float64(42),
		"requested_by":         "old-requester-name",
		"review": map[string]any{
			"reviewed_by_user_id": float64(84),
			"reviewed_by":         "old-reviewer-name",
		},
	}
	if code, _ := approvalActorConflict(details, 42, "new-requester-name", "approve"); code != "self_approval_forbidden" {
		t.Fatalf("renamed requester bypassed self-approval protection: %q", code)
	}
	if code, _ := approvalActorConflict(details, 84, "new-reviewer-name", "approve"); code != "reviewer_approver_conflict" {
		t.Fatalf("renamed reviewer bypassed separation: %q", code)
	}
}

func TestGenericIntegrationSecretsExcludeHubTokens(t *testing.T) {
	if got := integrationSecretKey("jupyterhub"); got != "" {
		t.Fatalf("Hub token must come from the Hub record, got generic key %q", got)
	}
}
