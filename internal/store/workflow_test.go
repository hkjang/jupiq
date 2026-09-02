package store

import "testing"

func TestWorkflowRequiresApproval(t *testing.T) {
	cases := []struct {
		name string
		cfg  WorkflowConfig
		kind string
		want bool
	}{{"disabled", WorkflowConfig{ApprovalEnabled: false}, "server_action", false}, {"empty means all", WorkflowConfig{ApprovalEnabled: true}, "server_action", true}, {"listed", WorkflowConfig{ApprovalEnabled: true, RequestTypes: []string{"gpu", "server_action"}}, "server_action", true}, {"not listed", WorkflowConfig{ApprovalEnabled: true, RequestTypes: []string{"gpu"}}, "server_action", false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.RequiresApproval(tc.kind); got != tc.want {
				t.Fatalf("RequiresApproval=%v want %v", got, tc.want)
			}
		})
	}
}
