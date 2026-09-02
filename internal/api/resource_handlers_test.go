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

func TestIntegrationSecretKeyIncludesSavedJupyterHubToken(t *testing.T) {
	if got := integrationSecretKey("jupyterhub"); got != "jupyterhub.api_token" {
		t.Fatalf("saved JupyterHub token key mismatch: %q", got)
	}
}
