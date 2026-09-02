package store

import (
	"encoding/json"
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
