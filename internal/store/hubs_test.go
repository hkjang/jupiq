package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hkjang/jupiq/internal/integration"
)

func TestPlanHubSnapshotMarksMissingServersAndEmptyUsers(t *testing.T) {
	plan := planHubSnapshot([]integration.JupyterUser{{Name: "user01", Servers: map[string]json.RawMessage{"gpu": json.RawMessage(`{"ready":true}`)}}})
	if plan.DeactivateAllUsers || len(plan.SeenUsers) != 1 || plan.SeenUsers[0] != "user01" {
		t.Fatalf("unexpected user plan: %#v", plan)
	}
	if _, ok := plan.ActiveServers["user01\x00gpu"]; !ok {
		t.Fatalf("active server missing: %#v", plan.ActiveServers)
	}
	if _, ok := plan.ActiveServers["stale-user\x00old"]; ok {
		t.Fatal("server absent from authoritative snapshot must remain stopped after pre-mark")
	}
	empty := planHubSnapshot(nil)
	if !empty.DeactivateAllUsers || len(empty.SeenUsers) != 0 || len(empty.ActiveServers) != 0 {
		t.Fatalf("empty snapshot must deactivate all users: %#v", empty)
	}
}

func TestValidateHubWriteRequiresOperationalFieldsAndBoundedInterval(t *testing.T) {
	valid := HubWrite{Name: "업무망", Network: "prod", BaseURL: "https://hub.internal", APIToken: "token", CollectIntervalSeconds: 60}
	if err := validateHubWrite(valid, true); err != nil {
		t.Fatalf("valid Hub rejected: %v", err)
	}
	for name, mutate := range map[string]func(*HubWrite){
		"network":  func(value *HubWrite) { value.Network = "" },
		"token":    func(value *HubWrite) { value.APIToken = "" },
		"interval": func(value *HubWrite) { value.CollectIntervalSeconds = 3601 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := validateHubWrite(candidate, true); err == nil {
				t.Fatalf("invalid Hub was accepted: %#v", candidate)
			}
		})
	}
}

func TestNormalizeHubWriteRemovesAccidentalWhitespace(t *testing.T) {
	input := HubWrite{Name: "  Hub  ", Network: "  prod  ", BaseURL: "  https://hub.internal///  ", APIToken: "  token  "}
	normalizeHubWrite(&input)
	if input.Name != "Hub" || input.Network != "prod" || input.BaseURL != "https://hub.internal" || input.APIToken != "token" {
		t.Fatalf("Hub values were not normalized: %#v", input)
	}
}

func TestJupyterSnapshotsUseStrictOperationalAllowlists(t *testing.T) {
	userRaw := safeJupyterUserSnapshot(integration.JupyterUser{
		Roles: []string{"user"}, Groups: []string{"research"}, Raw: json.RawMessage(`{"auth_state":{"token":"must-not-leak"}}`),
	})
	if strings.Contains(string(userRaw), "must-not-leak") || strings.Contains(string(userRaw), "auth_state") {
		t.Fatalf("sensitive user metadata leaked: %s", userRaw)
	}
	serverRaw := safeJupyterServerSnapshot(json.RawMessage(`{"ready":true,"project":"rag","progress":25,"user_options":{"api_key":"must-not-leak"},"environment":{"TOKEN":"secret"}}`))
	if strings.Contains(string(serverRaw), "must-not-leak") || strings.Contains(string(serverRaw), "environment") || !strings.Contains(string(serverRaw), `"project":"rag"`) {
		t.Fatalf("unexpected safe server snapshot: %s", serverRaw)
	}
}

func TestManagedUserRolesReadsOnlyBoundedAllowlistedRoles(t *testing.T) {
	longRole := strings.Repeat("r", 129)
	raw := json.RawMessage(`{"roles":[" user ","operator","user","",` + fmt.Sprintf("%q", longRole) + `],"groups":["must-not-be-a-role"],"auth_state":{"role":"must-not-leak"}}`)
	roles := managedUserRoles(raw)
	if len(roles) != 2 || roles[0] != "user" || roles[1] != "operator" {
		t.Fatalf("unexpected managed roles: %#v", roles)
	}
	if roles := managedUserRoles(json.RawMessage(`{"roles":"not-an-array"}`)); len(roles) != 0 {
		t.Fatalf("invalid role payload was accepted: %#v", roles)
	}
}

func TestHubCredentialBindingRequiresTokenForTargetOrTLSChanges(t *testing.T) {
	if hubCredentialBindingChanged("https://hub.internal/", true, "https://hub.internal", true) {
		t.Fatal("trailing slash-only change should keep the credential binding")
	}
	if !hubCredentialBindingChanged("https://hub.internal", true, "https://other.internal", true) {
		t.Fatal("target change did not invalidate the credential binding")
	}
	if !hubCredentialBindingChanged("https://hub.internal", true, "https://hub.internal", false) {
		t.Fatal("TLS verification change did not invalidate the credential binding")
	}
}
