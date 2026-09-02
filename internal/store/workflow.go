package store

import (
	"context"
	"strings"
)

type WorkflowConfig struct {
	ApprovalEnabled      bool     `json:"approval_enabled"`
	ManagerReviewEnabled bool     `json:"manager_review_enabled"`
	RequireReason        bool     `json:"require_reason"`
	RequestTypes         []string `json:"request_types"`
}

func (s *Store) GetWorkflowConfig(ctx context.Context) (WorkflowConfig, error) {
	var cfg WorkflowConfig
	err := s.GetSetting(ctx, "workflow", &cfg)
	return cfg, err
}

func (cfg WorkflowConfig) RequiresApproval(requestType string) bool {
	if !cfg.ApprovalEnabled {
		return false
	}
	if len(cfg.RequestTypes) == 0 {
		return true
	}
	for _, configured := range cfg.RequestTypes {
		if strings.EqualFold(strings.TrimSpace(configured), requestType) {
			return true
		}
	}
	return false
}
