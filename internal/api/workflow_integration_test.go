package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
)

func TestApprovalWorkflowActorSeparationIntegration(t *testing.T) {
	dsn := os.Getenv("JUPIQ_INTEGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("set JUPIQ_INTEGRATION_TEST_DSN to run PostgreSQL integration coverage")
	}
	ctx := context.Background()
	cipher, err := secure.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	marker := fmt.Sprintf("workflow-api-%d", time.Now().UnixNano())
	users := map[string]store.User{}
	for _, role := range []string{"requester", "reviewer", "approver"} {
		username := marker + "-" + role
		var id int64
		if err := database.Pool.QueryRow(ctx, `INSERT INTO users(username,display_name,auth_source) VALUES($1,$1,'local') RETURNING id`, username).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users[role] = store.User{ID: id, Username: username}
	}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM audit_logs WHERE actor_username LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM resources WHERE name LIKE $1`, marker+"%")
		_, _ = database.Pool.Exec(ctx, `DELETE FROM users WHERE username LIKE $1`, marker+"%")
	}()

	workflow := store.WorkflowConfig{ApprovalEnabled: true, ManagerReviewEnabled: true, RequireReason: true, RequestTypes: []string{"policy_change"}}
	requester := users["requester"]
	newApproval := func(status string) store.Resource {
		raw, marshalErr := json.Marshal(map[string]any{
			"resource_type": "policy", "request_type": "policy_change", "requested_by_user_id": requester.ID, "requested_by": requester.Username, "workflow": workflow,
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		resource, createErr := database.CreateResource(ctx, "approval", store.ResourceWrite{Name: marker + " approval", Status: status, OwnerUserID: &requester.ID, Data: raw}, requester.ID)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return resource
	}
	server := &Server{Store: database}
	call := func(action string, approvalID int64, actor store.User, reason string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/"+strconv.FormatInt(approvalID, 10)+"/"+action, strings.NewReader(`{"reason":`+strconv.Quote(reason)+`}`))
		req.SetPathValue("id", strconv.FormatInt(approvalID, 10))
		req.Header.Set("X-Request-ID", marker+"-"+action)
		req = withPrincipal(req, auth.Principal{User: actor})
		response := httptest.NewRecorder()
		switch action {
		case "review":
			server.approvalReview(response, req)
		case "approve":
			server.approvalApprove(response, req)
		case "reject":
			server.approvalReject(response, req)
		default:
			t.Fatalf("unsupported test action %q", action)
		}
		return response
	}
	assertCode := func(response *httptest.ResponseRecorder, want int, code string) {
		t.Helper()
		if response.Code != want || (code != "" && !strings.Contains(response.Body.String(), `"code":"`+code+`"`)) {
			t.Fatalf("status=%d want=%d code=%q body=%s", response.Code, want, code, response.Body.String())
		}
	}

	approval := newApproval("pending_review")
	assertCode(call("review", approval.ID, users["requester"], "self"), http.StatusConflict, "self_review_forbidden")
	renamedRequester := users["requester"]
	renamedRequester.Username = marker + "-requester-renamed"
	assertCode(call("review", approval.ID, renamedRequester, "renamed self"), http.StatusConflict, "self_review_forbidden")
	assertCode(call("approve", approval.ID, users["approver"], "too early"), http.StatusConflict, "review_required")
	assertCode(call("review", approval.ID, users["reviewer"], "팀장 검토 완료"), http.StatusOK, "")
	assertCode(call("approve", approval.ID, users["reviewer"], "same actor"), http.StatusConflict, "reviewer_approver_conflict")
	renamedReviewer := users["reviewer"]
	renamedReviewer.Username = marker + "-reviewer-renamed"
	assertCode(call("approve", approval.ID, renamedReviewer, "renamed same actor"), http.StatusConflict, "reviewer_approver_conflict")
	assertCode(call("approve", approval.ID, users["requester"], "self"), http.StatusConflict, "self_approval_forbidden")
	assertCode(call("approve", approval.ID, users["approver"], "독립 승인 완료"), http.StatusOK, "")

	saved, err := database.GetResource(ctx, "approval", approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	var details map[string]any
	if err := json.Unmarshal(saved.Data, &details); err != nil {
		t.Fatal(err)
	}
	execution, _ := details["execution"].(map[string]any)
	if saved.Status != "approved" || execution["status"] != "approved" || execution["approved_by"] != users["approver"].Username {
		t.Fatalf("approved state lost actor attribution: status=%q details=%#v", saved.Status, details)
	}

	rejected := newApproval("pending_review")
	assertCode(call("reject", rejected.ID, users["requester"], "self"), http.StatusConflict, "self_rejection_forbidden")
	assertCode(call("reject", rejected.ID, users["approver"], "요건 미충족"), http.StatusOK, "")
	savedRejected, err := database.GetResource(ctx, "approval", rejected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if savedRejected.Status != "rejected" || !strings.Contains(string(savedRejected.Data), users["approver"].Username) {
		t.Fatalf("rejection state or actor missing: status=%q data=%s", savedRejected.Status, savedRejected.Data)
	}
}
